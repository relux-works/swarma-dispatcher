//go:build linux

package m0client

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

const maxSerialValue = 9007199254740991

// Claim: for dispatch and for cancel the daemon's own decoder accepts the
// tool's wire bytes, Verify passes, every decoded member equals the
// configuration, the wire bytes are the canonical form and the preimage is
// within the closed maximum (and reaches exactly 964 / 1019 bytes with
// 16-digit serials). Control beside it: the same wire with one signature
// character changed still decodes but does not verify.
func TestRoundTripThroughDaemonDecoder(t *testing.T) {
	for _, serials := range []string{"fixture", "maximal"} {
		t.Run(serials, func(t *testing.T) {
			e := newEnv(t)
			if serials == "maximal" {
				e.doc["policy"].(map[string]any)["revision"] = maxSerialValue
				a := e.doc["association"].(map[string]any)
				a["revision"], a["registration_number"], a["master_epoch"] = maxSerialValue, maxSerialValue, maxSerialValue
				e.writeConfig()
			}
			c := e.config()
			target := randomID(t)
			for _, op := range []m0.Op{m0.OpDispatch, m0.OpCancel} {
				id, bound, want := randomID(t), m0.MaxDispatchPreimage, ""
				if op == m0.OpCancel {
					bound, want = m0.MaxCancelPreimage, target
				}
				wire, reason, err := SignRequest(c, op, id, want)
				if reason != "" || err != nil {
					t.Fatalf("%s: sign refused: %s %v", op, reason, err)
				}
				e.keep(wire)
				r, err := m0.DecodeRequest(wire)
				must(t, err)
				if err := r.Verify(); err != nil {
					t.Fatalf("%s: daemon verification: %v", op, err)
				}
				if r.Op != op || r.RequestID != id || r.DispatchRequestID != want || r.Receiver != c.Receiver || r.Policy != c.Policy || r.Association != c.Association || r.Worker != c.Worker || r.Plan != c.Plan || r.Auth.Key != c.Association.Key {
					t.Fatalf("%s: decoded members differ from the configuration: %+v", op, r)
				}
				if len(r.Preimage) > bound || !bytes.HasPrefix(r.Preimage, []byte(m0.SigningDomain+"\n")) {
					t.Fatalf("%s: preimage %d bytes, bound %d", op, len(r.Preimage), bound)
				}
				if serials == "maximal" && len(r.Preimage) != bound {
					t.Fatalf("%s: maximal preimage is %d bytes, want %d", op, len(r.Preimage), bound)
				}
				again, err := m0.SignedWire(r.Canonical, r.Auth.Key, r.Auth.Sig)
				must(t, err)
				if !bytes.Equal(again, wire) {
					t.Fatalf("%s: wire bytes are not the canonical signed form", op)
				}
				flipped := "A"
				if strings.HasPrefix(r.Auth.Sig, "A") {
					flipped = "B"
				}
				tampered := bytes.Replace(wire, []byte(`"sig":"`+r.Auth.Sig[:1]), []byte(`"sig":"`+flipped), 1)
				bad, err := m0.DecodeRequest(tampered)
				must(t, err)
				if m0.CodeOf(bad.Verify()) != m0.CodeSignatureInvalid {
					t.Fatalf("%s: tampered signature verified", op)
				}
			}
		})
	}
}

