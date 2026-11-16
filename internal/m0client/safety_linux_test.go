//go:build linux

package m0client

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

// modelCallerTarget keeps a symlink target's ownership identical to the real
// object, so an ownership refusal cannot mask a missing no-follow check.
func (e *env) modelCallerTarget(name string) {
	e.t.Helper()
	var actual syscall.Stat_t
	must(e.t, syscall.Stat(name, &actual))
	e.changeStat(func(path string, st *syscall.Stat_t) {
		if path == name {
			st.Uid, st.Gid = e.policy.Association.UID, actual.Gid
		}
	})
}

func (e *env) restoreKey() {
	e.t.Helper()
	for _, name := range []string{e.keyPath, e.keyPath + ".real", e.keyPath + ".alias"} {
		must(e.t, os.RemoveAll(name))
	}
	must(e.t, os.WriteFile(e.keyPath, e.seed, 0600))
}

// Claim: post-fstat key chmod/link/unlink/rewrite/name replacement returns 64 without store/connect/launch; each restored key launches once.
func TestKeyFileRecheckedAfterRead(t *testing.T) {
	for _, kind := range []string{"mode", "hard link", "unlink recreate", "rewrite", "different content", "name replacement"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			b := e.newBackend(e.dispatchID, e.cancelID)
			l := e.serveBackend(b)
			original := e.host
			changed := false
			e.host.stat = func(fd int) (syscall.Stat_t, error) {
				st, err := original.fstat(fd)
				name, _ := os.Readlink("/proc/self/fd/" + fdString(fd))
				if err == nil && name == e.keyPath && !changed {
					// Return the original metadata, then change the object before
					// the bounded read. The shared leaf check must detect it.
					changed = true
					switch kind {
					case "mode":
						must(t, os.Chmod(e.keyPath, 0640))
					case "hard link":
						must(t, os.Link(e.keyPath, e.keyPath+".alias"))
					case "unlink recreate":
						must(t, os.Remove(e.keyPath))
						must(t, os.WriteFile(e.keyPath, e.seed, 0600))
					case "rewrite", "different content":
						// Identical-byte rewrite isolates metadata from the
						// association-key check. Also test changed contents.
						content := append([]byte(nil), e.seed...)
						if kind == "different content" {
							content[0] ^= 1
						}
						must(t, os.WriteFile(e.keyPath, content, 0600))
						clear(content)
						must(t, os.Chtimes(e.keyPath, time.Unix(1, 0), time.Unix(1, 0)))
					case "name replacement":
						// A different empty inode suffices; do not copy a seed.
						other := e.keyPath + ".replacement"
						must(t, os.WriteFile(other, nil, 0600))
						must(t, os.Rename(other, e.keyPath))
					}
				}
				return st, err
			}
			code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonKeyFileUnsafe) || !changed || e.storedCount() != 0 || len(l.drain()) != 0 {
				t.Fatalf("changed key: exit %d stdout %q stderr %q changed %v", code, stdout, stderr, changed)
			}
			if starts, _ := b.launcher.count(); starts != 0 {
				t.Fatal("changed key launched", starts)
			}
			e.host = original
			e.restoreKey()
			code, stdout, stderr = e.run("dispatch", "--config", e.cfgPath)
			starts, _ := b.launcher.count()
			if code != ExitAccepted || !strings.Contains(stdout, `"state":"started"`) || stderr != "" || starts != 1 || e.storedCount() != 1 || len(l.drain()) != 1 {
				t.Fatalf("restored key control: exit %d stdout %q stderr %q starts %d", code, stdout, stderr, starts)
			}
		})
	}
}

