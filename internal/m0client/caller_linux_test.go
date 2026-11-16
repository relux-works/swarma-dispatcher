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

// Claim: Both installed operation plans round-trip with identical canonical R, valid signatures and exact preimage bounds; altered signatures fail.
func TestRoundTripThroughDaemonDecoder(t *testing.T) {
	for _, serials := range []string{"fixture", "maximal"} {
		t.Run(serials, func(t *testing.T) {
			e := newEnv(t)
			if serials == "maximal" {
				e.policy.Revision = maxSerialValue
				e.policy.Association.Revision = maxSerialValue
				e.policy.Association.RegistrationNumber = maxSerialValue
				e.policy.Association.MasterEpoch = maxSerialValue
			}
			c := e.config()
			target := randomID(t)
			for _, op := range []m0.Op{m0.OpDispatch, m0.OpCancel} {
				id, bound, want := randomID(t), m0.MaxDispatchPreimage, ""
				if op == m0.OpCancel {
					bound, want = m0.MaxCancelPreimage, target
				}
				plan := e.plan(op, id, want)
				wire, reason, err := SignRequest(c, plan)
				if reason != "" || err != nil {
					t.Fatalf("%s: sign refused: %s %v", op, reason, err)
				}
				e.keep(wire)
				r, err := m0.DecodeRequest(wire)
				must(t, err)
				if err := r.Verify(); err != nil {
					t.Fatalf("%s: daemon verification: %v", op, err)
				}
				if r.Op != op || r.RequestID != id || r.DispatchRequestID != want || !bytes.Equal(r.Canonical, plan.Request.Canonical) || r.Auth.Key != plan.Request.Association.Key {
					t.Fatalf("%s: decoded members differ from the installed plan: %+v", op, r)
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

// Claim: The installed dispatch R launches once through the real runtime/extension, stores the exact wire bytes, and its installed cancel stops it.
func TestRuntimeDispatchAndCancel(t *testing.T) {
	e := newEnv(t)
	dispatchID, cancelID := randomID(t), randomID(t)
	e.installPlans(dispatchID, cancelID)
	b := e.newBackend(dispatchID, cancelID)
	l := e.serveBackend(b)
	code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
	if code != ExitAccepted || !strings.HasPrefix(stdout, `{"ok":true,"request_id":"`+dispatchID+`"`) || !strings.Contains(stdout, `"state":"started"`) || stderr != "" {
		t.Fatalf("dispatch: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if starts, _ := b.launcher.count(); starts != 1 {
		t.Fatalf("dispatch: starts=%d", starts)
	}
	streams := l.drain()
	stored, err := os.ReadFile(e.storedPath(dispatchID))
	must(t, err)
	decoded, err := m0.DecodeRequest(stored)
	must(t, err)
	if !bytes.Equal(decoded.Canonical, b.source.inst.Catalog.Dispatch.Request.Canonical) {
		t.Fatal("dispatch R differs from installed signing plan")
	}
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

// Claim: A foreign seed refuses locally without storing or connecting; the associated seed sends successfully.
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
func signedBy(t *testing.T, plan *m0.SigningPlan, seed []byte) []byte {
	t.Helper()
	r := plan.Request
	sig, err := m0.SignatureSpelling(ed25519.Sign(ed25519.NewKeyFromSeed(seed), r.Preimage))
	must(t, err)
	wire, err := m0.SignedWire(r.Canonical, r.Association.Key, sig)
	must(t, err)
	return wire
}

// Claim: A foreign signature refuses signature_invalid without launch; the same R signed by the associated seed launches.
func TestForeignSignatureRefusedByDaemon(t *testing.T) {
	e := newEnv(t)
	id := randomID(t)
	b := e.newBackend(id, randomID(t))
	e.serveBackend(b)
	c := e.config()
	other := make([]byte, ed25519.SeedSize)
	_, err := rand.Read(other)
	must(t, err)
	o := Send(c, id, signedBy(t, e.plan(m0.OpDispatch, id, ""), other), false)
	e.keep(o.Reply)
	if o.Exit != ExitRefused || !strings.Contains(string(o.Reply), `"code":"signature_invalid"`) {
		t.Fatalf("foreign signature: exit %d reply %q %v", o.Exit, o.Reply, o.Detail)
	}
	if starts, _ := b.launcher.count(); starts != 0 {
		t.Fatalf("foreign signature launched: starts=%d", starts)
	}
	o = Send(c, id, signedBy(t, e.plan(m0.OpDispatch, id, ""), e.seed), false)
	if o.Exit != ExitAccepted {
		t.Fatalf("control: exit %d reply %q %v", o.Exit, o.Reply, o.Detail)
	}
}

// Claim: Altered R refuses at the runtime or local resend gate; the unchanged signed request launches once.
func TestAlteredRequestRefused(t *testing.T) {
	e := newEnv(t)
	id := randomID(t)
	b := e.newBackend(id, randomID(t))
	l := e.serveBackend(b)
	c := e.config()
	wire, reason, err := e.sign(c, m0.OpDispatch, id, "")
	if reason != "" || err != nil {
		t.Fatal(reason, err)
	}
	flip := func(s string) string {
		if s[0] == '0' {
			return "1" + s[1:]
		}
		return "0" + s[1:]
	}
	receiver := bytes.Replace(wire, []byte(`"receiver":"`+e.policy.Receiver), []byte(`"receiver":"`+flip(e.policy.Receiver)), 1)
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
	e.installPlans(id, e.cancelID)
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

// Claim: A runtime peer other than association.uid refuses without launch; the associated peer starts the same signed request.
func TestPeerOtherThanAssociationUID(t *testing.T) {
	e := newEnv(t)
	id := randomID(t)
	b := e.newBackend(id, randomID(t))
	wire, reason, err := e.sign(e.config(), m0.OpDispatch, id, "")
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

// Claim: Exact resend works without the seed and never relaunches; a different installed plan cannot replenish the runtime launch budget.
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

// Claim: Unsafe key modes, symlinks, links, lengths and absence refuse without store/connect; a safe key signs and sends.
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

// Claim: foreign key ownership refuses before signing; the same modeled installation signs with the enrolled owner.
func TestKeyFileForeignOwner(t *testing.T) {
	e := newEnv(t)
	c := e.config()
	plan := e.plan(m0.OpDispatch, randomID(t), "")
	if _, reason, err := SignRequest(c, plan); reason != "" || err != nil {
		t.Fatal("control", reason, err)
	}
	native := c.host.stat
	c.host.stat = func(fd int) (syscall.Stat_t, error) {
		st, err := native(fd)
		if st.Mode&syscall.S_IFMT == syscall.S_IFREG {
			st.Uid++
		}
		return st, err
	}
	if _, reason, _ := SignRequest(c, plan); reason != reasonKeyFileUnsafe {
		t.Fatal("foreign owner accepted", reason)
	}
}

// Claim: A foreign service UID or kernel peer refuses with zero sent bytes; the expected service completes an exchange.
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

// Claim: Missing, truncated, late, ancillary or wrong-ID replies are unknown with exact durable resend; a plain matching reply succeeds.
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

// Claim: Unsafe or altered stored requests refuse without connecting; the untouched canonical signed request resends.
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
			wire, reason, err := e.sign(e.config(), m0.OpDispatch, randomID(t), "")
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

// Claim: only canonical, complete transport-pin/tool-path configurations parse; each malformed form refuses beside the valid control.
func TestConfigClosedForm(t *testing.T) {
	e := newEnv(t)
	valid, err := json.Marshal(e.doc)
	must(t, err)
	if _, err := ParseConfig(valid); err != nil {
		t.Fatal("control", err)
	}
	edit := func(fn func(map[string]any)) []byte {
		var doc map[string]any
		d := json.NewDecoder(bytes.NewReader(valid))
		d.UseNumber()
		must(t, d.Decode(&doc))
		fn(doc)
		b, err := json.Marshal(doc)
		must(t, err)
		return b
	}
	nested := func(k string, v any) func(map[string]any) {
		return func(d map[string]any) { d["caller"].(map[string]any)[k] = v }
	}
	cases := map[string][]byte{
		"unknown":             edit(func(d map[string]any) { d["extra"] = 1 }),
		"old receiver":        edit(func(d map[string]any) { d["receiver"] = e.policy.Receiver }),
		"endpoint override":   edit(func(d map[string]any) { d["endpoint_dir"] = e.epDir }),
		"unknown caller":      edit(nested("extra", 1)),
		"missing":             edit(func(d map[string]any) { delete(d, "dispatch_plan") }),
		"missing caller":      edit(func(d map[string]any) { delete(d["caller"].(map[string]any), "ino") }),
		"duplicate":           bytes.Replace(valid, []byte(`{`), []byte(`{"caller":{},`), 1),
		"unsorted":            bytes.Replace(valid, []byte(`{`), []byte(`{"z":1,`), 1),
		"array":               edit(func(d map[string]any) { d["caller"] = []int{1} }),
		"null":                edit(func(d map[string]any) { d["key_file"] = nil }),
		"trailing":            append(append([]byte(nil), valid...), ' '),
		"whitespace":          append([]byte("\n"), valid...),
		"escaped":             bytes.Replace(valid, []byte(`"caller"`), []byte(`"call\u0065r"`), 1),
		"unicode":             edit(func(d map[string]any) { d["key_file"] = "/é" }),
		"version":             edit(func(d map[string]any) { d["v"] = 2 }),
		"timeout zero":        edit(func(d map[string]any) { d["stage_timeout_ms"] = 0 }),
		"timeout large":       edit(func(d map[string]any) { d["stage_timeout_ms"] = 2001 }),
		"session large":       edit(func(d map[string]any) { d["session_timeout_ms"] = 5001 }),
		"exponent":            bytes.Replace(valid, []byte(`"stage_timeout_ms":2000`), []byte(`"stage_timeout_ms":2e3`), 1),
		"service zero":        edit(func(d map[string]any) { d["service_uid"] = 0 }),
		"managed service":     edit(func(d map[string]any) { d["service_uid"] = 30000 }),
		"same uid":            edit(func(d map[string]any) { d["service_uid"] = e.policy.Association.UID }),
		"same gid":            edit(func(d map[string]any) { d["service_gid"] = d["connect_gid"] }),
		"caller zero":         edit(nested("uid", 0)),
		"caller overflow":     edit(nested("uid", 4294967295)),
		"caller dev zero":     edit(nested("dev", 0)),
		"caller ino overflow": edit(nested("ino", uint64(maxSerial)+1)),
		"caller path long":    edit(nested("dir", "/"+strings.Repeat("a", 512))),
		"caller path root":    edit(nested("dir", "/")),
		"relative":            edit(func(d map[string]any) { d["state_dir"] = "state" }),
		"unclean plan":        edit(func(d map[string]any) { d["cancel_plan"] = "/a/../b" }),
		"endpoint zero":       edit(func(d map[string]any) { d["endpoint_dev"] = 0 }),
		"endpoint overflow":   edit(func(d map[string]any) { d["endpoint_ino"] = uint64(maxSerial) + 1 }),
		"empty":               nil, "oversize": bytes.Repeat([]byte("x"), MaxConfigBytes+1),
	}
	for name, doc := range cases {
		if _, err := ParseConfig(doc); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

// Claim: Unsafe configuration shape/path refuses before key access/store/connect; the protected snapshot succeeds.
func TestConfigFileShape(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(t *testing.T, e *env) string
		refused bool
	}{
		{"control", func(_ *testing.T, e *env) string { return e.cfgPath }, false},
		{"wrong mode", func(t *testing.T, e *env) string { must(t, os.Chmod(e.cfgPath, 0600)); return e.cfgPath }, true},
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
			code, stdout, stderr := e.run("dispatch", "--config", path)
			connections := 0
			if tc.name == "regular file" || tc.name == "missing socket" {
				l.mu.Lock()
				connections = l.accepted
				l.mu.Unlock()
			} else {
				connections = len(l.drain())
			}
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

// Claim: Unsafe state directories refuse without repair/connect; an owned 0700 directory stores one 0600 request.
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

// Claim: Unsafe endpoint type, path or modes refuse before connect with stored-not-sent evidence; valid pins exchange successfully.
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
			must(t, os.Remove(filepath.Join(e.epDir, endpointName)))
			must(t, os.WriteFile(filepath.Join(e.epDir, endpointName), nil, 0660))
			must(t, os.Chmod(filepath.Join(e.epDir, endpointName), 0660))
		}, true},
		{"missing socket", func(t *testing.T, e *env, _ *listener) { must(t, os.Remove(filepath.Join(e.epDir, endpointName))) }, true},
		{"symlinked directory", func(t *testing.T, e *env, _ *listener) {
			link := filepath.Join(e.root, "eplink")
			must(t, os.Symlink(e.epDir, link))
			e.host.endpoint = link
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
			connections := 0
			if tc.name == "regular file" || tc.name == "missing socket" {
				l.mu.Lock()
				connections = l.accepted
				l.mu.Unlock()
			} else {
				connections = len(l.drain())
			}
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

// Claim: All eleven planted seed forms are detected; public keys, signatures and unrelated bytes remain clean.
func TestSeedScannerFindsPlantedSeed(t *testing.T) {
	e := newEnv(t)
	for name, form := range seedForms(e.seed) {
		planted := append(append([]byte("stderr: prefix "), form...), " suffix\n"...)
		if found := findSeed(e.seed, []byte("clean"), planted); len(found) == 0 {
			t.Errorf("%s: planted seed not found", name)
		}
	}
	wire, reason, err := e.sign(e.config(), m0.OpDispatch, randomID(t), "")
	if reason != "" || err != nil {
		t.Fatal(reason, err)
	}
	if found := findSeed(e.seed, []byte(e.key), wire, []byte("unrelated bytes")); len(found) != 0 {
		t.Fatalf("control: clean data reported: %v", found)
	}
}

// Claim: Ancillary rights refuse and delivered descriptors close; ordinary socket data is received without leaks.
func TestReceiveRefusesAndClosesPassedDescriptor(t *testing.T) {
	_ = newEnv(t)
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

// Claim: Early stages receive the stage allowance; late stages cannot exceed the original session deadline.
func TestStageDeadlineNeverPassesSessionBudget(t *testing.T) {
	_ = newEnv(t)
	early := &wire{start: time.Now(), spec: endpointSpec{stage: 2 * time.Second, session: 5 * time.Second}}
	if d := time.Until(early.deadline()); d < 1900*time.Millisecond || d > 2*time.Second {
		t.Fatalf("control: early stage deadline in %v", d)
	}
	late := &wire{start: time.Now().Add(-4 * time.Second), spec: endpointSpec{stage: 2 * time.Second, session: 5 * time.Second}}
	if d := time.Until(late.deadline()); d > time.Second || d < 900*time.Millisecond {
		t.Fatalf("late stage deadline in %v, want the session remainder", d)
	}
}

// Claim: The effective group is recognized as membership; a foreign group is not.
func TestConnectGroupMembership(t *testing.T) {
	_ = newEnv(t)
	if !groupMember(uint32(os.Getegid())) {
		t.Fatal("control: effective group refused")
	}
	if os.Getegid() != 4294967294 && groupMember(4294967294) {
		t.Fatal("foreign group accepted")
	}
}

// Claim: Changed endpoint/caller identities or modes refuse with zero bytes before hello; unchanged pins exchange successfully.
func TestEndpointRecheckedAfterConnect(t *testing.T) {
	for _, change := range []string{"none", "socket mode", "directory mode", "caller mode", "caller replacement"} {
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
				case "caller mode":
					must(t, os.Chmod(e.callerDir, 0755))
				case "caller replacement":
					must(t, os.Rename(e.callerDir, e.callerDir+".old"))
					must(t, os.Mkdir(e.callerDir, 0700))
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

// Claim: A bad hello result sends no request and reports unknown; an exact hello result permits one answered request.
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

// Claim: An existing installed request name refuses without replacement/connect; a fresh installed plan stores and sends.
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