// Claim: against the landed runtime behind a real Unix socket the tool's
// dispatch is accepted (ok:true, state started, exit 0) with exactly one
// launch, the stored file holds the bytes the listener received, and the
// owned cancel of that dispatch is accepted too (exit 0, one stop).
func TestEndToEndDispatchAndCancel(t *testing.T) {
	e := newEnv(t)
	dispatchID, cancelID := randomID(t), randomID(t)
	b := e.newBackend(dispatchID, cancelID)
	l := e.serveBackend(b)
	code, stdout, stderr := e.dispatch(dispatchID)
	if code != ExitAccepted || !strings.HasPrefix(stdout, `{"ok":true,"request_id":"`+dispatchID+`"`) || !strings.Contains(stdout, `"state":"started"`) || stderr != "" {
		t.Fatalf("dispatch: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if starts, _ := b.launcher.count(); starts != 1 {
		t.Fatalf("dispatch: starts=%d", starts)
	}
	streams := l.drain()
	stored, err := os.ReadFile(e.storedPath(dispatchID))
	must(t, err)
	if len(streams) != 1 || !bytes.Equal(requestFrame(t, streams[0]), stored) {
		t.Fatalf("dispatch: listener saw %d connections or bytes that differ from the stored request", len(streams))
	}
	var st syscall.Stat_t
	must(t, syscall.Stat(e.storedPath(dispatchID), &st))
	if st.Mode&07777 != 0600 || st.Nlink != 1 {
		t.Fatalf("stored request mode %#o links %d", st.Mode&07777, st.Nlink)
	}
	code, stdout, stderr = e.cancel(cancelID, dispatchID)
	if code != ExitAccepted || !strings.HasPrefix(stdout, `{"ok":true,"request_id":"`+cancelID+`"`) || stderr != "" {
		t.Fatalf("cancel: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if starts, stops := b.launcher.count(); starts != 1 || stops != 1 {
		t.Fatalf("cancel: starts=%d stops=%d", starts, stops)
	}
}

// Claim: a seed whose public key is not association.key refuses
// key_not_associated with exit 64, no connection and no stored request.
// Control beside it: the associated seed in the same installation is accepted.
func TestSeedNotAssociated(t *testing.T) {
	e := newEnv(t)
	l := e.listen(modeAccept)
	other := make([]byte, ed25519.SeedSize)
	_, err := rand.Read(other)
	must(t, err)
	must(t, os.WriteFile(e.keyPath, other, 0600))
	code, stdout, stderr := e.dispatch(randomID(t))
	if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonKeyNotAssociated) {
		t.Fatalf("foreign seed: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if n := len(l.drain()); n != 0 || e.storedCount() != 0 {
		t.Fatalf("foreign seed: %d connections, %d stored requests", n, e.storedCount())
	}
	if found := findSeed(other, []byte(stdout), []byte(stderr)); len(found) != 0 {
		t.Fatalf("foreign seed printed: %v", found)
	}
	must(t, os.WriteFile(e.keyPath, e.seed, 0600))
	if code, _, stderr := e.dispatch(randomID(t)); code != ExitAccepted {
		t.Fatalf("control: exit %d stderr %q", code, stderr)
	}
	if n := len(l.drain()); n != 1 {
		t.Fatalf("control: %d connections", n)
	}
}

// signedBy returns wire bytes for the configured R that carry the associated
// key but a signature made with another seed.
func signedBy(t *testing.T, c *Config, id string, seed []byte) []byte {
	t.Helper()
	r, err := BuildUnsigned(c, m0.OpDispatch, id, "")
	must(t, err)
	sig, err := m0.SignatureSpelling(ed25519.Sign(ed25519.NewKeyFromSeed(seed), r.Preimage))
	must(t, err)
	wire, err := m0.SignedWire(r.Canonical, c.Association.Key, sig)
	must(t, err)
	return wire
}

// Claim: wire bytes that carry the associated key but were signed by another
// key are answered signature_invalid by the daemon (exit 16) and launch
// nothing. Control beside it: the same R signed by the associated seed is
// accepted by the same daemon (exit 0).
func TestForeignSignatureRefusedByDaemon(t *testing.T) {
	e := newEnv(t)
	id := randomID(t)
	b := e.newBackend(id, randomID(t))
	e.serveBackend(b)
	c := e.config()
	other := make([]byte, ed25519.SeedSize)
	_, err := rand.Read(other)
	must(t, err)
	o := Send(c, id, signedBy(t, c, id, other), false)
	e.keep(o.Reply)
	if o.Exit != ExitRefused || !strings.Contains(string(o.Reply), `"code":"signature_invalid"`) {
		t.Fatalf("foreign signature: exit %d reply %q %v", o.Exit, o.Reply, o.Detail)
	}
	if starts, _ := b.launcher.count(); starts != 0 {
		t.Fatalf("foreign signature launched: starts=%d", starts)
	}
	o = Send(c, id, signedBy(t, c, id, e.seed), false)
	if o.Exit != ExitAccepted {
		t.Fatalf("control: exit %d reply %q %v", o.Exit, o.Reply, o.Detail)
	}
}

// Claim: one member of R changed after signing is refused by the daemon
// (exit 16) and launches nothing: a changed receiver, and a changed
// request_id sent under its own correlation ID (signature_invalid). The
// tool's resend refuses an altered stored file locally (exit 64, nothing
// sent). Control beside each: the unaltered bytes are accepted (exit 0).
func TestAlteredRequestRefused(t *testing.T) {
	e := newEnv(t)
	id := randomID(t)
	b := e.newBackend(id, randomID(t))
	l := e.serveBackend(b)
	c := e.config()
	wire, reason, err := SignRequest(c, m0.OpDispatch, id, "")
	if reason != "" || err != nil {
		t.Fatal(reason, err)
	}
	flip := func(s string) string {
		if s[0] == '0' {
			return "1" + s[1:]
		}
		return "0" + s[1:]
	}
	receiver := bytes.Replace(wire, []byte(`"receiver":"`+c.Receiver), []byte(`"receiver":"`+flip(c.Receiver)), 1)
	o := Send(c, id, receiver, false)
	if o.Exit != ExitRefused || bytes.Equal(receiver, wire) {
		t.Fatalf("altered receiver: exit %d reply %q %v", o.Exit, o.Reply, o.Detail)
	}
	otherID := flip(id)
	renamed := bytes.Replace(wire, []byte(`"request_id":"`+id), []byte(`"request_id":"`+otherID), 1)
	// The protected plan is re-pinned to the altered ID so that the signature
	// is the only thing left to refuse it.
	b.source.edit(func(i *m0.Installed) { i.Catalog = e.catalog(otherID, randomID(t)) })
	o = Send(c, otherID, renamed, false)
	if o.Exit != ExitRefused || !strings.Contains(string(o.Reply), `"code":"signature_invalid"`) {
		t.Fatalf("altered request_id: exit %d reply %q %v", o.Exit, o.Reply, o.Detail)
	}
	b.source.edit(func(i *m0.Installed) { i.Catalog = e.catalog(id, randomID(t)) })
	if starts, _ := b.launcher.count(); starts != 0 {
		t.Fatalf("altered request launched: starts=%d", starts)
	}
	dir, err := openStateDir(e.stateDir)
	must(t, err)
	must(t, storeRequest(dir, id, receiver))
	must(t, syscall.Close(dir))
	before := len(l.drain())
	code, stdout, stderr := e.resend(id)
	if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonStoredRequest) || len(l.drain()) != before {
		t.Fatalf("altered stored file: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	must(t, os.WriteFile(e.storedPath(id), wire, 0600))
	if code, stdout, stderr := e.resend(id); code != ExitAccepted || !strings.Contains(stdout, `"state":"started"`) {
		t.Fatalf("control: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if starts, _ := b.launcher.count(); starts != 1 {
		t.Fatalf("control: starts=%d", starts)
	}
}

// Claim: the tool's exact wire bytes handed to the runtime with a kernel peer
// that is not association.uid are refused caller_not_permitted and launch
// nothing. Control beside it: the same bytes with the association UID as the
// peer are accepted. (The kernel UID cannot be varied in a single-UID run.)
func TestPeerOtherThanAssociationUID(t *testing.T) {
	e := newEnv(t)
	id := randomID(t)
	b := e.newBackend(id, randomID(t))
	wire, reason, err := SignRequest(e.config(), m0.OpDispatch, id, "")
	if reason != "" || err != nil {
		t.Fatal(reason, err)
	}
	uid := e.policy.Association.UID
	if _, err := b.rt.Handle(context.Background(), uid+1, wire); m0.CodeOf(err) != m0.CodeCallerNotPermitted {
		t.Fatalf("other peer: %v", err)
	}
	if starts, _ := b.launcher.count(); starts != 0 {
		t.Fatalf("other peer launched: starts=%d", starts)
	}
	res, err := b.rt.Handle(context.Background(), uid, wire)
	if err != nil || res.State != "started" {
		t.Fatalf("control: %+v %v", res, err)
	}
}

// Claim: resend of an accepted dispatch returns the original result without
// the key file and starts nothing new (one launch at the launcher seam), and
// a new dispatch for the same worker is refused by the daemon: as issued
// because its ID is not the protected plan's, and with the plan re-pinned to
// it by the single-launch budget (m0_launch_budget_consumed); still one launch.
// Control: the first dispatch is accepted.
func TestResendAndSingleLaunchBudget(t *testing.T) {
	e := newEnv(t)
	first, cancelID := randomID(t), randomID(t)
	b := e.newBackend(first, cancelID)
	e.serveBackend(b)
	code, original, stderr := e.dispatch(first)
	if code != ExitAccepted {
		t.Fatalf("control: exit %d stderr %q", code, stderr)
	}
	must(t, os.Remove(e.keyPath))
	code, replayed, stderr := e.resend(first)
	if code != ExitAccepted || replayed != original {
		t.Fatalf("resend: exit %d %q, original %q, stderr %q", code, replayed, original, stderr)
	}
	if starts, _ := b.launcher.count(); starts != 1 {
		t.Fatalf("resend: starts=%d", starts)
	}
	must(t, os.WriteFile(e.keyPath, e.seed, 0600))
	second := randomID(t)
	code, stdout, _ := e.dispatch(second)
	if code != ExitRefused || !strings.Contains(stdout, `"ok":false`) {
		t.Fatalf("second dispatch as issued: exit %d %q", code, stdout)
	}
	b.source.edit(func(i *m0.Installed) { i.Catalog = e.catalog(second, cancelID) })
	code, stdout, _ = e.resend(second)
	if code != ExitRefused || !strings.Contains(stdout, `"code":"m0_launch_budget_consumed"`) {
		t.Fatalf("second dispatch with the plan re-pinned: exit %d %q", code, stdout)
	}
	if starts, _ := b.launcher.count(); starts != 1 {
		t.Fatalf("second dispatch: starts=%d", starts)
	}
}

// Claim: a key file with mode 0640, a symlink to a good key file, a second
// hard link, 31 bytes, 33 bytes or mode 0400 refuses key_file_unsafe with
// exit 64, no connection and no stored request. Control beside them: the
// untouched 0600 single-link 32-byte file is accepted (exit 0).
func TestKeyFileShape(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, e *env)
		unsafe  bool
	}{
		{"control", func(*testing.T, *env) {}, false},
		{"mode 0640", func(t *testing.T, e *env) { must(t, os.Chmod(e.keyPath, 0640)) }, true},
		{"mode 0400", func(t *testing.T, e *env) { must(t, os.Chmod(e.keyPath, 0400)) }, true},
		{"symlink", func(t *testing.T, e *env) {
			real := e.keyPath + ".real"
			must(t, os.Rename(e.keyPath, real))
			must(t, os.Symlink(real, e.keyPath))
		}, true},
		{"two links", func(t *testing.T, e *env) { must(t, os.Link(e.keyPath, e.keyPath+".alias")) }, true},
		{"31 bytes", func(t *testing.T, e *env) { must(t, os.WriteFile(e.keyPath, e.seed[:31], 0600)) }, true},
		{"33 bytes", func(t *testing.T, e *env) {
			must(t, os.WriteFile(e.keyPath, append(append([]byte(nil), e.seed...), 0), 0600))
		}, true},
		{"missing", func(t *testing.T, e *env) { must(t, os.Remove(e.keyPath)) }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			tc.prepare(t, e)
			code, stdout, stderr := e.dispatch(randomID(t))
			connections := len(l.drain())
			if !tc.unsafe {
				if code != ExitAccepted || connections != 1 || e.storedCount() != 1 {
					t.Fatalf("exit %d stderr %q connections %d", code, stderr, connections)
				}
				return
			}
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonKeyFileUnsafe) || connections != 0 || e.storedCount() != 0 {
				t.Fatalf("exit %d stdout %q stderr %q connections %d stored %d", code, stdout, stderr, connections, e.storedCount())
			}
		})
	}
}