// Claim: state-directory symlink or writable ancestors return 64 without store/connect; restoring each protected path permits dispatch.
func TestStateDirectoryAncestors(t *testing.T) {
	for _, kind := range []string{"symlink", "group writable"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			ancestor := filepath.Join(e.root, "storage")
			must(t, os.Mkdir(ancestor, 0700))
			parent := filepath.Join(ancestor, "state")
			must(t, os.Rename(e.stateDir, parent))
			e.stateDir = parent
			if kind == "symlink" {
				link := filepath.Join(e.root, "storage-link")
				must(t, os.Symlink(ancestor, link))
				e.doc["state_dir"] = filepath.Join(link, "state")
			} else {
				must(t, os.Chmod(ancestor, 0720))
				e.doc["state_dir"] = parent
			}
			e.writeConfig()
			code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonState) || e.storedCount() != 0 || len(l.drain()) != 0 {
				t.Fatalf("state ancestor: exit %d stdout %q stderr %q", code, stdout, stderr)
			}
			must(t, os.Chmod(ancestor, 0700))
			e.doc["state_dir"] = parent
			e.writeConfig()
			if code, _, stderr := e.run("dispatch", "--config", e.cfgPath); code != ExitAccepted || e.storedCount() != 1 || len(l.drain()) != 1 {
				t.Fatalf("state ancestor control: exit %d stderr %q", code, stderr)
			}
		})
	}
}

// Claim: creating a subdirectory in a pinned ancestor is accepted, while persistent directory replacement returns 64; regular-file link counts remain pinned.
func TestDirectoryIdentityIgnoresChildCount(t *testing.T) {
	e, ancestor := keyDirectoryEnv(t)
	parent := filepath.Dir(e.keyPath)
	pin, err := e.host.pinDirectory(parent, e.policy.Association.UID)
	must(t, err)
	defer pin.close()
	must(t, os.Mkdir(filepath.Join(ancestor, "child"), 0700))
	if err := pin.recheck(parent, e.policy.Association.UID); err != nil {
		t.Fatal("child creation control", err)
	}
	before, err := os.Stat(e.keyPath)
	must(t, err)
	must(t, os.Link(e.keyPath, e.keyPath+".alias"))
	after, err := os.Stat(e.keyPath)
	must(t, err)
	if identity(*before.Sys().(*syscall.Stat_t)) == identity(*after.Sys().(*syscall.Stat_t)) {
		t.Fatal("regular-file link change was ignored")
	}
	must(t, os.Remove(e.keyPath+".alias"))
	must(t, os.Rename(parent, parent+".old"))
	must(t, os.Mkdir(parent, 0700))
	err = pin.recheck(parent, e.policy.Association.UID)
	if !errors.Is(err, errIdentity) {
		t.Fatal("directory replacement accepted", err)
	}
	var stdout, stderr bytes.Buffer
	if code := report(local(reasonKeyFileUnsafe, err), &stdout, &stderr); code != ExitLocal || stdout.Len() != 0 {
		t.Fatal("directory replacement refusal", code)
	}
	e.keep(stdout.Bytes(), stderr.Bytes())
}

// Claim: a fault-generated signature for the unchanged wire R returns 64 and no wire/store/connect; the corrected signature verifies and launches once.
func TestSignedWireSelfVerification(t *testing.T) {
	e := newEnv(t)
	b := e.newBackend(e.dispatchID, e.cancelID)
	l := e.serveBackend(b)
	c := e.config()
	plan := e.plan(m0.OpDispatch, e.dispatchID, "")
	finish := func(wire []byte, reason string, err error) Outcome {
		if reason != "" {
			return local(reason, err)
		}
		dir, err := c.host.openStateDir(c.StateDir)
		must(t, err)
		must(t, storeRequest(dir, e.dispatchID, wire))
		must(t, syscall.Close(dir))
		return Send(c, e.dispatchID, wire, true)
	}
	// Model a signing fault without a new hook: alter only the in-memory
	// preimage, not the R that SignedWire embeds. Its signature must not escape.
	plan.Request.Preimage[0] ^= 1
	wire, reason, err := SignRequest(c, plan)
	var stdout, stderr bytes.Buffer
	code := report(finish(wire, reason, err), &stdout, &stderr)
	e.keep(stdout.Bytes(), stderr.Bytes())
	if code != ExitLocal || wire != nil || reason != "signed_request_invalid" || m0.CodeOf(err) != m0.CodeSignatureInvalid || stdout.Len() != 0 || e.storedCount() != 0 || len(l.drain()) != 0 {
		t.Fatalf("faulty signature escaped: exit %d reason %q err %v", code, reason, err)
	}
	plan.Request.Preimage[0] ^= 1
	wire, reason, err = SignRequest(c, plan)
	if reason != "" || err != nil {
		t.Fatal("signature control", reason, err)
	}
	e.keep(wire)
	o := finish(wire, reason, err)
	e.keep(o.Reply)
	starts, _ := b.launcher.count()
	if o.Exit != ExitAccepted || starts != 1 || e.storedCount() != 1 || len(l.drain()) != 1 {
		t.Fatal("signature control did not launch", o.Exit, starts)
	}
}

