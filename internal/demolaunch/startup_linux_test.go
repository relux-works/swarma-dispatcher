//go:build linux

package demolaunch

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Every corrupt auxiliary record has a valid neighboring startup control.
// Rejects invalid orphan facts and unchecked logs/flags; latch persistence
// failure retains startup with admission closed. No fixture evidence is repaired.
func TestStartupStateRows(t *testing.T) {
	for _, variant := range []string{"valid", "unstopped", "garbage-closed", "stopped-without-intent", "orphan", "unknown", "attempt-corrupt", "intent-corrupt", "intent-binding", "stopped-corrupt", "stopped-binding", "log-mode", "log-directory", "log-fifo", "log-link", "truncated-content", "lost-content", "startup-latch-sync"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			at := []byte(`{"request_id":"r","request_digest":"` + strings.Repeat("a", 64) + `","execution_id":"EX-0123456789abcdef0123456789abcdef","time":"2026-01-01T00:00:00Z"}`)
			intent := []byte(`{"request_id":"stop-1","execution_id":"EX-0123456789abcdef0123456789abcdef","grace_seconds":10,"time":"2026-01-01T00:00:00Z"}`)
			stopped := []byte(`{"execution_id":"EX-0123456789abcdef0123456789abcdef","timing":"timely","closed":false,"code":""}`)
			if variant == "attempt-corrupt" {
				at = []byte(`{}`)
			}
			if variant == "intent-corrupt" {
				intent = []byte(`{}`)
			}
			if variant == "intent-binding" {
				intent = bytes.ReplaceAll(intent, []byte("EX-0123456789abcdef0123456789abcdef"), []byte("EX-ffffffffffffffffffffffffffffffff"))
			}
			if variant == "stopped-binding" {
				stopped = bytes.ReplaceAll(stopped, []byte("EX-0123456789abcdef0123456789abcdef"), []byte("EX-ffffffffffffffffffffffffffffffff"))
			}
			if variant == "stopped-corrupt" {
				stopped = []byte(`{}`)
			}
			if variant != "orphan" {
				must(t, h.e.store.create("attempt-"+gen, at))
			}
			if variant != "stopped-without-intent" {
				must(t, h.e.store.create("stop-"+gen, intent))
			}
			if variant != "unstopped" && variant != "startup-latch-sync" {
				must(t, h.e.store.create("stopped-"+gen, stopped))
			}
			log := filepath.Join(h.root, "state", "output-"+gen+"-stdout.log")
			switch variant {
			case "garbage-closed":
				must(t, h.e.store.create("closed", []byte("not json")))
			case "unknown":
				must(t, h.e.store.create("foreign", []byte("untouched")))
			case "log-directory":
				must(t, os.Mkdir(log, 0600))
			case "log-fifo":
				must(t, syscall.Mkfifo(log, 0600))
			case "log-link":
				must(t, os.Symlink("attempt-"+gen, log))
			case "log-mode":
				must(t, os.WriteFile(log, nil, 0644))
				must(t, os.Chmod(log, 0644))
			default:
				must(t, h.e.store.create("output-"+gen+"-stdout.log", []byte("log")))
			}
			flag := []byte("true")
			if variant == "truncated-content" {
				flag = []byte("false")
			}
			must(t, h.e.store.create("output-"+gen+"-stdout.truncated", flag))
			loss := []byte(`"output_lost"`)
			if variant == "lost-content" {
				loss = []byte(`"wrong"`)
			}
			must(t, h.e.store.create("output-"+gen+"-stderr.lost", loss))
			if variant == "startup-latch-sync" {
				h.e.store.sync = func(*os.File) error { return syscall.EIO }
			}
			before, err := os.ReadDir(filepath.Join(h.root, "state"))
			must(t, err)
			e, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
			switch variant {
			case "valid", "unstopped", "garbage-closed":
				must(t, err)
				if e.closed != (variant != "valid") {
					t.Fatal("closed", e.closed)
				}
				if variant == "valid" {
					absentFile(t, h, "closed")
				} else {
					fileBytes(t, h, "closed")
				}
				if variant == "garbage-closed" {
					assertState(t, e.handle(stopBytes(10)), "absent", "")
					if string(fileBytes(t, h, "closed")) != "not json" {
						t.Fatal("closed rewritten")
					}
				}
			case "log-mode", "log-directory", "log-fifo":
				if !errors.Is(err, ErrOwnership) {
					t.Fatal(err)
				}
			case "log-link":
				if !errors.Is(err, syscall.ELOOP) {
					t.Fatal(err)
				}
			case "unknown":
				if err == nil || err.Error() != "state_unknown_entry: foreign" {
					t.Fatal(err)
				}
			case "startup-latch-sync":
				if err != nil || !e.closed || e.latchWritten {
					t.Fatalf("startup latch must retain stop: %v", err)
				}
				fileBytes(t, h, "closed")
			default:
				if !errors.Is(err, ErrInvalid) {
					t.Fatal(err)
				}
			}
			for _, entry := range before {
				if _, x := os.Lstat(filepath.Join(h.root, "state", entry.Name())); x != nil {
					t.Fatal("state removed", x)
				}
			}
			if variant != "orphan" && !bytes.Equal(at, fileBytes(t, h, "attempt-"+gen)) {
				t.Fatal("attempt changed")
			}
			if h.calls.Load() != 0 {
				t.Fatal("startup invoked child")
			}
		})
	}
}

