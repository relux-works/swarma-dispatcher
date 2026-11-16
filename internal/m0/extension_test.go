package m0

import (
	"errors"
	"strings"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

// Claim: the extension answers only M0-kind frames. A non-M0 body closes with
// transport.ErrInvalid; an M0 body whose request_id differs from the
// connection ID gets a request_invalid envelope and is never handled; a pinned
// dispatch yields the exact success envelope; a refusal yields the exact error
// envelope carrying the refusal code; every envelope fits the 508-byte
// outbound bound; a sync failure returns transport.ErrFatal.
func TestExtensionServe(t *testing.T) {
	e := newEnv(t)
	ext := NewExtension(e.rt)
	for _, body := range []string{`{"op":"capacity","request_id":"a"}`, "not json", `{"kind":"other","request_id":"a"}`} {
		if _, err := ext.Serve(e.peer, dispatchID, []byte(body)); !errors.Is(err, transport.ErrInvalid) {
			t.Fatalf("EXT: non-M0 body answered: %v", err)
		}
	}
	got, err := ext.Serve(e.peer, "other", e.dispatchWire)
	must(t, err)
	if got != `{"error":{"code":"request_invalid","message":"request_id differs from the connection correlation ID"},"ok":false,"request_id":"other","v":1}` {
		t.Fatalf("EXT: correlation mismatch envelope %s", got)
	}
	if starts, _ := e.launcher.count(); starts != 0 {
		t.Fatal("EXT: mismatched correlation was handled")
	}
	got, err = ext.Serve(e.peer, dispatchID, e.dispatchWire)
	must(t, err)
	rec := e.record(t, dispatchID)
	if got != `{"ok":true,"request_id":"`+dispatchID+`","result":{"effect_id":"`+rec.EffectID+`","state":"started"},"v":1}` {
		t.Fatalf("EXT: success envelope %s", got)
	}
	other := newKey(t)
	foreign := parseDoc(t, e.cancelDoc.encode(nil))
	setPath(t, foreign, "association.key", text(other.spelling))
	got, err = ext.Serve(e.peer, cancelID, signRequest(t, foreign, other))
	must(t, err)
	if got != `{"error":{"code":"run_not_owned","message":"cancel is not from the original owner"},"ok":false,"request_id":"`+cancelID+`","v":1}` {
		t.Fatalf("EXT: refusal envelope %s", got)
	}
	if len(got) > 508 || len(refusalEnvelope(strings.Repeat("a", 64), CodeStorageFailed, strings.Repeat("x", 1000))) > 508 {
		t.Fatal("EXT: envelope exceeds the outbound buffer")
	}
	if sanitize("a\"b\\c\nd") != "a_b_c_d" {
		t.Fatal("EXT: sanitize kept a forbidden byte")
	}
	e2 := newEnv(t)
	e2.store.failAppendAt = e2.store.appendCalls + 2
	e2.store.failErr = syncFailure()
	_, err = NewExtension(e2.rt).Serve(e2.peer, dispatchID, e2.dispatchWire)
	if !errors.Is(err, transport.ErrFatal) || !MustExit(err) {
		t.Fatalf("EXT: sync failure not fatal: %v", err)
	}
}

// Claim: ParseConfig accepts exactly the canonical closed object and refuses
// non-canonical bytes, an unknown or missing member, an unknown entry, a
// relative or unclean state directory and a wrong kind. Attach refuses an
// unreadable file, returns m0_closed for enabled=false and for default seams
// (naming the policy source) and for a removed installed policy, without
// touching any ledger.
func TestConfigAndAttachGate(t *testing.T) {
	canon := `{"enabled":true,"entry":"initialize","kind":"swarma-dispatcher m0-config/1","recovery_attested":false,"state_dir":"/var/lib/example","supported_local_storage":true,"v":1}`
	cfg, err := ParseConfig([]byte(canon))
	must(t, err)
	if !cfg.Enabled || cfg.Entry != EntryInitialize || cfg.RecoveryAttested || cfg.StateDir != "/var/lib/example" || !cfg.SupportedLocalStorage {
		t.Fatalf("CONFIG: %+v", cfg)
	}
	for name, bad := range map[string]string{
		"whitespace":     " " + canon,
		"unsorted":       strings.Replace(strings.Replace(canon, `"enabled":true,`, "", 1), `"v":1`, `"v":1,"enabled":true`, 1),
		"unknown-member": strings.Replace(canon, `"v":1`, `"v":1,"x":1`, 1),
		"missing-member": strings.Replace(canon, `"recovery_attested":false,`, "", 1),
		"entry":          strings.Replace(canon, `"entry":"initialize"`, `"entry":"repair"`, 1),
		"relative-dir":   strings.Replace(canon, `"/var/lib/example"`, `"var/lib/example"`, 1),
		"dotdot-dir":     strings.Replace(canon, `"/var/lib/example"`, `"/var/../example"`, 1),
		"kind":           strings.Replace(canon, `m0-config/1`, `m0-config/2`, 1),
		"enabled-string": strings.Replace(canon, `"enabled":true`, `"enabled":"true"`, 1),
	} {
		if _, err := ParseConfig([]byte(bad)); CodeOf(err) != CodeRequestInvalid {
			t.Errorf("CONFIG: %s accepted: %v", name, err)
		}
	}
	readOf := func(doc string) func(string) ([]byte, error) {
		return func(string) ([]byte, error) { return []byte(doc), nil }
	}
	if _, err := Attach("/m0.json", DefaultSeams(), func(string) ([]byte, error) { return nil, errFixture }); CodeOf(err) != CodeRequestInvalid {
		t.Fatalf("ATTACH: unreadable config %v", err)
	}
	disabled := strings.Replace(canon, `"enabled":true`, `"enabled":false`, 1)
	if _, err := Attach("/m0.json", DefaultSeams(), readOf(disabled)); CodeOf(err) != CodeClosed {
		t.Fatalf("ATTACH: disabled %v", err)
	}
	ext, err := Attach("/m0.json", DefaultSeams(), readOf(canon))
	if ext != nil || CodeOf(err) != CodeClosed || !strings.Contains(err.Error(), string(SeamPolicySource)) {
		t.Fatalf("ATTACH: default seams %v", err)
	}
	e := newEnv(t)
	e.source.edit(func(i *Installed) { i.Removed = true; i.InstallRevision = 2 })
	if _, err := Attach("/m0.json", e.seams(), readOf(canon)); CodeOf(err) != CodeClosed {
		t.Fatalf("ATTACH: removed policy %v", err)
	}
}
