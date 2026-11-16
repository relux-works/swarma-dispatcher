//go:build linux

package m0client

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

func fdString(fd int) string { return strconv.Itoa(fd) }

func (e *env) changeStat(fn func(string, *syscall.Stat_t)) {
	native := e.host.stat
	e.host.stat = func(fd int) (syscall.Stat_t, error) {
		st, err := native(fd)
		if err != nil {
			return st, err
		}
		name, err := os.Readlink("/proc/self/fd/" + fdString(fd))
		if err == nil {
			fn(name, &st)
		}
		return st, err
	}
}

func (e *env) editPlan(name string, fn func(map[string]any)) {
	b, err := os.ReadFile(name)
	must(e.t, err)
	var plan map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	must(e.t, d.Decode(&plan))
	fn(plan)
	b, err = json.Marshal(plan)
	must(e.t, err)
	must(e.t, os.WriteFile(name, b, 0600))
}

// Claim: an altered installed plan is signed as-is but refused by the runtime's exact-plan gate; the untampered plan launches once.
func TestTamperedPlanRefusedByRuntime(t *testing.T) {
	e := newEnv(t)
	id, cancelID := e.dispatchID, e.cancelID
	b := e.newBackend(id, cancelID)
	l := e.serveBackend(b)
	if code, _, stderr := e.run("dispatch", "--config", e.cfgPath); code != ExitAccepted {
		t.Fatal("control", code, stderr)
	}
	other := randomID(t)
	e.editPlan(e.dispatchPath, func(p map[string]any) { p["request"].(map[string]any)["request_id"] = other })
	code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
	if code != ExitRefused || !strings.Contains(stdout, "request is not the protected dispatch signing plan") {
		t.Fatalf("tampered plan: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	streams := l.drain()
	if len(streams) != 2 {
		t.Fatalf("connections %d", len(streams))
	}
	sent, err := m0.DecodeRequest(requestFrame(t, streams[1]))
	must(t, err)
	plan, err := e.config().loadPlan(m0.OpDispatch)
	must(t, err)
	if !bytes.Equal(sent.Canonical, plan.Request.Canonical) {
		t.Fatal("client did not send exactly the tampered installed R")
	}
	if starts, _ := b.launcher.count(); starts != 1 {
		t.Fatal("tampered plan launched", starts)
	}
}

// Claim: the same installed dispatch refuses locally with a resend instruction and preserves the original file even without a key; its first submission succeeds.
func TestSecondDispatchSamePlan(t *testing.T) {
	e := newEnv(t)
	l := e.listen(modeAccept)
	if code, _, stderr := e.run("dispatch", "--config", e.cfgPath); code != ExitAccepted {
		t.Fatal("control", code, stderr)
	}
	name := e.storedPath(e.dispatchID)
	before, err := os.ReadFile(name)
	must(t, err)
	var old syscall.Stat_t
	must(t, syscall.Stat(name, &old))
	for _, removeKey := range []bool{false, true} {
		if removeKey {
			must(t, os.Remove(e.keyPath))
		}
		code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
		after, err := os.ReadFile(name)
		must(t, err)
		var now syscall.Stat_t
		must(t, syscall.Stat(name, &now))
		if code != ExitLocal || stdout != "" || !strings.Contains(stderr, "use resend") || !bytes.Equal(before, after) || identity(old) != identity(now) || old.Mtim != now.Mtim || len(l.drain()) != 1 {
			t.Fatalf("second dispatch: exit %d stdout %q stderr %q", code, stdout, stderr)
		}
	}
}

// Claim: a plan associated with another public key refuses key_not_associated before storage/connect; the original plan signs and sends.
func TestPlanKeyNotAssociated(t *testing.T) {
	e := newEnv(t)
	l := e.listen(modeAccept)
	other := make([]byte, ed25519.SeedSize)
	_, err := rand.Read(other)
	must(t, err)
	defer clear(other)
	key, err := m0.KeySpelling(ed25519.NewKeyFromSeed(other).Public().(ed25519.PublicKey))
	must(t, err)
	e.editPlan(e.dispatchPath, func(p map[string]any) { p["request"].(map[string]any)["association"].(map[string]any)["key"] = key })
	code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
	if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonKeyNotAssociated) || e.storedCount() != 0 || len(l.drain()) != 0 {
		t.Fatalf("foreign plan key: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if found := findSeed(other, []byte(stdout), []byte(stderr)); len(found) != 0 {
		t.Fatal("foreign seed leaked", found)
	}
	e.installPlans(e.dispatchID, e.cancelID)
	if code, _, stderr := e.run("dispatch", "--config", e.cfgPath); code != ExitAccepted {
		t.Fatal("control", code, stderr)
	}
}

// Claim: association/caller/real-UID disagreement refuses before key access, storage or connect; matching identities succeed in the same fixture.
func TestLocalCallerUID(t *testing.T) {
	for _, kind := range []string{"association", "caller", "real", "effective"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			id := e.dispatchID
			switch kind {
			case "association":
				e.editPlan(e.dispatchPath, func(p map[string]any) {
					p["request"].(map[string]any)["association"].(map[string]any)["uid"] = e.policy.Association.UID + 1
				})
			case "caller":
				e.doc["caller"].(map[string]any)["uid"] = e.policy.Association.UID + 1
				e.writeConfig()
			case "real":
				e.host.uid = func() int { return int(e.policy.Association.UID + 1) }
			case "effective":
				e.host.euid = func() int { return int(e.policy.Association.UID + 1) }
			}
			for _, missingKey := range []bool{false, true} {
				if missingKey {
					must(t, os.Remove(e.keyPath))
				}
				code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
				reason := reasonConfig
				if kind == "association" {
					reason = reasonCaller
				}
				if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reason) || len(l.drain()) != 0 || e.storedCount() != 0 {
					t.Fatalf("UID mismatch: exit %d stdout %q stderr %q", code, stdout, stderr)
				}
			}
			e.host.uid = func() int { return int(e.policy.Association.UID) }
			e.host.euid = e.host.uid
			e.doc["caller"].(map[string]any)["uid"] = e.policy.Association.UID
			e.writeConfig()
			e.installPlans(id, e.cancelID)
			must(t, os.WriteFile(e.keyPath, e.seed, 0600))
			if code, _, stderr := e.run("dispatch", "--config", e.cfgPath); code != ExitAccepted {
				t.Fatal("control", code, stderr)
			}
		})
	}
}