// Claim: a 0600 single-link 32-byte key file owned by another UID refuses
// key_file_unsafe before any signature. Control beside it: the same file owned
// by the effective UID signs.
func TestKeyFileForeignOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("UNRUN: a key file owned by another UID needs chown, which needs root; this run has one unprivileged UID")
	}
	dir := t.TempDir()
	seed := make([]byte, ed25519.SeedSize)
	_, err := rand.Read(seed)
	must(t, err)
	name := filepath.Join(dir, "seed")
	must(t, os.WriteFile(name, seed, 0600))
	key, err := m0.KeySpelling(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	must(t, err)
	c := &Config{KeyFile: name, Association: m0.Association{Key: key}}
	if _, reason, err := signPreimage(c, []byte("preimage")); reason != "" || err != nil {
		t.Fatalf("control: %s %v", reason, err)
	}
	must(t, os.Chown(name, 65534, -1))
	if _, reason, _ := signPreimage(c, []byte("preimage")); reason != reasonKeyFileUnsafe {
		t.Fatalf("foreign owner: reason %q", reason)
	}
}

// Claim: with the configured service_uid set to another value the tool
// refuses with exit 64 and the listener receives nothing; and when only the
// kernel peer check can refuse (endpoint pins satisfied, peer UID different)
// the client connects, sends zero bytes and closes before hello. Control
// beside them: the listener owned by the expected UID is served (exit 0).
func TestServiceUIDRequiredBeforeHello(t *testing.T) {
	e := newEnv(t)
	l := e.listen(modeAccept)
	if code, _, stderr := e.dispatch(randomID(t)); code != ExitAccepted {
		t.Fatalf("control: exit %d stderr %q", code, stderr)
	}
	if streams := l.drain(); len(streams) != 1 || len(streams[0]) == 0 {
		t.Fatalf("control: streams %d", len(streams))
	}
	c := e.config()
	id := randomID(t)
	spec := specOf(c)
	spec.peer = c.ServiceUID + 1
	reply, sent, stage, err := exchange(spec, id, []byte(`{"kind":"fixture"}`))
	if err != errPeer || stage != stagePeer || sent != 0 || reply != nil {
		t.Fatalf("peer check: reply %q sent %d stage %q err %v", reply, sent, stage, err)
	}
	if o := classify(id, false, sent, reply, stage, err); o.Exit != ExitLocal {
		t.Fatalf("peer check: exit %d", o.Exit)
	}
	streams := l.drain()
	if len(streams) != 2 || len(streams[1]) != 0 {
		t.Fatalf("peer check: the listener received bytes: %d streams, last %q", len(streams), streams[len(streams)-1])
	}
	other := os.Geteuid() + 1
	if other >= 30000 && other <= 59999 {
		other = 60000
	}
	e.doc["service_uid"] = other
	e.writeConfig()
	code, stdout, stderr := e.dispatch(randomID(t))
	if code != ExitLocal || stdout != "" || !strings.Contains(stderr, stagePins) {
		t.Fatalf("other service_uid: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if streams := l.drain(); len(streams) != 2 {
		t.Fatalf("other service_uid: the listener saw a connection")
	}
}

// Claim: a listener that reads the request and closes without a reply gives
// exit 17 with the request_id on stderr and nothing on stdout; the .req file
// exists; resend then sends byte-identical data on the wire. The same exit 17
// follows a truncated reply, a reply with another correlation ID, a reply
// that never arrives within the stage timeout and a reply carrying a passed
// descriptor. Control beside them: the same listener answering a plain valid
// reply gives exit 0.
func TestUnknownOutcome(t *testing.T) {
	for _, mode := range []string{modeAccept, modeClose, modeTruncate, modeWrongID, modeSilent, modeRights} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			if mode == modeSilent {
				e.doc["stage_timeout_ms"] = 200
				e.writeConfig()
			}
			l := e.listen(mode)
			id := randomID(t)
			code, stdout, stderr := e.dispatch(id)
			if mode == modeAccept {
				if code != ExitAccepted || stdout != acceptedEnvelope(id) || stderr != "" {
					t.Fatalf("control: exit %d stdout %q stderr %q", code, stdout, stderr)
				}
				return
			}
			if code != ExitUnknown || stdout != "" || !strings.Contains(stderr, "request_id="+id+"\n") {
				t.Fatalf("exit %d stdout %q stderr %q", code, stdout, stderr)
			}
			stored, err := os.ReadFile(e.storedPath(id))
			must(t, err)
			if code, _, stderr := e.resend(id); code != ExitUnknown || !strings.Contains(stderr, "request_id="+id+"\n") {
				t.Fatalf("resend: exit %d stderr %q", code, stderr)
			}
			streams := l.drain()
			if len(streams) != 2 || len(streams[0]) == 0 || !bytes.Equal(streams[0], streams[1]) {
				t.Fatalf("resend did not send byte-identical data: %d streams", len(streams))
			}
			if !bytes.Equal(requestFrame(t, streams[1]), stored) {
				t.Fatal("resend sent bytes that differ from the stored request")
			}
			if after, err := os.ReadFile(e.storedPath(id)); err != nil || !bytes.Equal(after, stored) {
				t.Fatal("resend changed the stored request")
			}
		})
	}
}

