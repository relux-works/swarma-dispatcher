//go:build linux

package keymaker

import (
	"context"
	"os"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

// Claim: direct absent-socket and refused-connect Dial failures are typed unavailable/exit 1; each unchanged socket is a successful zero-write control.
func TestDialTransportTyped(t *testing.T) {
	for _, mode := range []string{"absent", "refused"} {
		t.Run(mode, func(t *testing.T) {
			for _, bad := range []bool{true, false} {
				d, ln := socketFixture(t)
				if bad && mode == "absent" {
					if err := os.Remove(d.Path); err != nil {
						t.Fatal(err)
					}
				}
				if bad && mode == "refused" {
					if err := ln.Close(); err != nil {
						t.Fatal(err)
					}
				}
				c, err := d.Dial(context.Background())
				if c != nil {
					_ = c.Close()
				}
				if !bad {
					if err != nil {
						t.Fatalf("valid neighbour: %v", err)
					}
					continue
				}
				if c != nil || !codes.Is(err, codes.BindUnavailable) {
					t.Fatalf("direct transport type: %v", err)
				}
				verdict, exitErr := codes.Exit(err, 0, false)
				if exitErr != nil || verdict.Status != 1 {
					t.Fatalf("transport verdict: %+v %v", verdict, exitErr)
				}
			}
		})
	}
}
