//go:build linux

package transport

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

type fakeExtension struct {
	reply    string
	err      error
	calls    int
	lastPeer uint32
	lastID   string
}

func (f *fakeExtension) ManagedCallers() []Principal { return nil }

func (f *fakeExtension) Serve(peer uint32, id string, body []byte) (string, error) {
	f.calls++
	f.lastPeer, f.lastID = peer, id
	if f.err != nil {
		return "", f.err
	}
	return f.reply, nil
}

const m0Frame = `{"kind":"swarma-dispatcher m0-request/1","request_id":"a"}`

// Claim: with no extension an M0-kind post-hello frame still closes without a
// reply (the reference subset is unchanged); with an extension the frame is
// offered exactly once with the kernel peer UID and correlation ID and its
// reply is written after the same two pin rechecks; the closed two-member
// grammar is never offered to the extension; an extension ErrFatal closes
// the connection and propagates.
func TestExtensionOffered(t *testing.T) {
	session := func(t *testing.T, ext Extension, op string) (reply []byte, readErr, served error, checks int) {
		t.Helper()
		a, b := pair(t)
		done := make(chan error, 1)
		go func() {
			done <- serveOperationExt(b, time.Now(), func() error { checks++; return nil }, 42, ext)
			wakeClose(b)
		}()
		wire := wireFor(t, a)
		if err := wire.writeFrame(hello("a")); err != nil {
			t.Fatal(err)
		}
		if got, err := wire.readFrame(); err != nil || string(got) != helloResult("a") {
			t.Fatal("hello", err)
		}
		if err := wire.writeFrame(op); err != nil {
			t.Fatal(err)
		}
		reply, readErr = wire.readFrame()
		served = <-done
		return reply, readErr, served, checks
	}
	reply, readErr, served, _ := session(t, nil, m0Frame)
	if reply != nil || readErr == nil || !errors.Is(served, ErrInvalid) {
		t.Fatalf("NIL-EXT: %q %v %v", reply, readErr, served)
	}
	ext := &fakeExtension{reply: `{"ok":true,"request_id":"a","result":{"state":"x"},"v":1}`}
	reply, readErr, served, checks := session(t, ext, m0Frame)
	if readErr != nil || served != nil || string(reply) != ext.reply || ext.calls != 1 || ext.lastPeer != 42 || ext.lastID != "a" || checks != 2 {
		t.Fatalf("EXT: %q %v %v calls=%d peer=%d id=%q checks=%d", reply, readErr, served, ext.calls, ext.lastPeer, ext.lastID, checks)
	}
	grammar := &fakeExtension{reply: "never"}
	reply, readErr, served, _ = session(t, grammar, `{"op":"demo.launch","request_id":"a"}`)
	if readErr != nil || served != nil || string(reply) != invalid("a") || grammar.calls != 0 {
		t.Fatalf("GRAMMAR: %q %v %v calls=%d", reply, readErr, served, grammar.calls)
	}
	fatal := &fakeExtension{err: ErrFatal}
	reply, readErr, served, _ = session(t, fatal, m0Frame)
	if reply != nil || readErr == nil || !errors.Is(served, ErrFatal) {
		t.Fatalf("FATAL: %q %v %v", reply, readErr, served)
	}
}

// Claim: in the real accept loop an extension ErrFatal requests daemon
// shutdown with that cause, so serveExt returns an error wrapping ErrFatal;
// a non-fatal extension error closes only its connection and the loop keeps
// serving (control).
func TestExtensionFatalStopsServe(t *testing.T) {
	for _, fatal := range []bool{false, true} {
		e, c := fixtureEndpoint(t)
		pindir := protectedFixture(t, 0700)
		id := pindir.ids[len(pindir.ids)-1]
		cfg := serverConfig{commonConfig: c, Callers: []principal{{Dev: id.dev, Ino: id.ino, UID: uint32(os.Geteuid()), Dir: pindir.path}}}
		ctx, cancel := context.WithCancel(context.Background())
		stop := &shutdown{cancel: cancel}
		ext := &fakeExtension{err: ErrInvalid}
		if fatal {
			ext.err = ErrFatal
		}
		done := make(chan error, 1)
		go func() { done <- serveExt(ctx, e, cfg, stop, ext) }()
		f, err := connectSocket(e.dir, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		wire := wireFor(t, f)
		if err := wire.writeFrame(hello("a")); err != nil {
			t.Fatal(err)
		}
		if _, err := wire.readFrame(); err != nil {
			t.Fatal(err)
		}
		if err := wire.writeFrame(m0Frame); err != nil {
			t.Fatal(err)
		}
		if _, err := wire.readFrame(); err == nil {
			t.Fatal("reply after extension error")
		}
		wakeClose(f)
		if !fatal {
			// The loop still serves: a plain capacity exchange succeeds.
			g, err := connectSocket(e.dir, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if got, err := capacityExchange(g, time.Now(), "b"); err != nil || got != hold("b") {
				t.Fatal("loop stopped after a non-fatal extension error", err)
			}
			wakeClose(g)
			stop.request(nil)
		}
		select {
		case err := <-done:
			if fatal != errors.Is(err, ErrFatal) {
				t.Fatalf("fatal=%v result=%v", fatal, err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("serve did not return")
		}
		cancel()
		if err := e.close(); err != nil {
			t.Fatal(err)
		}
	}
}