// Claim: resend refuses with exit 64 and sends nothing when the stored file
// is missing, is a symlink, has a second link, has mode 0640, is truncated,
// has one signature character changed, has a byte appended or carries
// another request's bytes under this name. Control beside them: the untouched
// stored file is sent (exit 0).
func TestResendStoredFileShape(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, e *env, path string)
		refused bool
	}{
		{"control", func(*testing.T, *env, string) {}, false},
		{"missing", func(t *testing.T, _ *env, path string) { must(t, os.Remove(path)) }, true},
		{"symlink", func(t *testing.T, e *env, path string) {
			real := filepath.Join(e.root, "moved.req")
			must(t, os.Rename(path, real))
			must(t, os.Symlink(real, path))
		}, true},
		{"two links", func(t *testing.T, e *env, path string) { must(t, os.Link(path, filepath.Join(e.root, "alias.req"))) }, true},
		{"mode 0640", func(t *testing.T, _ *env, path string) { must(t, os.Chmod(path, 0640)) }, true},
		{"other request", func(t *testing.T, e *env, path string) {
			wire, reason, err := SignRequest(e.config(), m0.OpDispatch, randomID(t), "")
			if reason != "" || err != nil {
				t.Fatal(reason, err)
			}
			must(t, os.WriteFile(path, wire, 0600))
		}, true},
		{"truncated", func(t *testing.T, _ *env, path string) { must(t, os.Truncate(path, 100)) }, true},
		{"one signature character", func(t *testing.T, _ *env, path string) {
			wire, err := os.ReadFile(path)
			must(t, err)
			at := bytes.Index(wire, []byte(`"sig":"`)) + len(`"sig":"`)
			if wire[at] == 'A' {
				wire[at] = 'B'
			} else {
				wire[at] = 'A'
			}
			must(t, os.WriteFile(path, wire, 0600))
		}, true},
		{"added whitespace", func(t *testing.T, _ *env, path string) {
			wire, err := os.ReadFile(path)
			must(t, err)
			must(t, os.WriteFile(path, append(wire, '\n'), 0600))
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			id := randomID(t)
			if code, _, stderr := e.dispatch(id); code != ExitAccepted {
				t.Fatalf("setup: exit %d stderr %q", code, stderr)
			}
			tc.prepare(t, e, e.storedPath(id))
			code, stdout, stderr := e.resend(id)
			connections := len(l.drain())
			if !tc.refused {
				if code != ExitAccepted || connections != 2 {
					t.Fatalf("exit %d stderr %q connections %d", code, stderr, connections)
				}
				return
			}
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonStoredRequest) || connections != 1 {
				t.Fatalf("exit %d stdout %q stderr %q connections %d", code, stdout, stderr, connections)
			}
		})
	}
}

