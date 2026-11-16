//go:build linux

package m0client

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// keyDirectoryEnv gives the seed a dedicated ancestor, so unsafe key paths
// cannot fail earlier through the configuration or transport directory checks.
func keyDirectoryEnv(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	ancestor := filepath.Join(e.root, "vault")
	must(t, os.Mkdir(ancestor, 0700))
	parent := filepath.Join(ancestor, "keys")
	must(t, os.Rename(filepath.Dir(e.keyPath), parent))
	e.keyPath = filepath.Join(parent, "seed")
	e.doc["key_file"] = e.keyPath
	e.writeConfig()
	e.changeStat(func(name string, st *syscall.Stat_t) {
		if name == ancestor {
			st.Uid = e.policy.Association.UID
		}
	})
	return e, ancestor
}

// Claim: unsafe key parents or ancestors refuse locally without store/connect; restoring a caller-owned 0700 parent permits one runtime launch in each case.
func TestKeyDirectoryProtection(t *testing.T) {
	for _, kind := range []string{
		"parent 0750", "parent 0755", "parent setgid", "parent sticky", "foreign parent", "root parent",
		"symlink parent", "symlink ancestor", "group-writable ancestor",
		"other-writable ancestor", "foreign ancestor",
	} {
		t.Run(kind, func(t *testing.T) {
			e, ancestor := keyDirectoryEnv(t)
			b := e.newBackend(e.dispatchID, e.cancelID)
			l := e.serveBackend(b)
			parent := filepath.Dir(e.keyPath)
			originalHost := e.host
			switch kind {
			case "parent 0750":
				must(t, os.Chmod(parent, 0750))
			case "parent 0755":
				must(t, os.Chmod(parent, 0755))
			case "parent setgid":
				must(t, syscall.Chmod(parent, 02700))
			case "parent sticky":
				must(t, syscall.Chmod(parent, 01700))
			case "foreign parent", "root parent":
				e.changeStat(func(name string, st *syscall.Stat_t) {
					if name == parent {
						st.Uid = e.policy.Association.UID + 1
						if kind == "root parent" {
							st.Uid = 0
						}
					}
				})
			case "symlink parent":
				link := filepath.Join(ancestor, "key-link")
				must(t, os.Symlink(parent, link))
				e.doc["key_file"] = filepath.Join(link, "seed")
				e.writeConfig()
			case "symlink ancestor":
				link := filepath.Join(e.root, "vault-link")
				must(t, os.Symlink(ancestor, link))
				e.doc["key_file"] = filepath.Join(link, "keys", "seed")
				e.writeConfig()
			case "group-writable ancestor":
				must(t, os.Chmod(ancestor, 0720))
			case "other-writable ancestor":
				must(t, os.Chmod(ancestor, 0702))
			case "foreign ancestor":
				e.changeStat(func(name string, st *syscall.Stat_t) {
					if name == ancestor {
						st.Uid = e.policy.Association.UID + 1
					}
				})
			}
			code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonKeyFileUnsafe) || e.storedCount() != 0 || len(l.drain()) != 0 {
				t.Fatalf("unsafe key directory: exit %d stdout %q stderr %q", code, stdout, stderr)
			}
			if starts, _ := b.launcher.count(); starts != 0 {
				t.Fatal("unsafe key directory launched", starts)
			}

			e.host = originalHost
			must(t, os.Chmod(parent, 0700))
			must(t, os.Chmod(ancestor, 0700))
			e.doc["key_file"] = e.keyPath
			e.writeConfig()
			code, stdout, stderr = e.run("dispatch", "--config", e.cfgPath)
			starts, _ := b.launcher.count()
			if code != ExitAccepted || !strings.Contains(stdout, `"state":"started"`) || stderr != "" || starts != 1 || e.storedCount() != 1 || len(l.drain()) != 1 {
				t.Fatalf("safe key directory control: exit %d stdout %q stderr %q starts %d", code, stdout, stderr, starts)
			}
		})
	}
}

// Claim: persistent key parent or grandparent replacement returns 64 without store/connect; restoring each original directory permits one runtime launch.
func TestKeyDirectoryReplacement(t *testing.T) {
	for _, component := range []string{"parent", "grandparent"} {
		t.Run(component, func(t *testing.T) {
			e, ancestor := keyDirectoryEnv(t)
			b := e.newBackend(e.dispatchID, e.cancelID)
			l := e.serveBackend(b)
			name := filepath.Dir(e.keyPath)
			if component == "grandparent" {
				name = ancestor
			}
			moved := name + ".original"
			originalHost := e.host
			replaced, readBeforeRecheck := false, false
			seedFD := -1
			e.host.stat = func(fd int) (syscall.Stat_t, error) {
				st, err := originalHost.fstat(fd)
				if err != nil {
					return st, err
				}
				actual, err := os.Readlink("/proc/self/fd/" + fdString(fd))
				if err != nil {
					return st, err
				}
				if replaced {
					// Renaming a directory does not change the ownership of its
					// descendants. Preserve the original ownership model there.
					alias := name + strings.TrimPrefix(actual, moved)
					if strings.HasPrefix(actual, moved+"/") && (alias == e.keyPath || alias == filepath.Dir(e.keyPath)) || actual == moved {
						st.Uid = e.policy.Association.UID
						if alias == e.keyPath || alias == filepath.Dir(e.keyPath) {
							raw, err := fstat(fd)
							must(t, err)
							st.Gid = raw.Gid
						}
					}
					// Inspect only the offset, never seed contents.
					offset, err := syscall.Seek(seedFD, 0, 1)
					readBeforeRecheck = readBeforeRecheck || err == nil && offset == seedBytes
				} else if actual == e.keyPath {
					seedFD = fd
					must(t, os.Rename(name, moved))
					must(t, os.Mkdir(name, 0700))
					replaced = true
				}
				return st, nil
			}
			code, stdout, stderr := e.run("dispatch", "--config", e.cfgPath)
			if code != ExitLocal || stdout != "" || !strings.Contains(stderr, reasonKeyFileUnsafe) || !replaced || !readBeforeRecheck || e.storedCount() != 0 || len(l.drain()) != 0 {
				t.Fatalf("replaced key directory: exit %d stdout %q stderr %q replaced %v read-before-recheck %v", code, stdout, stderr, replaced, readBeforeRecheck)
			}
			if starts, _ := b.launcher.count(); starts != 0 {
				t.Fatal("replaced key directory launched", starts)
			}
			e.host = originalHost
			must(t, os.Remove(name))
			must(t, os.Rename(moved, name))
			code, stdout, stderr = e.run("dispatch", "--config", e.cfgPath)
			starts, _ := b.launcher.count()
			if code != ExitAccepted || !strings.Contains(stdout, `"state":"started"`) || stderr != "" || starts != 1 || e.storedCount() != 1 || len(l.drain()) != 1 {
				t.Fatalf("restored key directory control: exit %d stdout %q stderr %q starts %d", code, stdout, stderr, starts)
			}
		})
	}
}