// Claim: resend refuses a validly signed stored R different from the current plan, without connecting; restoring that plan permits byte-identical resend without the key.
func TestStoredRequestMustEqualPlan(t *testing.T) {
	e := newEnv(t)
	l := e.listen(modeAccept)
	if code, _, stderr := e.dispatch(e.dispatchID); code != ExitAccepted {
		t.Fatal("control", code, stderr)
	}
	original, err := os.ReadFile(e.dispatchPath)
	must(t, err)
	must(t, os.Remove(e.keyPath))
	e.editPlan(e.dispatchPath, func(p map[string]any) { p["request"].(map[string]any)["receiver"] = strings.Repeat("f", 32) })
	code, stdout, stderr := e.resend(e.dispatchID)
	if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonStoredRequest) || len(l.drain()) != 1 {
		t.Fatalf("different plan: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	must(t, os.WriteFile(e.dispatchPath, original, 0600))
	if code, _, stderr := e.resend(e.dispatchID); code != ExitAccepted {
		t.Fatal("restored control", code, stderr)
	}
	streams := l.drain()
	if len(streams) != 2 || !bytes.Equal(streams[0], streams[1]) {
		t.Fatal("resend changed wire bytes")
	}
}

// Claim: unsafe or malformed plan files refuse before key access/store/connect, beside a valid protected-plan control.
func TestPlanFileProtection(t *testing.T) {
	for _, kind := range []string{"control", "symlink", "writable", "directory", "missing", "foreign owner", "wrong operation", "duplicate", "unknown"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			switch kind {
			case "symlink":
				must(t, os.Rename(e.dispatchPath, e.dispatchPath+".real"))
				must(t, os.Symlink(e.dispatchPath+".real", e.dispatchPath))
			case "writable":
				must(t, os.Chmod(e.dispatchPath, 0620))
			case "directory":
				must(t, os.Remove(e.dispatchPath))
				must(t, os.Mkdir(e.dispatchPath, 0700))
			case "missing":
				must(t, os.Remove(e.dispatchPath))
			case "foreign owner":
				e.changeStat(func(name string, st *syscall.Stat_t) {
					if name == e.dispatchPath {
						st.Uid = 65533
					}
				})
			case "wrong operation":
				b, err := os.ReadFile(e.cancelPath)
				must(t, err)
				must(t, os.WriteFile(e.dispatchPath, b, 0600))
			case "duplicate":
				b, err := os.ReadFile(e.dispatchPath)
				must(t, err)
				must(t, os.WriteFile(e.dispatchPath, bytes.Replace(b, []byte(`{`), []byte(`{"v":1,`), 1), 0600))
			case "unknown":
				e.editPlan(e.dispatchPath, func(p map[string]any) { p["extra"] = 1 })
			}
			if kind != "control" {
				must(t, os.Chmod(e.keyPath, 0644))
			}
			code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
			if kind == "control" {
				if code != ExitAccepted {
					t.Fatal("control", code, stderr)
				}
				return
			}
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonPlan) || e.storedCount() != 0 || len(l.drain()) != 0 {
				t.Fatalf("unsafe plan: exit %d stdout %q stderr %q", code, stdout, stderr)
			}
		})
	}
}