// Protected reads use real regular files, byte caps and links with current-UID
// ownership injection. Public entrypoints still require root-owned snapshots;
// these tests do not qualify the distinct-user deployment.
func TestProtectedReadsAndEntrypoints(t *testing.T) {
	for _, variant := range []string{"valid", "owner", "group", "mode", "link", "oversize", "directory"} {
		t.Run(variant, func(t *testing.T) {
			root := fixtureDir(t)
			name := filepath.Join(root, "input")
			body := []byte(`{"v":1}`)
			if variant == "directory" {
				must(t, os.Mkdir(name, 0444))
			} else {
				must(t, os.WriteFile(name, body, 0444))
				must(t, os.Chmod(name, 0444))
			}
			owner, group := uint32(os.Geteuid()), uint32(os.Getegid())
			if variant == "owner" {
				owner++
			}
			if variant == "group" {
				group++
			}
			if variant == "mode" {
				must(t, os.Chmod(name, 0644))
			}
			if variant == "link" {
				must(t, os.Link(name, name+"-alias"))
			}
			limit := 4096
			if variant == "oversize" {
				limit = 1
			}
			b, err := protectedSnapshotAs(name, 0444, group, limit, owner)
			if variant == "valid" {
				must(t, err)
				if !bytes.Equal(b, body) {
					t.Fatal(string(b))
				}
			} else {
				want := ErrConfig
				if variant == "oversize" {
					want = ErrConfig
				}
				if !errors.Is(err, want) || b != nil {
					t.Fatal(string(b), err)
				}
			}
		})
	}
	root := fixtureDir(t)
	name := filepath.Join(root, "config")
	body := marshal(cfgFixture())
	must(t, os.WriteFile(name, body, 0440))
	must(t, os.Chmod(name, 0440))
	old := syscall.Umask(022)
	defer syscall.Umask(old)
	if err := Daemon(name); !errors.Is(err, ErrConfig) {
		t.Fatal("daemon accepted unprotected config", err)
	}
	if reply, err := Client(name, "status", []byte(`{"op":"demo.status","account":"demo"}`)); reply != "" || !errors.Is(err, ErrConfig) {
		t.Fatal("client accepted unprotected config", reply, err)
	}
	if reply, err := Client(name, "launch", []byte(`{"op":"demo.status","account":"demo"}`)); reply != "" || !errors.Is(err, ErrInvalid) {
		t.Fatal("client operation mismatch", reply, err)
	}
	after, err := os.ReadFile(name)
	must(t, err)
	if !bytes.Equal(after, body) {
		t.Fatal("config changed")
	}
}

