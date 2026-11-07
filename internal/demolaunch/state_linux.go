//go:build linux

package demolaunch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
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
	dir         *os.File
	uid         uint32
	sync        func(*os.File) error
	createFault func(string) error // Nil in production; tests can fail before naming.
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
	if s.createFault != nil {
		if err := s.createFault(name); err != nil {
			return err
		}
	}
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
	b, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return nil, err
	}
	if len(b) > 4096 {
		return nil, ErrLimit
	}
	return b, nil
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
	if _, e := object(b, 4096, "request_id request_digest execution_id time", ""); e != nil || typed(b, &a) != nil || !validID(a.RequestID) || !digestID.MatchString(a.Digest) || !executionID.MatchString(a.ExecutionID) {
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
	latchAtStartup := false
	torn := map[string]bool{}
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
		if strings.HasSuffix(n, ".log") {
			f, x := openAt(e.store.dir, n, syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
			if x != nil {
				return x
			}
			id, x := identity(f)
			f.Close()
			if x != nil || !stateMetadata(id, e.store.uid) {
				return ErrOwnership
			}
			if id.size > logCap {
				return ErrInvalid
			}
			continue
		}
		if n == "closed" {
			latchAtStartup = true
			if b, x := e.store.read(n); x == nil && (len(b) == 0 || !json.Valid(b)) {
				e.torn = append(e.torn, n)
				e.closed = true
			}
			continue
		}
		b, x := e.store.read(n)
		if x != nil {
			return x
		}
		if len(b) == 0 || !json.Valid(b) {
			torn[n] = true
			e.torn = append(e.torn, n)
			e.closed = true
			continue
		}
		if strings.HasSuffix(n, ".truncated") && string(b) != "true" {
			return ErrInvalid
		}
		if strings.HasSuffix(n, ".lost") && string(b) != `"output_lost"` {
			return ErrInvalid
		}
	}
	sort.Strings(e.torn)
	if len(e.torn) > 0 {
		_ = e.closeAdmission("torn_state")
	}
	if err = e.store.closedPresent(); err == nil {
		e.closed = true
		if latchAtStartup {
			e.latchWritten = true
		}
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
		at := attempt{}
		if !torn["attempt-"+a.Generation] {
			at, err = parseAttempt(b)
			if err != nil {
				return err
			}
		}
		e.attempts[name] = at
		b, err = e.store.read("stop-" + a.Generation)
		if errors.Is(err, os.ErrNotExist) || (err == nil && torn["stop-"+a.Generation]) {
			e.tornIntents[name] = true
		} else if err == nil {
			si, x := parseIntent(b)
			if x != nil || (at.ExecutionID != "" && si.ExecutionID != at.ExecutionID) {
				return ErrInvalid
			}
			e.stops[name] = &stopRecord{intent: si}
			if at.ExecutionID == "" {
				at.ExecutionID = si.ExecutionID
				e.attempts[name] = at
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		b, err = e.store.read("stopped-" + a.Generation)
		if err == nil && torn["stopped-"+a.Generation] {
			// A torn completion is never an absence fact.
		} else if err == nil {
			var record stoppedRecord
			if _, x := object(b, 4096, "execution_id timing closed code", ""); x != nil || typed(b, &record) != nil || record.ExecutionID != at.ExecutionID || (record.Code != "" && record.Code != "latch_uncertain") || (record.Timing != "timely" && record.Timing != "missed" && record.Timing != "unknown") {
				return ErrInvalid
			}
			if (e.tornIntents[name] && (record.Timing == "timely" || !record.Closed)) || ((record.Timing != "timely" || record.Code != "") && !record.Closed) {
				return ErrInvalid
			}
			if record.Closed {
				_ = e.closeAdmission("restored_closed_stop")
			}
			e.stopped[name] = true
			if e.tornIntents[name] {
				continue
			} // No original arguments to replay.
			e.stops[name].terminal = &response{State: "absent", Timing: record.Timing, Closed: e.closed, Stopped: true, Code: record.Code}
		} else if errors.Is(err, os.ErrNotExist) {
			_ = e.closeAdmission("startup_unstopped")
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
		if !found && !torn[n] {
			return ErrInvalid
		}
	}
	return nil
}

// Caller holds the short state mutex, or is in single-threaded startup.
func (e *engine) closeAdmission(reason string) error {
	e.closed = true
	if e.latchReason == "" {
		e.latchReason = reason
	}
	if e.latchWritten {
		return nil
	}
	err := e.store.create("closed", marshal(map[string]string{"reason": e.latchReason, "time": time.Now().UTC().Format(time.RFC3339Nano)}))
	if errors.Is(err, os.ErrExist) {
		err = e.store.closedPresent()
		if err == nil {
			// An earlier failed write may have left the name without durability.
			var f *os.File
			f, err = openAt(e.store.dir, "closed", syscall.O_RDONLY|syscall.O_NONBLOCK, 0)
			if err == nil {
				err = e.store.sync(f)
				f.Close()
			}
			if err == nil {
				err = e.store.sync(e.store.dir)
			}
		}
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