// Claim: a peer close before or after the hello write returns 64 with stored-not-sent evidence; the same stored request succeeds against a responding peer.
func TestHelloCloseNeverMeansRequestSent(t *testing.T) {
	for _, mode := range []string{modeCloseBeforeHello, modeCloseAfterHello} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(mode)
			c := e.config()
			id := e.dispatchID
			request, reason, err := SignRequest(c, e.plan(m0.OpDispatch, id, ""))
			if reason != "" || err != nil {
				t.Fatal(reason, err)
			}
			dir, err := c.host.openStateDir(c.StateDir)
			must(t, err)
			must(t, storeRequest(dir, id, request))
			must(t, syscall.Close(dir))
			spec := specOf(c)
			if mode == modeCloseBeforeHello {
				spec.connected = func() { <-l.closed }
			}
			reply, sent, stage, err := exchange(spec, id, request)
			l.close()
			l.mu.Lock()
			streams := append([][]byte(nil), l.streams...)
			l.mu.Unlock()
			want := []byte(nil)
			if mode == modeCloseAfterHello {
				want = frameOf(helloFrame(id))
			}
			if len(streams) != 1 || !bytes.Equal(streams[0], want) || stage != stageHello || err == nil || sent != 0 {
				t.Fatalf("hello ordering: streams %d sent %d stage %q err %v", len(streams), sent, stage, err)
			}
			var stdout, stderr bytes.Buffer
			code := report(classify(id, true, sent, reply, stage, err), &stdout, &stderr)
			e.keep(stdout.Bytes(), stderr.Bytes())
			if code != ExitLocal || stdout.Len() != 0 || !strings.Contains(stderr.String(), stageHello) || !strings.Contains(stderr.String(), "request_id="+id+" stored, not sent") {
				t.Fatalf("unwritten request: exit %d stdout %q stderr %q", code, stdout.String(), stderr.String())
			}
			control := e.listen(modeAccept)
			if code, _, stderr := e.resend(id); code != ExitAccepted || len(control.drain()) != 1 {
				t.Fatalf("hello control: exit %d stderr %q", code, stderr)
			}
		})
	}
}

// Claim: zero request-frame bytes return 64, while even one prefix byte makes a missing reply unknown (17); a matching reply returns 0.
func TestRequestFrameByteBoundary(t *testing.T) {
	e := newEnv(t)
	id := e.dispatchID
	for _, sent := range []int{0, 1, 4, 100} {
		var stdout, stderr bytes.Buffer
		o := classify(id, true, sent, nil, stageRequest, io.ErrUnexpectedEOF)
		code := report(o, &stdout, &stderr)
		e.keep(stdout.Bytes(), stderr.Bytes())
		want := ExitUnknown
		if sent == 0 {
			want = ExitLocal
		}
		if code != want || stdout.Len() != 0 {
			t.Fatalf("request bytes %d: exit %d", sent, code)
		}
	}
	if o := classify(id, true, 100, []byte(acceptedEnvelope(id)), "", nil); o.Exit != ExitAccepted {
		t.Fatal("reply control", o)
	}
}

// Claim: a completed listener stream enters the end-of-test secrecy scan even when the test never drains the listener.
func TestListenerStreamsAlwaysScanned(t *testing.T) {
	e := newEnv(t)
	l := e.listen(modeAccept)
	if code, _, stderr := e.dispatch(e.dispatchID); code != ExitAccepted {
		t.Fatal("dispatch control", code, stderr)
	}
	l.close()
	if len(l.streams) != 1 || len(l.streams[0]) == 0 {
		t.Fatal("listener did not capture the control exchange")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, blob := range e.outputs {
		if bytes.Equal(blob, l.streams[0]) {
			return
		}
	}
	t.Fatal("listener stream is absent from the secrecy scan")
}