// Composed server loading reaches both actual protected file reads. The fixed
// roster path/modes/caps are asserted before mapping it to a current-UID fixture.
// Intended witness for removal of roster protection and frozen-path substitution.
func TestServerProtectedInputWiring(t *testing.T) {
	for _, variant := range []string{"valid", "config-mode", "roster-mode", "roster-symlink", "roster-uid", "roster-owner", "executor-mismatch", "service-mismatch", "service-uid", "operator-uid"} {
		t.Run(variant, func(t *testing.T) {
			root := fixtureDir(t)
			config := filepath.Join(root, "config")
			roster := filepath.Join(root, "roster")
			cfg := cfgFixture()
			doc := rosterDocument(30000)
			switch variant {
			case "roster-uid":
				doc["accounts"].([]map[string]any)[0]["uid"] = 30020
			case "roster-owner":
				doc["accounts"].([]map[string]any)[0]["owning_dispatcher"] = "foreign"
			case "executor-mismatch":
				cfg["executor"] = "swarma-demo-codex-executor"
			case "service-mismatch":
				cfg["service_account"] = "foreign"
			case "service-uid":
				cfg["service_uid"] = 1102
			case "operator-uid":
				cfg["caller"].(map[string]any)["uid"] = 1002
			}
			configBytes, rosterBytes := marshal(cfg), marshal(doc)
			must(t, os.WriteFile(config, configBytes, 0440))
			must(t, os.Chmod(config, 0440))
			must(t, os.WriteFile(roster, rosterBytes, 0644))
			must(t, os.Chmod(roster, 0644))
			if variant == "config-mode" {
				must(t, os.Chmod(config, 0644))
			}
			if variant == "roster-mode" {
				must(t, os.Chmod(roster, 0600))
			}
			if variant == "roster-symlink" {
				must(t, os.Rename(roster, roster+"-target"))
				must(t, os.Symlink(roster+"-target", roster))
			}
			calls := 0
			read := func(name string, mode, gid uint32, limit int) ([]byte, error) {
				calls++
				if calls == 1 {
					if name != config || mode != 0440 || gid != uint32(os.Getegid()) || limit != 16384 {
						t.Fatal("config binding", name, mode, gid, limit)
					}
				} else {
					if calls != 2 || name != "/etc/swarma-demo/roster.json" || mode != 0644 || gid != 0 || limit != 49152 {
						t.Fatal("roster binding", name, mode, gid, limit)
					}
					name = roster
				}
				return protectedSnapshotAs(name, mode, uint32(os.Getegid()), limit, uint32(os.Geteuid()))
			}
			c, a, err := loadServerInputs(config, read)
			if variant == "valid" {
				must(t, err)
				if calls != 2 || c.Executor != "swarma-demo-executor" || len(a) != 3 {
					t.Fatal(c, a, calls)
				}
			} else {
				if !errors.Is(err, ErrConfig) || a != nil {
					t.Fatalf("invalid protected roster accepted: %s %v %v", variant, a, err)
				}
			}
			cb, x := os.ReadFile(config)
			must(t, x)
			rb, x := os.ReadFile(roster)
			must(t, x)
			if !bytes.Equal(cb, configBytes) || !bytes.Equal(rb, rosterBytes) {
				t.Fatal("protected input changed")
			}
		})
	}
}

// Unreadable or wrongly protected latches cannot silently reopen admission.
// Valid garbage is deliberately closed; mode/type/link failures still refuse.
func TestClosedMetadata(t *testing.T) {
	for _, variant := range []string{"valid", "mode", "link", "directory"} {
		t.Run(variant, func(t *testing.T) {
			h := fixture(t, "good")
			name := filepath.Join(h.root, "state", "closed")
			switch variant {
			case "directory":
				must(t, os.Mkdir(name, 0600))
			case "link":
				must(t, os.Symlink("absent", name))
			default:
				must(t, os.WriteFile(name, []byte("garbage"), 0600))
				if variant == "mode" {
					must(t, os.Chmod(name, 0644))
				}
			}
			e, err := testEngine(t, h.e.ctx, h.e.store, []account{h.e.accounts["demo"]}, serverConfig{})
			if variant == "valid" {
				must(t, err)
				if !e.closed {
					t.Fatal("latch ignored")
				}
				assertState(t, e.handle(launchBytes("demo")), "refused", "admission_closed")
			} else {
				want := ErrOwnership
				if variant == "link" {
					want = syscall.ELOOP
				}
				if !errors.Is(err, want) {
					t.Fatal(err)
				}
			}
			if h.calls.Load() != 0 {
				t.Fatal("startup effect")
			}
			if _, err := os.Lstat(name); err != nil {
				t.Fatal("latch removed", err)
			}
		})
	}
}