// Claim: the configuration object is closed: an unknown member at the root or
// in a nested object, a missing member, a duplicate member, an array, a
// timeout outside the transport bounds, a managed or zero service UID, a
// relative path, an endpoint name with a separator and any request spelling
// the daemon's decoder refuses are all refused. Control beside them: the
// untouched document parses.
func TestConfigClosedForm(t *testing.T) {
	e := newEnv(t)
	valid, err := json.Marshal(e.doc)
	must(t, err)
	if _, err := ParseConfig(valid); err != nil {
		t.Fatalf("control: %v", err)
	}
	edit := func(fn func(doc map[string]any)) []byte {
		var doc map[string]any
		d := json.NewDecoder(bytes.NewReader(valid))
		d.UseNumber()
		must(t, d.Decode(&doc))
		fn(doc)
		b, err := json.Marshal(doc)
		must(t, err)
		return b
	}
	nested := func(object, member string, value any) func(map[string]any) {
		return func(doc map[string]any) { doc[object].(map[string]any)[member] = value }
	}
	cases := map[string][]byte{
		"unknown root member":     edit(func(doc map[string]any) { doc["extra"] = 1 }),
		"unknown nested member":   edit(nested("association", "extra", 1)),
		"unknown plan member":     edit(nested("plan", "command", "x")),
		"missing member":          edit(func(doc map[string]any) { delete(doc, "state_dir") }),
		"missing nested member":   edit(func(doc map[string]any) { delete(doc["worker"].(map[string]any), "generation") }),
		"duplicate member":        bytes.Replace(valid, []byte(`{`), []byte(`{"v":1,`), 1),
		"array":                   edit(func(doc map[string]any) { doc["endpoint"] = []string{"d.sock"} }),
		"null":                    edit(func(doc map[string]any) { doc["key_file"] = nil }),
		"trailing bytes":          append(append([]byte(nil), valid...), []byte(`{}`)...),
		"wrong kind":              edit(func(doc map[string]any) { doc["kind"] = "swarma-dispatcher m0-config/1" }),
		"version 2":               edit(func(doc map[string]any) { doc["v"] = 2 }),
		"stage timeout 0":         edit(func(doc map[string]any) { doc["stage_timeout_ms"] = 0 }),
		"stage timeout 2001":      edit(func(doc map[string]any) { doc["stage_timeout_ms"] = 2001 }),
		"session timeout 5001":    edit(func(doc map[string]any) { doc["session_timeout_ms"] = 5001 }),
		"fractional timeout":      bytes.Replace(valid, []byte(`"stage_timeout_ms":2000`), []byte(`"stage_timeout_ms":2e3`), 1),
		"service uid 0":           edit(func(doc map[string]any) { doc["service_uid"] = 0 }),
		"managed service uid":     edit(func(doc map[string]any) { doc["service_uid"] = 30000 }),
		"relative state dir":      edit(func(doc map[string]any) { doc["state_dir"] = "state" }),
		"unclean endpoint dir":    edit(func(doc map[string]any) { doc["endpoint_dir"] = e.epDir + "/../ep" }),
		"endpoint with separator": edit(func(doc map[string]any) { doc["endpoint"] = "a/d.sock" }),
		"endpoint dot dot":        edit(func(doc map[string]any) { doc["endpoint"] = ".." }),
		"association uid 29999":   edit(nested("association", "uid", 29999)),
		"uppercase receiver":      edit(func(doc map[string]any) { doc["receiver"] = strings.Repeat("E", 32) }),
		"padded key":              edit(nested("association", "key", e.key+"=")),
		"digest without prefix":   edit(nested("plan", "profile", strings.Repeat("2", 64))),
		"revision 0":              edit(nested("policy", "revision", 0)),
		"empty":                   nil,
		"oversize":                append(append([]byte(nil), valid...), bytes.Repeat([]byte(" "), MaxConfigBytes)...),
	}
	if !bytes.Contains(valid, []byte(`"stage_timeout_ms":2000`)) {
		t.Fatal("fixture spelling changed")
	}
	for name, doc := range cases {
		if _, err := ParseConfig(doc); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Claim: a configuration file that is a symlink, is group-writable, is
// other-writable, is not a regular file or is named by a relative path
// refuses with exit 64 before the key file is read or anything is sent or
// stored. Control beside them: the same content in a regular 0600 file is
// used (exit 0).
func TestConfigFileShape(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, e *env) string
		refused bool
	}{
		{"control", func(_ *testing.T, e *env) string { return e.cfgPath }, false},
		{"read-only control", func(t *testing.T, e *env) string { must(t, os.Chmod(e.cfgPath, 0444)); return e.cfgPath }, false},
		{"symlink", func(t *testing.T, e *env) string {
			link := filepath.Join(e.root, "link.json")
			must(t, os.Symlink(e.cfgPath, link))
			return link
		}, true},
		{"group-writable", func(t *testing.T, e *env) string { must(t, os.Chmod(e.cfgPath, 0660)); return e.cfgPath }, true},
		{"other-writable", func(t *testing.T, e *env) string { must(t, os.Chmod(e.cfgPath, 0602)); return e.cfgPath }, true},
		{"directory", func(_ *testing.T, e *env) string { return e.root }, true},
		{"relative path", func(_ *testing.T, e *env) string { return "caller.json" }, true},
		{"missing", func(_ *testing.T, e *env) string { return filepath.Join(e.root, "absent.json") }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			path := tc.prepare(t, e)
			// An unsafe key file would refuse with another word: the
			// configuration refusal must come first.
			if tc.refused {
				must(t, os.Chmod(e.keyPath, 0644))
			}
			code, stdout, stderr := e.run(mustRaw(t, randomID(t)), "dispatch", "--config", path)
			connections := len(l.drain())
			if !tc.refused {
				if code != ExitAccepted || connections != 1 {
					t.Fatalf("exit %d stderr %q", code, stderr)
				}
				return
			}
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonConfig) || connections != 0 || e.storedCount() != 0 {
				t.Fatalf("exit %d stdout %q stderr %q connections %d", code, stdout, stderr, connections)
			}
		})
	}
}