// Claim: every configured transport identity pin is enforced before hello; unmodified pins complete the exchange.
func TestConfigurationPins(t *testing.T) {
	for _, kind := range []string{"control", "endpoint dev", "endpoint ino", "service gid", "connect gid", "private gid", "caller dev", "caller ino", "caller dir", "caller owner", "caller mode", "caller symlink"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			stored := 0
			switch kind {
			case "endpoint dev":
				e.doc["endpoint_dev"] = uint64(e.doc["endpoint_dev"].(uint64)) + 1
				stored = 1
			case "endpoint ino":
				e.doc["endpoint_ino"] = e.doc["endpoint_ino"].(uint64) + 1
				stored = 1
			case "service gid":
				e.doc["service_gid"] = uint32(4294967293)
				stored = 1
			case "connect gid":
				e.doc["connect_gid"] = uint32(4294967293)
			case "private gid":
				e.doc["service_gid"] = os.Getegid()
				e.doc["connect_gid"] = uint32(4294967293)
			case "caller dev":
				p := e.doc["caller"].(map[string]any)
				p["dev"] = p["dev"].(uint64) + 1
			case "caller ino":
				p := e.doc["caller"].(map[string]any)
				p["ino"] = p["ino"].(uint64) + 1
			case "caller dir":
				other := filepath.Join(e.root, "other")
				must(t, os.Mkdir(other, 0700))
				e.doc["caller"].(map[string]any)["dir"] = other
			case "caller owner":
				e.changeStat(func(name string, st *syscall.Stat_t) {
					if name == e.callerDir {
						st.Uid++
					}
				})
			case "caller mode":
				must(t, os.Chmod(e.callerDir, 0755))
			case "caller symlink":
				must(t, os.Rename(e.callerDir, e.callerDir+".real"))
				must(t, os.Symlink(e.callerDir+".real", e.callerDir))
			}
			e.writeConfig()
			code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
			if kind == "control" {
				if code != ExitAccepted {
					t.Fatal("control", code, stderr)
				}
				return
			}
			if code != ExitLocal || stdout != "" || e.storedCount() != stored || len(l.drain()) != 0 {
				t.Fatalf("configuration pin: exit %d stdout %q stderr %q stored %d", code, stdout, stderr, e.storedCount())
			}
			if stored == 1 && !strings.Contains(stderr, "stored, not sent") {
				t.Fatal("missing stored diagnostic", stderr)
			}
		})
	}
}

// Claim: config ownership/group/link and ancestor protections are mandatory, beside a root-owned pinned 0444 snapshot control.
func TestProtectedConfigurationSnapshot(t *testing.T) {
	for _, kind := range []string{"control", "owner", "group", "links", "ancestor owner", "ancestor writable", "ancestor symlink", "replacement"} {
		t.Run(kind, func(t *testing.T) {
			e := newEnv(t)
			l := e.listen(modeAccept)
			switch kind {
			case "owner":
				e.changeStat(func(name string, st *syscall.Stat_t) {
					if name == e.cfgPath {
						st.Uid = e.policy.Association.UID
					}
				})
			case "group":
				e.changeStat(func(name string, st *syscall.Stat_t) {
					if name == e.cfgPath {
						st.Gid = 1
					}
				})
			case "links":
				must(t, os.Link(e.cfgPath, e.cfgPath+".alias"))
			case "ancestor owner":
				e.changeStat(func(name string, st *syscall.Stat_t) {
					if name == e.root {
						st.Uid = e.policy.Association.UID
					}
				})
			case "ancestor writable":
				must(t, os.Chmod(e.root, 0720))
			case "ancestor symlink":
				link := filepath.Join(e.root, "link")
				must(t, os.Symlink(e.root, link))
				e.cfgPath = filepath.Join(link, "caller.json")
			case "replacement":
				native := e.host.stat
				reads := 0
				e.host.stat = func(fd int) (syscall.Stat_t, error) {
					st, err := native(fd)
					name, _ := os.Readlink("/proc/self/fd/" + fdString(fd))
					if name == e.cfgPath {
						reads++
						if reads == 2 {
							b, err := os.ReadFile(name)
							must(t, err)
							must(t, os.Rename(name, name+".old"))
							must(t, os.WriteFile(name, b, 0444))
						}
					}
					return st, err
				}
			}
			code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
			if kind == "control" {
				if code != ExitAccepted {
					t.Fatal("control", code, stderr)
				}
				return
			}
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonConfig) || e.storedCount() != 0 || len(l.drain()) != 0 {
				t.Fatalf("config protection: exit %d stdout %q stderr %q", code, stdout, stderr)
			}
		})
	}
}

// Claim: replacing the caller pin after load but before connect refuses without a connection, beside an unchanged-pin exchange.
func TestCallerPinRecheckedBeforeConnect(t *testing.T) {
	e := newEnv(t)
	l := e.listen(modeAccept)
	c := e.config()
	id := e.dispatchID
	wire, reason, err := SignRequest(c, e.plan(m0.OpDispatch, id, ""))
	if reason != "" || err != nil {
		t.Fatal(reason, err)
	}
	if o := Send(c, id, wire, false); o.Exit != ExitAccepted {
		t.Fatal("control", o)
	}
	must(t, os.Rename(e.callerDir, e.callerDir+".old"))
	must(t, os.Mkdir(e.callerDir, 0700))
	if o := Send(c, id, wire, false); o.Exit != ExitLocal || o.Sent != 0 || len(l.drain()) != 1 {
		t.Fatal("replaced caller pin accepted", o)
	}
}
