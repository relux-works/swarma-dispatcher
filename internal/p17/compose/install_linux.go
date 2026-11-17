//go:build linux

package compose

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

const (
	ledgerMarker  = ".ledger-install"
	recordMarker  = ".record-install"
	bootstrapName = ".composition.lock"
)

var ErrInstallation = errors.New("composition: installation marker or prior state refused")

type installation struct {
	dir      string
	syncFile func(*os.File) error
	step     func(string) error
}

type bootFacts struct {
	ledger             sweep.Installation
	firstStart         bool
	journalEstablished bool
}

func (i installation) checkpoint(step string) error {
	if i.step != nil {
		return i.step(step)
	}
	return nil
}

// write publishes an independent durable fact. Reserved temporaries are never
// read as authority; a stopped write leaves the previous authoritative marker.
func (i installation) write(name, value string) (result error) {
	defer func() {
		if result != nil {
			result = errors.Join(sweep.ErrNotDurable, result)
		}
	}()
	f, err := os.CreateTemp(i.dir, ".install-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	syncFile := i.syncFile
	if syncFile == nil {
		syncFile = func(f *os.File) error { return f.Sync() }
	}
	if err = i.checkpoint(name + ":temp"); err == nil {
		_, err = f.WriteString(value)
	}
	if err == nil {
		err = syncFile(f)
	}
	if err == nil {
		err = i.checkpoint(name + ":file-sync")
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), filepath.Join(i.dir, name)); err != nil {
		return err
	}
	if err = i.checkpoint(name + ":rename"); err != nil {
		return err
	}
	d, err := os.Open(i.dir)
	if err != nil {
		return err
	}
	err = errors.Join(syncFile(d), d.Close())
	if err == nil {
		err = i.checkpoint(name + ":directory-sync")
	}
	return err
}

func protectedDirectory(dir string) error {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return ErrInstallation
	}
	for path := dir; ; path = filepath.Dir(path) {
		fi, err := os.Lstat(path)
		if err != nil {
			return errors.Join(ErrInstallation, err)
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 || st.Mode&0022 != 0 || (st.Uid != 0 && st.Uid != uint32(os.Geteuid())) {
			return ErrInstallation
		}
		if path == dir && (st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700) {
			return ErrInstallation
		}
		if path == "/" {
			break
		}
	}
	return nil
}

func bootstrapLock(dir string) (*os.File, error) {
	if err := protectedDirectory(dir); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(dir, bootstrapName), syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), bootstrapName)
	var st syscall.Stat_t
	if err = syscall.Fstat(fd, &st); err == nil && (st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1) {
		err = ErrInstallation
	}
	if err == nil {
		err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

func (i installation) read(name string) (string, bool, error) {
	fd, err := syscall.Open(filepath.Join(i.dir, name), syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, errors.Join(ErrInstallation, err)
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		return "", false, errors.Join(ErrInstallation, err)
	}
	if st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 || st.Size > 4096 {
		return "", false, ErrInstallation
	}
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return "", false, err
	}
	return string(b), true, nil
}

func (i installation) priorRecords() (bool, error) {
	found := false
	err := filepath.WalkDir(i.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "journal" || strings.HasPrefix(d.Name(), "run-") {
			found = true
		}
		if d.Type()&os.ModeSymlink != 0 {
			return ErrInstallation
		}
		return nil
	})
	return found, err
}

func (i installation) emptyIntentState() error {
	entries, err := os.ReadDir(i.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		n := e.Name()
		if n == ledgerMarker || n == recordMarker || n == bootstrapName || n == "launcher.lock" || n == sweep.QuarantineName || strings.HasPrefix(n, ".install-") || strings.HasPrefix(n, ".quarantine-") {
			continue
		}
		if (n == "records" || n == "inputs") && e.IsDir() {
			children, err := os.ReadDir(filepath.Join(i.dir, n))
			if err != nil || len(children) != 0 {
				return ErrInstallation
			}
			continue
		}
		return ErrInstallation
	}
	return nil
}

// zeroLedger accepts the exact empty-ledger object the client can persist,
// allowing whitespace but rejecting duplicate/unknown/ambiguous members.
func zeroLedger(b string) bool {
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(b)) != nil {
		return false
	}
	return compact.String() == `{"version":1,"entries":{}}`
}

// prepare is serialized by the separate bootstrap lock before the quarantine
// takes launcher.lock. Explicit intent is the only authority to resume first
// start with an existing zero-entry ledger; absence never overrides prior state.
func (i installation) prepare() (bootFacts, error) {
	var facts bootFacts
	ledger, ledgerExists, err := i.read(ledgerMarker)
	if err != nil {
		return facts, err
	}
	record, recordExists, err := i.read(recordMarker)
	if err != nil {
		return facts, err
	}
	if ledgerExists && ledger != "starting\n" && ledger != "established\n" {
		return facts, ErrInstallation
	}
	if recordExists && record != "initialize\n" && record != "established\n" {
		return facts, ErrInstallation
	}
	prior, err := i.priorRecords()
	if err != nil {
		return facts, errors.Join(ErrInstallation, err)
	}
	if prior && record != "established\n" {
		return facts, ErrInstallation
	}
	if ledger != "established\n" && (prior || record == "established\n") {
		return facts, ErrInstallation
	}
	_, ledgerErr := os.Lstat(filepath.Join(i.dir, sweep.QuarantineName))
	// The initialize marker is durable before Start can create any ledger.
	if !recordExists && (ledger == "established\n" || ledgerErr == nil) {
		return facts, ErrInstallation
	}
	if !ledgerExists {
		if !errors.Is(ledgerErr, os.ErrNotExist) {
			return facts, ErrInstallation
		}
		if err := i.emptyIntentState(); err != nil {
			return facts, err
		}
		if err := i.write(ledgerMarker, "starting\n"); err != nil {
			return facts, err
		}
		ledger = "starting\n"
	}
	facts.ledger = sweep.Established
	facts.firstStart = ledger == "starting\n"
	if facts.firstStart {
		if err := i.emptyIntentState(); err != nil {
			return facts, err
		}
		if errors.Is(ledgerErr, os.ErrNotExist) {
			facts.ledger = sweep.Fresh
		} else {
			b, exists, err := i.read(sweep.QuarantineName)
			if err != nil || !exists || !zeroLedger(b) {
				return facts, ErrInstallation
			}
		}
	}
	if !recordExists {
		if prior {
			return facts, ErrInstallation
		}
		if err := i.write(recordMarker, "initialize\n"); err != nil {
			return facts, err
		}
		record = "initialize\n"
	}
	facts.journalEstablished = record == "established\n"
	return facts, nil
}