// Claim: a state directory with mode 0755 or 0750, one that is a symlink and
// one that does not exist refuse with exit 64 before the key file is read;
// nothing is sent, created or repaired. Control beside them: mode 0700 is
// used and receives exactly one 0600 file named by the request ID.
func TestStateDirectoryShape(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, e *env)
		refused bool
	}{
		{"control", func(*testing.T, *env) {}, false},
		{"mode 0755", func(t *testing.T, e *env) { must(t, os.Chmod(e.stateDir, 0755)) }, true},
		{"mode 0750", func(t *testing.T, e *env) { must(t, os.Chmod(e.stateDir, 0750)) }, true},
		{"symlink", func(t *testing.T, e *env) {
			real := e.stateDir + ".real"
			must(t, os.Rename(e.stateDir, real))
			must(t, os.Symlink(real, e.stateDir))
		}, true},
		{"missing", func(t *testing.T, e *env) { must(t, os.Remove(e.stateDir)) }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			tc.prepare(t, e)
			id := randomID(t)
			code, stdout, stderr := e.dispatch(id)
			connections := len(l.drain())
			if !tc.refused {
				entries, err := os.ReadDir(e.stateDir)
				must(t, err)
				if code != ExitAccepted || connections != 1 || len(entries) != 1 || entries[0].Name() != id+".req" {
					t.Fatalf("exit %d stderr %q entries %v", code, stderr, entries)
				}
				return
			}
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonState) || connections != 0 {
				t.Fatalf("exit %d stdout %q stderr %q connections %d", code, stdout, stderr, connections)
			}
			if info, err := os.Lstat(e.stateDir); err == nil && info.IsDir() && info.Mode().Perm() == 0700 {
				t.Fatal("state directory was repaired")
			}
		})
	}
}

