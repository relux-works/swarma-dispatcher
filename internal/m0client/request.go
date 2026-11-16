package m0client

import (
	"bytes"
	"errors"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

// ValidRequestID reports whether s is 32 lowercase hex characters.
func ValidRequestID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := range len(s) {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// checkStored requires the exact canonical signed wire form and the installed
// operation plan's canonical R. Initial signing and resend share this check;
// resend never reconstructs or signs a request.
func checkStored(plan *m0.SigningPlan, requestID string, wire []byte) error {
	r, err := m0.DecodeRequest(wire)
	if err != nil {
		return err
	}
	if r.RequestID != requestID || !bytes.Equal(r.Canonical, plan.Request.Canonical) {
		return errors.New("stored request differs from its name or the installed signing plan")
	}
	again, err := m0.SignedWire(r.Canonical, r.Auth.Key, r.Auth.Sig)
	if err != nil {
		return err
	}
	if !bytes.Equal(again, wire) {
		return errors.New("stored request is not the canonical wire form")
	}
	return r.Verify()
}
