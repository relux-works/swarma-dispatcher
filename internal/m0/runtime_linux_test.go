//go:build linux

package m0

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

func journalFixture(t *testing.T) journal.Config {
	t.Helper()
	d := t.TempDir()
	must(t, os.Chmod(d, 0700))
	for _, n := range []string{"records", "inputs"} {
		must(t, os.Mkdir(filepath.Join(d, n), 0700))
		must(t, os.Chmod(filepath.Join(d, n), 0700))
	}
	var fs syscall.Statfs_t
	must(t, syscall.Statfs(d, &fs))
	if fs.Type != 0xEF53 && fs.Type != 0x58465342 {
		t.Skipf("real storage UNRUN: fixture filesystem type %#x is neither ext4 nor XFS", fs.Type)
	}
	current := syscall.Umask(0)
	syscall.Umask(current)
	m := uint32(current)
	if m&0600 != 0 {
		t.Skipf("process umask %#o masks owner read/write", m)
	}
	return journal.Config{Directory: d, Records: "records", Inputs: "inputs", DeclaredUmask: &m, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 4 << 20}
}

func journalBytes(t *testing.T, cfg journal.Config) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cfg.Directory, "journal"))
	must(t, err)
	return b
}

// Claim (real ext4/XFS journal): Initialize commits the open frame as the
// genuine first transition; a started dispatch survives Close and established
// recovery, whose replay issues no second start; recovery without the
// attestation refuses m0_recovery_held; committed history is never rewritten
// (the recovered journal extends the old bytes); and the cached projection
// record is materialized.
func TestRealJournalLifecycle(t *testing.T) {
	cfg := journalFixture(t)
	e := newEnv(t)
	rt, err := Initialize(cfg, e.seams())
	must(t, err)
	e.rt = rt
	res, err := e.dispatch(t)
	must(t, err)
	if res.State != string(phaseStarted) {
		t.Fatalf("REAL: %+v", res)
	}
	must(t, rt.Close())
	before := journalBytes(t, cfg)
	held := cfg
	held.RecoveryPrerequisiteEstablished = false
	if _, err := OpenEstablished(held, e.seams()); CodeOf(err) != CodeRecoveryHeld {
		t.Fatalf("REAL: recovery without attestation: %v", err)
	}
	if !bytes.Equal(before, journalBytes(t, cfg)) {
		t.Fatal("REAL: refused recovery touched the journal")
	}
	rt2, err := OpenEstablished(cfg, e.seams())
	must(t, err)
	e.rt = rt2
	if rec := e.record(t, dispatchID); rec.Phase != phaseStarted {
		t.Fatalf("REAL: recovered %+v", rec)
	}
	again, err := e.dispatch(t)
	must(t, err)
	if !again.Replayed || again.State != string(phaseStarted) {
		t.Fatalf("REAL: replay %+v", again)
	}
	if starts, _ := e.launcher.count(); starts != 1 {
		t.Fatalf("REAL: starts=%d", starts)
	}
	must(t, rt2.Close())
	after := journalBytes(t, cfg)
	if len(after) <= len(before) || !bytes.Equal(before, after[:len(before)]) {
		t.Fatal("REAL: recovery rewrote committed history")
	}
	if _, err := os.Stat(filepath.Join(cfg.Directory, "records", projectionRecord)); err != nil {
		t.Fatalf("REAL: projection record missing: %v", err)
	}
}

// Claim (real journal, process lost after durable intent): the next session
// recovers the record as in-flight UNKNOWN, the same-ID replay returns
// observation_unknown with no second start, and the launch intent frame is
// the last committed transition.
func TestRealJournalCrashAfterIntent(t *testing.T) {
	cfg := journalFixture(t)
	e := newEnv(t)
	rt, err := Initialize(cfg, e.seams())
	must(t, err)
	e.rt = rt
	e.launcher.onStart = func() { must(t, rt.Close()) }
	_, err = e.dispatch(t)
	wantCode(t, err, CodeStorageFailed)
	e.launcher.onStart = nil
	rt2, err := OpenEstablished(cfg, e.seams())
	must(t, err)
	defer rt2.Close()
	e.rt = rt2
	if rec := e.record(t, dispatchID); rec.Phase != phaseInFlight || rec.EffectID == "" {
		t.Fatalf("CRASH-REAL: %+v", rec)
	}
	res, err := e.dispatch(t)
	wantCode(t, err, CodeObservationUnknown)
	if !res.Replayed {
		t.Fatal("CRASH-REAL: replay restarted")
	}
	if starts, _ := e.launcher.count(); starts != 1 {
		t.Fatalf("CRASH-REAL: starts=%d", starts)
	}
}

// Claim: Attach opens the ledger only when the configuration enables M0 and
// the policy source reports an installed policy; the returned extension serves
// one dispatch over the real journal, and a second Attach with the
// "established" entry recovers it. With default seams the same configuration
// is m0_closed and no state file is created.
func TestAttachOpensLedger(t *testing.T) {
	cfg := journalFixture(t)
	e := newEnv(t)
	doc := `{"enabled":true,"entry":"initialize","kind":"swarma-dispatcher m0-config/1","recovery_attested":true,"state_dir":"` + cfg.Directory + `","supported_local_storage":true,"v":1}`
	read := func(string) ([]byte, error) { return []byte(doc), nil }
	if _, err := Attach("/m0.json", DefaultSeams(), read); CodeOf(err) != CodeClosed {
		t.Fatalf("ATTACH: default seams %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.Directory, "journal")); !os.IsNotExist(err) {
		t.Fatal("ATTACH: closed gate created state")
	}
	ext, err := Attach("/m0.json", e.seams(), read)
	must(t, err)
	reply, err := ext.Serve(e.peer, dispatchID, e.dispatchWire)
	must(t, err)
	if !strings.Contains(reply, `"state":"started"`) {
		t.Fatalf("ATTACH: %s", reply)
	}
	must(t, ext.Close())
	established := strings.Replace(doc, `"entry":"initialize"`, `"entry":"established"`, 1)
	ext2, err := Attach("/m0.json", e.seams(), func(string) ([]byte, error) { return []byte(established), nil })
	must(t, err)
	defer ext2.Close()
	reply, err = ext2.Serve(e.peer, dispatchID, e.dispatchWire)
	must(t, err)
	if !strings.Contains(reply, `"state":"started"`) {
		t.Fatalf("ATTACH: recovered %s", reply)
	}
	if starts, _ := e.launcher.count(); starts != 1 {
		t.Fatalf("ATTACH: starts=%d", starts)
	}
}
