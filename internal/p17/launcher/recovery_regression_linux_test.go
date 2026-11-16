//go:build linux && (amd64 || arm64)

package launcher_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
	"github.com/relux-works/swarma-dispatcher/internal/p17/sweep"
)

// Numeric test adaptation lets these behavior controls run against clients
// predating the explicit installation field, without changing their production code.
func installationFact(e *env, fact uint64) {
	field := reflect.ValueOf(&e.cfg).Elem().FieldByName("Installation")
	if field.IsValid() {
		field.SetUint(fact)
	}
}
func startLedger(t *testing.T, q *sweep.Quarantine, d string, fact uint64) {
	t.Helper()
	method := reflect.ValueOf(q).MethodByName("Start")
	args := []reflect.Value{reflect.ValueOf(d)}
	if method.Type().IsVariadic() {
		facts := reflect.MakeSlice(method.Type().In(1), 1, 1)
		facts.Index(0).SetUint(fact)
		args = append(args, facts)
	}
	var out []reflect.Value
	if method.Type().IsVariadic() {
		out = method.CallSlice(args)
	} else {
		out = method.Call(args)
	}
	if !out[0].IsNil() {
		t.Fatal(out[0].Interface())
	}
}

// Claim: losing an established pre-record ledger blocks every uid without replacement; its present neighbour preserves the held uid and permits another uid.
func TestEstablishedLedgerRequired(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "present"}[missing], func(t *testing.T) {
			e := newEnv(t)
			must(t, e.proc.Add(777, [4]uint32{0, 0, cuid, 0}, 7))
			e.src.PrepareMode = "dependency"
			_, err := e.build().Begin(context.Background(), "pre-record")
			if !codes.Is(err, codes.OriginUnavailable) || !e.quar.Held(cuid) || len(e.store.Keys()) != 0 {
				t.Fatalf("setup: %v", err)
			}
			must(t, e.quar.Close())
			if missing {
				must(t, os.Remove(filepath.Join(e.jcfg.Directory, sweep.QuarantineName)))
			}
			e.quar = &sweep.Quarantine{}
			installationFact(e, 2)
			reports := e.build().Restart(context.Background())
			if missing {
				if len(reports) != 1 || !codes.Is(reports[0].Err, codes.IsolationUnqualified) {
					t.Fatalf("lost established ledger accepted: %+v", reports)
				}
				for _, uid := range []uint32{cuid, cuid + 99} {
					if !e.quar.Held(uid) || e.quar.Lease(uid, "next") == nil {
						t.Fatalf("missing ledger leased %d", uid)
					}
				}
				if _, err := os.Lstat(filepath.Join(e.jcfg.Directory, sweep.QuarantineName)); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing ledger replaced: %v", err)
				}
			} else {
				if !e.quar.Held(cuid) {
					t.Fatal("present ledger lost hold")
				}
				must(t, e.quar.Lease(cuid+99, "valid-neighbour"))
			}
		})
	}
}

// Claim: explicit fresh refuses any ledger, journal or custom-directory record, even when the ledger is missing; a truly empty fresh installation starts.
func TestFreshInstallationCrossCheck(t *testing.T) {
	for _, kind := range []string{"ledger", "journal", "record", "empty"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			startLedger(t, e.quar, e.jcfg.Directory, 1)
			must(t, e.quar.Close())
			path := filepath.Join(e.jcfg.Directory, sweep.QuarantineName)
			if kind != "ledger" {
				must(t, os.Remove(path))
			}
			if kind == "journal" {
				must(t, os.WriteFile(filepath.Join(e.jcfg.Directory, "journal"), []byte("prior state"), 0600))
			}
			if kind == "record" {
				d := filepath.Join(e.jcfg.Directory, "custom-records")
				must(t, os.Mkdir(d, 0700))
				must(t, os.WriteFile(filepath.Join(d, "run-orphan.json"), []byte("prior state"), 0600))
			}
			e.quar = &sweep.Quarantine{}
			installationFact(e, 1)
			reports := e.build().Restart(context.Background())
			if kind == "empty" {
				if len(reports) != 0 {
					t.Fatalf("fresh control: %+v", reports)
				}
				must(t, e.quar.Lease(cuid, "control"))
				return
			}
			if len(reports) != 1 || !codes.Is(reports[0].Err, codes.IsolationUnqualified) || !e.quar.Held(cuid) || e.quar.Lease(cuid, "next") == nil {
				t.Fatalf("fresh over %s accepted: %+v", kind, reports)
			}
		})
	}
}

// Claim: H6 returns record-less token/transferred leases only with no helper and a qualified zero proof; live, helper and unqualified neighbours stay held and are reported.
func TestRestartRechecksRecordless(t *testing.T) {
	for _, mode := range []string{"clean", "transferred", "live", "helper", "unqualified"} {
		t.Run(mode, func(t *testing.T) {
			e := newEnv(t)
			l := e.build()
			if reports := l.Restart(context.Background()); len(reports) != 0 {
				t.Fatalf("startup: %+v", reports)
			}
			key := "crashed-token"
			must(t, e.quar.Lease(cuid, key))
			if mode == "transferred" {
				must(t, e.quar.Transfer(cuid, key, "recordless-nonce"))
				key = "recordless-nonce"
			}
			if mode == "live" {
				must(t, e.proc.Add(777, [4]uint32{0, 0, cuid, 0}, 7))
			}
			if mode == "helper" {
				must(t, e.quar.MarkHelper(cuid, key))
			}
			if mode == "unqualified" {
				must(t, e.proc.SetMount("proc", "/", "rw,hidepid=2"))
			}
			must(t, e.store.Close())
			must(t, e.quar.Close())
			e.quar = &sweep.Quarantine{}
			installationFact(e, 2)
			var err error
			e.store, err = runrecord.Initialize(e.jcfg)
			must(t, err)
			reports := e.build().Restart(context.Background())
			held := mode != "clean" && mode != "transferred"
			if len(reports) != 1 || reports[0].Key != key || reports[0].UIDReleased == held || e.quar.Held(cuid) != held {
				t.Fatalf("recovery: %+v held=%v", reports, e.quar.Held(cuid))
			}
			uid := reflect.ValueOf(reports[0]).FieldByName("UID")
			reason := reflect.ValueOf(reports[0]).FieldByName("Reason")
			if !uid.IsValid() || uid.Uint() != uint64(cuid) || !reason.IsValid() || reason.String() == "" {
				t.Fatal("record-less report lacks uid/reason")
			}
			if held {
				if e.quar.Lease(cuid, "next") == nil {
					t.Fatal("unproved lease returned")
				}
				must(t, e.quar.Lease(cuid+1, "other"))
			} else {
				must(t, e.quar.Lease(cuid, "next"))
			}
			if len(e.km.Binds()) != 0 || len(e.km.Ends()) != 0 || len(e.killer.UIDs()) != 0 {
				t.Fatal("record-less recovery contacted holder/helper")
			}
		})
	}
}