// Snapshot rechecks bind both the opened file and its name/ancestors after the
// read. Controls exercise post-read identity or directory revalidation.
func TestSnapshotPostReadChecks(t *testing.T) {
	for _, variant := range []string{"valid", "mode", "replace", "size", "ancestor"} {
		t.Run(variant, func(t *testing.T) {
			root := fixtureDir(t)
			dir := filepath.Join(root, "inputs")
			must(t, os.Mkdir(dir, 0700))
			name := filepath.Join(dir, "config")
			body := []byte(`{"v":1}`)
			must(t, os.WriteFile(name, body, 0444))
			must(t, os.Chmod(name, 0444))
			read := func(r io.Reader, n int) ([]byte, error) {
				b, err := readSnapshot(r, n)
				if err != nil {
					return b, err
				}
				switch variant {
				case "mode":
					must(t, os.Chmod(name, 0644))
				case "replace":
					must(t, os.Rename(name, name+"-old"))
					must(t, os.WriteFile(name, body, 0444))
					must(t, os.Chmod(name, 0444))
				case "size":
					must(t, os.Chmod(name, 0644))
					f, err := os.OpenFile(name, os.O_APPEND|os.O_WRONLY, 0)
					must(t, err)
					_, err = f.Write([]byte(" "))
					must(t, err)
					must(t, f.Close())
					must(t, os.Chmod(name, 0444))
				case "ancestor":
					must(t, os.Chmod(dir, 0770))
				}
				return b, nil
			}
			b, err := protectedRead(name, 0444, uint32(os.Getegid()), 4096, uint32(os.Geteuid()), read)
			if variant == "valid" {
				must(t, err)
				if !bytes.Equal(b, body) {
					t.Fatal(string(b))
				}
			} else {
				want := ErrConfig
				if variant == "ancestor" {
					want = ErrIdentity
				}
				if !errors.Is(err, want) || b != nil {
					t.Fatal(string(b), err)
				}
			}
		})
	}
}

// Real ancestor protection and replacement tests pair valid pins with each
// forbidden mode/name change; they never modify checkout ancestors themselves.
func TestAncestorPins(t *testing.T) {
	for _, variant := range []string{"valid", "writable", "symlink", "replaced"} {
		t.Run(variant, func(t *testing.T) {
			root := fixtureDir(t)
			parent := filepath.Join(root, "parent")
			leaf := filepath.Join(parent, "leaf")
			must(t, os.Mkdir(parent, 0700))
			must(t, os.Mkdir(leaf, 0700))
			pin, err := pinDirectory(leaf, uint32(os.Geteuid()))
			must(t, err)
			defer pin.close()
			switch variant {
			case "writable":
				must(t, os.Chmod(parent, 0770))
			case "symlink":
				must(t, os.Rename(parent, parent+"-old"))
				must(t, os.Symlink(parent+"-old", parent))
			case "replaced":
				must(t, os.Rename(parent, parent+"-old"))
				must(t, os.Mkdir(parent, 0700))
				must(t, os.Mkdir(leaf, 0700))
			}
			err = pin.recheck()
			if variant == "valid" {
				must(t, err)
			} else if !errors.Is(err, ErrIdentity) {
				t.Fatal(err)
			}
		})
	}
}

// Current-process runtime identity has an admitted control. Wrong service UID,
// private GID or missing connect membership refuses; no credentials are changed.
func TestRuntimeIdentity(t *testing.T) {
	cfg := commonConfig{ServiceUID: uint32(os.Geteuid()), ServiceGID: uint32(os.Getegid()), ConnectGID: uint32(os.Getegid())}
	must(t, runtimeConfig(cfg, cfg.ServiceUID, true))
	if err := runtimeConfig(cfg, cfg.ServiceUID+1, true); !errors.Is(err, ErrIdentity) {
		t.Fatal("runtime identity assertion", err)
	}
	wrong := cfg
	wrong.ServiceGID++
	if err := runtimeConfig(wrong, cfg.ServiceUID, true); !errors.Is(err, ErrIdentity) {
		t.Fatal("runtime identity assertion", err)
	}
	for g := uint32(100000); ; g++ {
		member, err := groupMember(g)
		must(t, err)
		if !member {
			wrong = cfg
			wrong.ConnectGID = g
			break
		}
	}
	if err := runtimeConfig(wrong, cfg.ServiceUID, true); !errors.Is(err, ErrIdentity) {
		t.Fatal("runtime identity assertion", err)
	}
	if err := runtimeConfig(cfg, cfg.ServiceUID, false); !errors.Is(err, ErrIdentity) {
		t.Fatal("runtime identity assertion", "client belongs to private group", err)
	}
	wrong = cfg
	wrong.ServiceGID = wrong.ConnectGID + 100000
	for {
		member, err := groupMember(wrong.ServiceGID)
		must(t, err)
		if !member {
			break
		}
		wrong.ServiceGID++
	}
	must(t, runtimeConfig(wrong, cfg.ServiceUID, false))
}