// Claim: before connecting the client requires the pinned endpoint: a socket
// with mode 0666 or 0600, a regular file in its place, an endpoint directory
// with mode 0755 or 0700, a missing socket and a connect_gid that is not a
// group of the process all refuse with exit 64 and the listener receives
// nothing; the request stays stored and the refusal says so. Control beside
// them: directory 0711 and socket 0660 are used (exit 0).
func TestEndpointPins(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, e *env, l *listener)
		refused bool
	}{
		{"control", func(*testing.T, *env, *listener) {}, false},
		{"socket 0666", func(t *testing.T, _ *env, l *listener) { must(t, os.Chmod(l.path, 0666)) }, true},
		{"socket 0600", func(t *testing.T, _ *env, l *listener) { must(t, os.Chmod(l.path, 0600)) }, true},
		{"directory 0755", func(t *testing.T, e *env, _ *listener) { must(t, os.Chmod(e.epDir, 0755)) }, true},
		{"directory 0700", func(t *testing.T, e *env, _ *listener) { must(t, os.Chmod(e.epDir, 0700)) }, true},
		{"regular file", func(t *testing.T, e *env, _ *listener) {
			e.doc["endpoint"] = "plain"
			e.writeConfig()
			must(t, os.WriteFile(filepath.Join(e.epDir, "plain"), nil, 0660))
			must(t, os.Chmod(filepath.Join(e.epDir, "plain"), 0660))
		}, true},
		{"missing socket", func(_ *testing.T, e *env, _ *listener) {
			e.doc["endpoint"] = "absent.sock"
			e.writeConfig()
		}, true},
		{"symlinked directory", func(t *testing.T, e *env, _ *listener) {
			link := filepath.Join(e.root, "eplink")
			must(t, os.Symlink(e.epDir, link))
			e.doc["endpoint_dir"] = link
			e.writeConfig()
		}, true},
		{"foreign connect gid", func(_ *testing.T, e *env, _ *listener) {
			e.doc["connect_gid"] = 4294967294
			e.writeConfig()
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			tc.prepare(t, e, l)
			id := randomID(t)
			code, stdout, stderr := e.dispatch(id)
			_ = os.Chmod(e.epDir, 0711)
			connections := len(l.drain())
			if !tc.refused {
				if code != ExitAccepted || connections != 1 {
					t.Fatalf("exit %d stderr %q", code, stderr)
				}
				return
			}
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, stagePins) || !strings.Contains(stderr, "request_id="+id+" stored, not sent") || connections != 0 {
				t.Fatalf("exit %d stdout %q stderr %q connections %d", code, stdout, stderr, connections)
			}
		})
	}
}

// Claim: the secrecy scanner finds a planted seed in every form it knows:
// raw, lowercase and uppercase hex, the four base64 alphabets and either half
// raw or in hex. Control beside it: the public key spelling, a request signed
// with the seed and unrelated bytes are clean.
func TestSeedScannerFindsPlantedSeed(t *testing.T) {
	e := newEnv(t)
	for name, form := range seedForms(e.seed) {
		planted := append(append([]byte("stderr: prefix "), form...), " suffix\n"...)
		if found := findSeed(e.seed, []byte("clean"), planted); len(found) == 0 {
			t.Errorf("%s: planted seed not found", name)
		}
	}
	wire, reason, err := SignRequest(e.config(), m0.OpDispatch, randomID(t), "")
	if reason != "" || err != nil {
		t.Fatal(reason, err)
	}
	if found := findSeed(e.seed, []byte(e.key), wire, []byte("unrelated bytes")); len(found) != 0 {
		t.Fatalf("control: clean data reported: %v", found)
	}
}

// Claim: a receive that carries a passed descriptor refuses with the
// ancillary error and closes the delivered descriptor (the process's open
// descriptor count is unchanged afterwards). Control beside it: plain data on
// the same kind of socket is received.
func TestReceiveRefusesAndClosesPassedDescriptor(t *testing.T) {
	openDescriptors := func() int {
		entries, err := os.ReadDir("/proc/self/fd")
		must(t, err)
		return len(entries)
	}
	for _, passed := range []bool{false, true} {
		pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0)
		must(t, err)
		f := os.NewFile(uintptr(pair[0]), "receiver")
		raw, err := f.SyscallConn()
		must(t, err)
		w := &wire{file: f, raw: raw}
		var rights []byte
		if passed {
			rights = syscall.UnixRights(pair[1])
		}
		must(t, syscall.Sendmsg(pair[1], []byte("data"), rights, nil, 0))
		before := openDescriptors()
		buf := make([]byte, 4)
		n, err := w.receive(buf)
		after := openDescriptors()
		if passed && (err != errAncillary || n != 0 || after != before) {
			t.Fatalf("passed descriptor: n=%d err=%v descriptors %d -> %d", n, err, before, after)
		}
		if !passed && (err != nil || string(buf[:n]) != "data" || after != before) {
			t.Fatalf("control: n=%d err=%v descriptors %d -> %d", n, err, before, after)
		}
		must(t, f.Close())
		must(t, syscall.Close(pair[1]))
	}
}

