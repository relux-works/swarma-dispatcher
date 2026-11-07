//go:build linux

package demolaunch

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
)

type stoppedRecord struct {
	ExecutionID string `json:"execution_id"`
	Timing      string `json:"timing"`
	Closed      bool   `json:"closed"`
	Code        string `json:"code"`
}

type stopIntent struct {
	RequestID   string `json:"request_id"`
	ExecutionID string `json:"execution_id"`
	Grace       int    `json:"grace_seconds"`
	Time        string `json:"time"`
}
type stateStore struct {
	dir  *os.File
	uid  uint32
	sync func(*os.File) error
}

func openState(name string, uid uint32) (*stateStore, error) {
	d, e := pinDirectory(name, uid)
	if e != nil {
		return nil, e
	}
	defer d.close()
	s := d.ids[len(d.ids)-1]
	if s.uid != uid || s.mode&07777 != 0700 {
		return nil, ErrOwnership
	}
	f, e := openAt(d.last(), ".", syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if e != nil {
		return nil, e
	}
	return &stateStore{dir: f, uid: uid, sync: func(f *os.File) error { return f.Sync() }}, nil
}

// No state path is removed, renamed, reopened for writing or truncated.
// Even an uncertain write leaves its exclusive name reserved forever.
func (s *stateStore) create(name string, b []byte) error {
	f, e := openAt(s.dir, name, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	id, e := identity(f)
	if e != nil || id.uid != s.uid || id.mode&07777 != 0600 || id.links != 1 {
		return ErrOwnership
	}
	n, e := f.Write(b)
	if e != nil {
		return e
	}
	if n != len(b) {
		return errors.New("short state write")
	}
	if e = s.sync(f); e != nil {
		return e
	}
	return s.sync(s.dir)
}
func (s *stateStore) read(name string) ([]byte, error) {
	f, e := openAt(s.dir, name, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	id, e := identity(f)
	if e != nil || !stateMetadata(id, s.uid) {
		return nil, ErrOwnership
	}
	return readSnapshot(f, 4096)
}
func (s *stateStore) names() ([]string, error) {
	f, e := openAt(s.dir, ".", syscall.O_RDONLY|syscall.O_DIRECTORY, 0)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	entries, e := f.Readdirnames(256)
	if e != nil && e != io.EOF {
		return nil, e
	}
	if len(entries) >= 256 {
		return nil, ErrLimit
	}
	return entries, nil
}
func parseAttempt(b []byte) (attempt, error) {
	var a attempt
	if _, e := object(b, 4096, "request_digest execution_id time", ""); e != nil || typed(b, &a) != nil || !digestID.MatchString(a.Digest) || !executionID.MatchString(a.ExecutionID) {
		return attempt{}, ErrInvalid
	}
	if _, e := time.Parse(time.RFC3339Nano, a.Time); e != nil {
		return attempt{}, ErrInvalid
	}
	return a, nil
}
func parseIntent(b []byte) (stopIntent, error) {
	var s stopIntent
	if _, e := object(b, 4096, "request_id execution_id grace_seconds time", ""); e != nil || typed(b, &s) != nil || !validID(s.RequestID) || !executionID.MatchString(s.ExecutionID) || s.Grace < 1 || s.Grace > 30 {
		return stopIntent{}, ErrInvalid
	}
	if _, e := time.Parse(time.RFC3339Nano, s.Time); e != nil {
		return stopIntent{}, ErrInvalid
	}
	return s, nil
}
func (e *engine) load() error {
	names, err := e.store.names()
	if err != nil {
		return err
	}
	known := map[string]bool{"closed": true}
	for _, a := range e.accounts {
		for _, prefix := range []string{"attempt-", "stopped-", "stop-", "output-"} {
			if prefix == "output-" {
				for _, stream := range []string{"stdout", "stderr"} {
					known[prefix+a.Generation+"-"+stream+".log"] = true
					known[prefix+a.Generation+"-"+stream+".truncated"] = true
					known[prefix+a.Generation+"-"+stream+".lost"] = true
				}
			} else {
				known[prefix+a.Generation] = true
			}
		}
	}
	for _, n := range names {
		if !known[n] {
			return fmt.Errorf("state_unknown_entry: %s", n)
		}
		if strings.HasPrefix(n, "output-") {
			f, x := openAt(e.store.dir, n, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
			if x != nil {
				return x
			}
			id, x := identity(f)
			f.Close()
			if x != nil || !stateMetadata(id, e.store.uid) {
				return ErrOwnership
			}
			if strings.HasSuffix(n, ".log") {
				if id.size > logCap {
					return ErrInvalid
				}
			} else {
				b, x := e.store.read(n)
				if x != nil {
					return x
				}
				want := "true"
				if strings.HasSuffix(n, ".lost") {
					want = "output_lost"
				}
				if string(b) != want {
					return ErrInvalid
				}
			}
		}
	}
	if err = e.store.closedPresent(); err == nil {
		e.closed = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for name, a := range e.accounts {
		b, err := e.store.read("attempt-" + a.Generation)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		at, err := parseAttempt(b)
		if err != nil {
			return err
		}
		e.attempts[name] = at
		b, err = e.store.read("stop-" + a.Generation)
		if err == nil {
			si, x := parseIntent(b)
			if x != nil || si.ExecutionID != at.ExecutionID {
				return ErrInvalid
			}
			e.stops[name] = &stopRecord{intent: si}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		b, err = e.store.read("stopped-" + a.Generation)
		if err == nil {
			if e.stops[name] == nil {
				return ErrInvalid
			}
			var record stoppedRecord
			if _, x := object(b, 4096, "execution_id timing closed code", ""); x != nil || typed(b, &record) != nil || record.ExecutionID != at.ExecutionID || (record.Code != "" && record.Code != "latch_uncertain") || (record.Timing != "timely" && record.Timing != "missed" && record.Timing != "unknown") {
				return ErrInvalid
			}
			if (record.Timing != "timely" || record.Code != "") && !record.Closed {
				return ErrInvalid
			}
			if record.Closed {
				if x := e.closeAdmission("restored_closed_stop"); x != nil {
					return x
				}
			}
			e.stopped[name] = true
			e.stops[name].terminal = &response{State: "absent", Timing: record.Timing, Closed: e.closed, Stopped: true, Code: record.Code}
		} else if errors.Is(err, os.ErrNotExist) {
			if err = e.closeAdmission("startup_unstopped"); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	// Orphan auxiliary records cannot establish a safe empty state.
	for _, n := range names {
		if n == "closed" {
			continue
		}
		found := false
		for name, a := range e.accounts {
			if strings.Contains(n, a.Generation) {
				_, found = e.attempts[name]
				break
			}
		}
		if !found {
			return ErrInvalid
		}
	}
	return nil
}

// Caller holds the short state mutex, or is in single-threaded startup.
func (e *engine) closeAdmission(reason string) error {
	e.closed = true
	if e.latchWritten {
		return nil
	}
	err := e.store.create("closed", marshal(map[string]string{"reason": reason, "time": time.Now().UTC().Format(time.RFC3339Nano)}))
	if errors.Is(err, os.ErrExist) {
		err = e.store.closedPresent()
	}
	if err == nil {
		e.latchWritten = true
	}
	return err
}

func stateMetadata(id fileID, uid uint32) bool {
	return id.uid == uid && id.mode&syscall.S_IFMT == syscall.S_IFREG && id.mode&07777 == 0600 && id.links == 1
}

// Called under engine.mu. Durable evidence survives restart; an unwritable
// evidence directory additionally exposes the live capture error as uncertainty.
func (e *engine) outputLoss(name string) []string {
	var loss []string
	a, ok := e.accounts[name]
	if !ok {
		return nil
	}
	for _, stream := range []string{"stdout", "stderr"} {
		if ch := e.launchChildren[name]; ch != nil {
			c := ch.stdout
			if stream == "stderr" {
				c = ch.stderr
			}
			c.mu.Lock()
			uncertain := c.lossPersistErr != nil
			c.mu.Unlock()
			if uncertain {
				loss = append(loss, stream+":loss_unpersisted")
				continue
			}
		}
		_, err := e.store.read("output-" + a.Generation + "-" + stream + ".lost")
		if err == nil {
			loss = append(loss, stream+":output_lost")
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			loss = append(loss, stream+":evidence_unavailable")
			continue
		}
		if ch := e.launchChildren[name]; ch != nil {
			c := ch.stdout
			if stream == "stderr" {
				c = ch.stderr
			}
			if _, _, err := c.snapshot(); err != nil {
				loss = append(loss, stream+":loss_unpersisted")
			}
		}
	}
	return loss
}

// The latch is its protected inode, not a parsed payload. Even a torn/empty
// payload keeps stop available; malformed metadata still refuses startup.
func (s *stateStore) closedPresent() error {
	f, err := openAt(s.dir, "closed", syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	id, err := identity(f)
	if err != nil {
		return err
	}
	if !stateMetadata(id, s.uid) {
		return ErrOwnership
	}
	return nil
}
