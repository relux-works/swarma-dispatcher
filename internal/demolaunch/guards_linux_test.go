//go:build linux

package demolaunch

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/frame"
)

// Literal field mutations remain valid JSON, so torn-record recovery cannot
// mask semantic validation. Each family has its intact startup control.
func TestParseableStateFields(t *testing.T) {
	at := `{"request_id":"r","request_digest":"` + strings.Repeat("a", 64) + `","execution_id":"EX-0123456789abcdef0123456789abcdef","time":"2026-01-01T00:00:00Z"}`
	intent := `{"request_id":"stop-1","execution_id":"EX-0123456789abcdef0123456789abcdef","grace_seconds":10,"time":"2026-01-01T00:00:00Z"}`
	stopped := `{"execution_id":"EX-0123456789abcdef0123456789abcdef","timing":"timely","closed":false,"code":""}`
	cases := []struct{ name, record, find, replace string }{
		{"valid", "", "", ""},
		{"attempt-id", "attempt", `"request_id":"r"`, `"request_id":""`},
		{"digest", "attempt", strings.Repeat("a", 64), strings.Repeat("g", 64)},
		{"execution", "attempt", "EX-0123456789abcdef0123456789abcdef", "EX-short"},
		{"time", "attempt", "2026-01-01T00:00:00Z", "not-time"},
		{"intent-id", "stop", `"request_id":"stop-1"`, `"request_id":""`},
		{"intent-execution", "stop", "EX-0123456789abcdef0123456789abcdef", "EX-short"},
		{"grace-low", "stop", `"grace_seconds":10`, `"grace_seconds":0`},
		{"grace-high", "stop", `"grace_seconds":10`, `"grace_seconds":31`},
		{"intent-time", "stop", "2026-01-01T00:00:00Z", "not-time"},
		{"binding", "stop", "EX-0123456789abcdef0123456789abcdef", "EX-ffffffffffffffffffffffffffffffff"},
		{"stopped-binding", "stopped", "EX-0123456789abcdef0123456789abcdef", "EX-ffffffffffffffffffffffffffffffff"},
		{"timing", "stopped", `"timing":"timely","closed":false`, `"timing":"later","closed":true`},
		{"code", "stopped", `"code":""`, `"code":"wrong"`},
		{"missed-open", "stopped", "timely", "missed"},
		{"unknown-open", "stopped", "timely", "unknown"},
		{"uncertain-open", "stopped", `"code":""`, `"code":"latch_uncertain"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := fixture(t, "good")
			docs := map[string]string{"attempt": at, "stop": intent, "stopped": stopped}
			if tc.record != "" {
				docs[tc.record] = strings.Replace(docs[tc.record], tc.find, tc.replace, 1)
			}
			for _, kind := range []string{"attempt", "stop", "stopped"} {
				must(t, h.e.store.create(kind+"-"+gen, []byte(docs[kind])))
			}
			if tc.record == "attempt" {
				if _, err := parseAttempt([]byte(docs["attempt"])); !errors.Is(err, ErrInvalid) {
					t.Fatal("attempt parser", tc.name, err)
				}
			}
			if tc.record == "stop" && tc.name != "binding" {
				if _, err := parseIntent([]byte(docs["stop"])); !errors.Is(err, ErrInvalid) {
					t.Fatal("intent parser", tc.name, err)
				}
			}
			before := directoryBytes(t, filepath.Join(h.root, "state"))
			_, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
			if tc.name == "valid" {
				must(t, err)
			} else if !errors.Is(err, ErrInvalid) {
				t.Fatal(tc.name, err)
			}
			for n, b := range before {
				if string(fileBytes(t, h, n)) != b {
					t.Fatal("evidence changed", n)
				}
			}
		})
	}
}

// Prefix bounds execute the demo recvmsg copy, not just internal/frame.
// Frozen deadlines are checked arithmetically and with elapsed stage intake.
func TestDemoFrameBoundsAndBudgets(t *testing.T) {
	for _, size := range []uint32{0, 1, 131072, 131073} {
		t.Run(jsonNumber(int(size)), func(t *testing.T) {
			a, b := socketPair(t)
			w := testWire(t, b)
			sent := make(chan error, 1)
			go func() {
				var prefix [4]byte
				binary.BigEndian.PutUint32(prefix[:], size)
				_, err := a.Write(prefix[:])
				if err == nil && size <= 131072 {
					_, err = a.Write(bytes.Repeat([]byte{'x'}, int(size)))
				}
				sent <- err
			}()
			got, err := w.readFrame()
			switch size {
			case 0:
				if !errors.Is(err, frame.ErrEmptyFrame) {
					t.Fatal(err)
				}
			case 131073:
				if !errors.Is(err, frame.ErrFrameTooLarge) {
					t.Fatal(err)
				}
			default:
				must(t, err)
				if len(got) != int(size) {
					t.Fatal(len(got))
				}
			}
			must(t, <-sent)
		})
	}
	now := time.Now()
	if stageBudget != 2*time.Second || sessionBudget != 55*time.Second || !stageDeadline(now, now).Equal(now.Add(2*time.Second)) || !stageDeadline(now.Add(-54*time.Second), now).Equal(now.Add(time.Second)) {
		t.Fatal("frozen budgets")
	}
	_, b := socketPair(t)
	w := testWire(t, b)
	start := time.Now()
	_, err := w.readFrame()
	elapsed := time.Since(start)
	if !errors.Is(err, os.ErrDeadlineExceeded) || elapsed < time.Second || elapsed > 4*time.Second {
		t.Fatal("stage wait", elapsed, err)
	}
}

// The client must accept a delayed valid reply but reject wrong hello, members,
// state and receipt predicates. Real socket I/O runs for each control.
func TestClientReplyValidation(t *testing.T) {
	for _, kind := range []string{"valid", "slow", "hello", "members", "state", "receipt"} {
		t.Run(kind, func(t *testing.T) {
			a, b := socketPair(t)
			done := make(chan error, 1)
			go func() {
				w, err := newSocketIO(b, time.Now())
				if err != nil {
					done <- err
					return
				}
				defer wakeClose(b)
				if _, err = w.readFrame(); err != nil {
					done <- err
					return
				}
				hello := `{"protocol":"swarma-demo-dispatcher/1"}`
				if kind == "hello" {
					hello = `{"protocol":"dispatcher/1"}`
				}
				if err = w.writeFrame(hello); err != nil {
					done <- err
					return
				}
				if kind == "hello" {
					done <- nil
					return
				}
				if _, err = w.readFrame(); err != nil {
					done <- err
					return
				}
				reply := `{"state":"facts","closed":false}`
				switch kind {
				case "slow":
					time.Sleep(3 * time.Second)
				case "members":
					reply = `{"state":"facts","closed":false,"extra":1}`
				case "state":
					reply = `{"state":"started","closed":false}`
				case "receipt":
					reply = `{"state":"receipt-seen","closed":false,"receipt":{"execution_id":"EX-0123456789abcdef0123456789abcdef","epoch":2,"pid":1,"start_time":2,"boot_id":"` + boot + `","session_id":1}}`
				}
				done <- w.writeFrame(reply)
			}()
			reply, err := clientExchange(a, time.Now(), []byte(`{"op":"demo.status","account":"demo"}`))
			if kind == "valid" || kind == "slow" {
				must(t, err)
				if reply != `{"state":"facts","closed":false}` {
					t.Fatal(reply)
				}
			} else if err == nil {
				t.Fatal("bad reply accepted", reply)
			}
			<-done
		})
	}
}

// Actual kernel credentials reach verifyPeer. Expected-UID mismatch is injected;
// no second UID or root-owned endpoint is claimed by this non-root witness.
func TestKernelPeerAndSocketIdentity(t *testing.T) {
	a, _ := socketPair(t)
	uid, err := peerUID(a)
	must(t, err)
	if uid != uint32(os.Geteuid()) {
		t.Fatal(uid)
	}
	must(t, verifyPeer(a, uid))
	if !errors.Is(verifyPeer(a, uid+1), ErrIdentity) {
		t.Fatal("wrong peer accepted")
	}
	h := fixture(t, "good")
	ep, _, _ := serveFixture(t, h, "valid")
	id, err := atIdentity(ep.dir, endpointName)
	must(t, err)
	c := commonConfig{ServiceUID: uint32(os.Geteuid()), ConnectGID: uint32(os.Getegid())}
	if !socketIdentity(id, c) {
		t.Fatal("positive socket rejected")
	}
	for _, field := range []string{"uid", "gid", "type", "mode", "links"} {
		bad := id
		switch field {
		case "uid":
			bad.uid++
		case "gid":
			bad.gid++
		case "type":
			bad.mode = bad.mode&07777 | syscall.S_IFREG
		case "mode":
			bad.mode ^= 0002
		case "links":
			bad.links++
		}
		if socketIdentity(bad, c) {
			t.Fatal(field)
		}
	}
}

// The Client composition seam substitutes protected configuration loading and
// distinct runtime roles only. Pin, socket metadata and SO_PEERCRED stay real.
func TestClientComposedGate(t *testing.T) {
	for _, variant := range []string{"valid", "pin", "socket-mode", "socket-group", "socket-owner", "endpoint-error", "runtime-error", "load-error", "pin-recheck", "endpoint-recheck", "socket-recheck"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			ep, _, pinPath := serveFixture(t, h, "valid")
			d, err := pinDirectory(pinPath, uint32(os.Geteuid()))
			must(t, err)
			id := d.ids[len(d.ids)-1]
			d.close()
			c := clientConfig{commonConfig: commonConfig{ServiceUID: uint32(os.Geteuid()), ServiceGID: uint32(os.Getegid()), ConnectGID: uint32(os.Getegid())}, Caller: principal{Dev: id.dev, Ino: id.ino, UID: id.uid, Dir: pinPath}}
			switch variant {
			case "pin":
				c.Caller.Ino++
			case "socket-mode":
				must(t, os.Chmod(filepath.Join(h.root, endpointName), 0600))
			case "socket-group":
				c.ConnectGID++
			case "socket-owner":
				c.ServiceUID++
			}
			load := func(string) (clientConfig, error) {
				if variant == "load-error" {
					return c, ErrConfig
				}
				return c, nil
			}
			runtime := func(commonConfig, uint32, bool) error {
				if variant == "runtime-error" {
					return ErrIdentity
				}
				return nil
			}
			endpoint := func(commonConfig) (*directory, error) {
				if variant == "endpoint-error" {
					return nil, ErrIdentity
				}
				if variant == "pin-recheck" {
					must(t, os.Chmod(pinPath, 0750))
				}
				return pinDirectory(ep.dir.path, uint32(os.Geteuid()))
			}
			observe := func(d *directory) {
				if variant == "endpoint-recheck" {
					must(t, os.Chmod(d.path, 0751))
				}
				if variant == "socket-recheck" {
					must(t, os.Chmod(filepath.Join(d.path, endpointName), 0600))
				}
			}
			defer os.Chmod(h.root, 0711)
			reply, err := clientWith("fixture", "status", []byte(`{"op":"demo.status","account":"demo"}`), load, runtime, endpoint, observe)
			if variant == "valid" {
				must(t, err)
				if !strings.Contains(reply, `"state":"facts"`) {
					t.Fatal(reply)
				}
			} else {
				want := ErrIdentity
				if variant == "load-error" {
					want = ErrConfig
				}
				if !errors.Is(err, want) || reply != "" {
					t.Fatal(reply, err)
				}
			}
		})
	}
}