// Claim: every stage deadline is the earlier of stage start plus the stage
// timeout and session start plus the session timeout: a late stage is cut by
// the session budget. Control beside it: an early stage gets the full stage
// timeout.
func TestStageDeadlineNeverPassesSessionBudget(t *testing.T) {
	early := &wire{start: time.Now(), spec: endpointSpec{stage: 2 * time.Second, session: 5 * time.Second}}
	if d := time.Until(early.deadline()); d < 1900*time.Millisecond || d > 2*time.Second {
		t.Fatalf("control: early stage deadline in %v", d)
	}
	late := &wire{start: time.Now().Add(-4 * time.Second), spec: endpointSpec{stage: 2 * time.Second, session: 5 * time.Second}}
	if d := time.Until(late.deadline()); d > time.Second || d < 900*time.Millisecond {
		t.Fatalf("late stage deadline in %v, want the session remainder", d)
	}
}

// Claim: connect_gid must be the effective or a supplementary group of the
// process: a group the process is not in is refused. Control beside it: the
// effective group is accepted.
func TestConnectGroupMembership(t *testing.T) {
	if !groupMember(uint32(os.Getegid())) {
		t.Fatal("control: effective group refused")
	}
	if os.Getegid() != 4294967294 && groupMember(4294967294) {
		t.Fatal("foreign group accepted")
	}
}

// Claim: the endpoint is re-checked after connect and before any write: a
// socket whose mode changes, or an endpoint directory whose mode changes,
// between connect and the re-stat refuses with zero bytes sent, and the
// listener receives nothing on that connection. Control beside them: with
// nothing changed in that window the exchange completes.
func TestEndpointRecheckedAfterConnect(t *testing.T) {
	for _, change := range []string{"none", "socket mode", "directory mode"} {
		t.Run(change, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			id := randomID(t)
			spec := specOf(e.config())
			spec.connected = func() {
				switch change {
				case "socket mode":
					must(t, os.Chmod(l.path, 0666))
				case "directory mode":
					must(t, os.Chmod(e.epDir, 0755))
				}
			}
			reply, sent, stage, err := exchange(spec, id, []byte(`{"kind":"fixture"}`))
			must(t, os.Chmod(e.epDir, 0711))
			streams := l.drain()
			if change == "none" {
				if err != nil || string(reply) != acceptedEnvelope(id) || len(streams) != 1 || len(streams[0]) == 0 {
					t.Fatalf("control: reply %q err %v", reply, err)
				}
				return
			}
			if err != errIdentity || stage != stagePins || sent != 0 || reply != nil || len(streams) != 1 || len(streams[0]) != 0 {
				t.Fatalf("reply %q sent %d stage %q err %v streams %d", reply, sent, stage, err, len(streams))
			}
		})
	}
}

// Claim: the request is written only after the exact hello result: when the
// listener answers hello with a well-formed refusal envelope the client
// sends no request frame and the outcome is unknown (exit 17, hello bytes
// were sent). Control beside it: after the exact hello result the request
// frame is sent and answered (exit 0).
func TestRequestOnlyAfterExactHelloResult(t *testing.T) {
	for _, mode := range []string{modeAccept, modeBadHello} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(mode)
			id := randomID(t)
			code, stdout, stderr := e.dispatch(id)
			streams := l.drain()
			hello := frameOf(helloFrame(id))
			if len(streams) != 1 || !bytes.HasPrefix(streams[0], hello) {
				t.Fatalf("streams %d", len(streams))
			}
			if mode == modeAccept {
				if code != ExitAccepted || len(streams[0]) == len(hello) {
					t.Fatalf("control: exit %d stderr %q", code, stderr)
				}
				return
			}
			if code != ExitUnknown || stdout != "" || !strings.Contains(stderr, "request_id="+id+"\n") || !bytes.Equal(streams[0], hello) {
				t.Fatalf("exit %d stdout %q stderr %q, listener received %d bytes beyond hello", code, stdout, stderr, len(streams[0])-len(hello))
			}
		})
	}
}

// Claim: the stored request is created exclusively: when a file already has
// the drawn request ID's name the tool refuses with exit 64, leaves that file
// unchanged and sends nothing. Control beside it: a fresh ID is stored and
// sent (exit 0).
func TestStoreNeverReplacesExistingRequest(t *testing.T) {
	e := newEnv(t)
	l := e.listen(modeAccept)
	id := randomID(t)
	must(t, os.WriteFile(e.storedPath(id), []byte("earlier"), 0600))
	code, stdout, stderr := e.dispatch(id)
	kept, err := os.ReadFile(e.storedPath(id))
	must(t, err)
	if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonStore) || string(kept) != "earlier" || len(l.drain()) != 0 {
		t.Fatalf("exit %d stdout %q stderr %q kept %q", code, stdout, stderr, kept)
	}
	if code, _, stderr := e.dispatch(randomID(t)); code != ExitAccepted || len(l.drain()) != 1 {
		t.Fatalf("control: exit %d stderr %q", code, stderr)
	}
}
