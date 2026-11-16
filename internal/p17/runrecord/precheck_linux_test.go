//go:build linux && (amd64 || arm64)

package runrecord_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"
)

// Claim: Initialize rejects setup defects before Create without creating any file, beside a valid no-write initialization.
func TestInitializeValidatesWithoutWrites(t *testing.T) {
	for _, mode := range []string{"valid", "journal", "directory", "records", "alias", "budget", "umask", "attestation"} {
		t.Run(mode, func(t *testing.T) {
			c := cfg(t)
			switch mode {
			case "journal":
				if err := os.WriteFile(filepath.Join(c.Directory, "journal"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Chmod(c.Directory, 0755); err != nil {
					t.Fatal(err)
				}
			case "records":
				c.Records = "missing"
			case "alias":
				c.Records = c.Inputs
			case "budget":
				c.ConsumerBudget = 0
			case "umask":
				m := uint32(0600)
				c.DeclaredUmask = &m
			case "attestation":
				c.SupportedLocalStorage = false
			}
			before, err := os.ReadDir(c.Directory)
			must(t, err)
			st, err := runrecord.Initialize(c)
			if mode == "valid" {
				must(t, err)
				must(t, st.Close())
			} else if !errors.Is(err, runrecord.ErrConfig) {
				t.Fatalf("setup not refused: %v", err)
			}
			after, err := os.ReadDir(c.Directory)
			must(t, err)
			if len(after) != len(before) {
				t.Fatalf("Initialize created files: %v -> %v", before, after)
			}
		})
	}
	// A journal-backed successful Create remains the storage control.
	c := cfg(t)
	st, err := runrecord.Initialize(c)
	must(t, err)
	defer st.Close()
	must(t, st.Create(base("control")))
}
