//go:build linux && (amd64 || arm64)

package m0hook

import (
	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"testing"
)

// Claim: runtime-shaped cancel, removal, expiry, combined and unknown-time stop metadata validate against the original bounds; later ceilings and wrong expiry are refused beside them.
func TestStopMetadataBounds(t *testing.T) {
	start := m0.StartRequest{EffectID: "start", ExecutionID: "execution", Worker: m0.Worker{UID: 30001, Generation: "cccccccccccccccccccccccccccccccc"}, Deadlines: m0.Deadlines{Expiry: 1700000200, DetectSeconds: 5, StopSeconds: 10}}
	for _, tc := range []struct {
		name        string
		obligations []m0.StopObligation
		ceiling     int64
		valid       bool
	}{
		{"cancel", []m0.StopObligation{{Source: "cancel", Event: 1700000100, Detection: 1700000101}}, 1700000111, true},
		{"removal", []m0.StopObligation{{Source: "removal", Event: 1700000100, Detection: 1700000101}}, 1700000111, true},
		{"expiry", []m0.StopObligation{{Source: "expiry", Event: 1700000200, Detection: 1700000201}}, 1700000211, true},
		{"combined", []m0.StopObligation{{Source: "cancel", Event: 1700000100, Detection: 1700000101}, {Source: "expiry", Event: 1700000200, Detection: 1700000201}}, 1700000111, true},
		{"unknown-time", []m0.StopObligation{{Source: "cancel", Event: 0, Detection: 0}}, 0, true},
		{"later-ceiling", []m0.StopObligation{{Source: "cancel", Event: 1700000100, Detection: 1700000101}}, 1700000115, false},
		{"wrong-expiry", []m0.StopObligation{{Source: "expiry", Event: 1700000199, Detection: 1700000201}}, 1700000211, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := tc.obligations[0]
			stop := m0.StopRequest{EffectID: "stop", ExecutionID: "execution", Worker: m0.Worker{UID: 30001, Generation: "cccccccccccccccccccccccccccccccc"}, Event: first.Event, Detection: first.Detection, Obligations: tc.obligations, Ceiling: tc.ceiling}
			if got := validStop(start, stop); got != tc.valid {
				t.Fatalf("stop metadata bounds: accepted=%v want=%v", got, tc.valid)
			}
		})
	}
}
