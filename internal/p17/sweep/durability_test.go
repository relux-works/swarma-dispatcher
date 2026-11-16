package sweep_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/isolation"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	"github.com/relux-works/swarma-dispatcher/internal/p17/launcher"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

type emptyRecords string

func (e emptyRecords) StateDirectory() string { return string(e) }

func (emptyRecords) Keys() []string                       { return nil }
func (emptyRecords) Get(string) (*runrecord.Record, bool) { return nil, false }
func (emptyRecords) Fenced(string) bool                   { return true }
func (emptyRecords) Create(*runrecord.Record) error       { return errors.New("unexpected Create") }
func (emptyRecords) Update(string, func(*runrecord.Record) error) (*runrecord.Record, error) {
	return nil, errors.New("unexpected Update")
}

// Claim: a sync failure before Begin returns requires process exit and poisons further leases; successful syncs durably return the clean pre-record lease.
func TestBeginQuarantineSyncFailure(t *testing.T) {
	for _, failAt := range []int{3, 4, 0} {
		for _, planted := range []bool{true, false} {
			t.Run(fmt.Sprintf("sync-%d-process-%t", failAt, planted), func(t *testing.T) {
				dir := t.TempDir()
				if err := os.Chmod(dir, 0700); err != nil {
					t.Fatal(err)
				}
				proc, err := fakes.NewProcTree(t.TempDir(), "boot")
				if err != nil {
					t.Fatal(err)
				}
				if planted {
					if err := proc.Add(777, [4]uint32{0, 0, 2001, 0}, 42); err != nil {
						t.Fatal(err)
					}
				}
				q := &sweep.Quarantine{}
				defer q.Close()
				l, err := launcher.New(launcher.Config{Installation: sweep.Fresh, StateDirectory: dir, Station: "1", LauncherUID: uint32(os.Getuid()), ProcRoot: proc.Root, ProcMountInfo: proc.MountInfoPath()}, launcher.Deps{Source: &source.Client{}, Records: emptyRecords(dir), Incarnations: incarnation.Proc{}, Placement: isolation.Fixed(2001), Quarantine: q})
				if err != nil {
					t.Fatal(err)
				}
				if r := l.Restart(context.Background()); len(r) != 0 {
					t.Fatalf("startup: %+v", r)
				}
				calls := 0
				sweep.SetSyncForTest(q, func(f *os.File) error {
					calls++
					if failAt != 0 && calls == failAt {
						return errors.New("injected sync failure")
					}
					return f.Sync()
				})
				_, err = l.Begin(context.Background(), runorigin.EventRef{}, keymaker.Principal{UID: 2001, Generation: "1"})
				if failAt != 0 {
					if !launcher.MustExit(err) || !errors.Is(err, sweep.ErrNotDurable) {
						t.Fatalf("sync failure not MustExit: %v", err)
					}
					if q.Lease(2002, "later") == nil {
						t.Fatal("poisoned process leased again")
					}
				} else if launcher.MustExit(err) || q.Held(2001) != planted {
					t.Fatalf("successful sync control: %v held=%v", err, q.Held(2001))
				}
			})
		}
	}
}

// Claim: the launcher lock exists before any journal, excludes a second quarantine and is released only on Close.
func TestLifetimeLockBeforeFirstRecord(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0700); err != nil {
		t.Fatal(err)
	}
	q := &sweep.Quarantine{}
	if err := q.Start(d, sweep.Fresh); err != nil {
		t.Fatal(err)
	}
	other := &sweep.Quarantine{}
	if err := other.Start(d, sweep.Established); err == nil {
		t.Fatal("second launcher took live lock")
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}
	if err := other.Start(d, sweep.Established); err != nil {
		t.Fatalf("closed owner control: %v", err)
	}
	defer other.Close()
}
