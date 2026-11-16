package m0client

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

type policyDoc struct {
	ID       string `json:"id"`
	Revision uint64 `json:"revision"`
}

type associationDoc struct {
	ID                 string `json:"id"`
	Revision           uint64 `json:"revision"`
	UID                uint32 `json:"uid"`
	Generation         string `json:"generation"`
	Key                string `json:"key"`
	RegistrationNumber uint64 `json:"registration_number"`
	MasterEpoch        uint64 `json:"master_epoch"`
}

type workerDoc struct {
	UID        uint32 `json:"uid"`
	Generation string `json:"generation"`
}

type planDoc struct {
	WorkerPlan  string `json:"worker_plan"`
	Profile     string `json:"profile"`
	Harness     string `json:"harness"`
	ExecProfile string `json:"exec_profile"`
}

type requestDoc struct {
	V                 int            `json:"v"`
	Kind              string         `json:"kind"`
	RequestID         string         `json:"request_id"`
	Receiver          string         `json:"receiver"`
	Policy            policyDoc      `json:"policy"`
	Association       associationDoc `json:"association"`
	Worker            workerDoc      `json:"worker"`
	Plan              planDoc        `json:"plan"`
	Op                string         `json:"op"`
	DispatchRequestID string         `json:"dispatch_request_id,omitempty"`
}

// unsignedDocument spells one unsigned request R from the configuration. The
// spelling is an input only: the bytes that are signed and sent are produced
// from it by the daemon's canonicalizer.
func unsignedDocument(c *Config, op m0.Op, requestID, dispatchRequestID string) ([]byte, error) {
	if (op == m0.OpCancel) != (dispatchRequestID != "") {
		return nil, errors.New("dispatch_request_id belongs to cancel only")
	}
	return json.Marshal(requestDoc{
		V: 1, Kind: m0.RequestKind, RequestID: requestID, Receiver: c.Receiver,
		Policy:      policyDoc{ID: c.Policy.ID, Revision: c.Policy.Revision},
		Association: associationDoc{ID: c.Association.ID, Revision: c.Association.Revision, UID: c.Association.UID, Generation: c.Association.Generation, Key: c.Association.Key, RegistrationNumber: c.Association.RegistrationNumber, MasterEpoch: c.Association.MasterEpoch},
		Worker:      workerDoc{UID: c.Worker.UID, Generation: c.Worker.Generation},
		Plan:        planDoc{WorkerPlan: c.Plan.WorkerPlan, Profile: c.Plan.Profile, Harness: c.Plan.Harness, ExecProfile: c.Plan.ExecProfile},
		Op:          string(op), DispatchRequestID: dispatchRequestID,
	})
}

// NewRequestID draws 128 bits from the given source and spells them as 32
// lowercase hex characters.
func NewRequestID(random io.Reader) (string, error) {
	var b [16]byte
	if _, err := io.ReadFull(random, b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ValidRequestID reports whether s is 32 lowercase hex characters.
func ValidRequestID(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !(s[i] >= '0' && s[i] <= '9' || s[i] >= 'a' && s[i] <= 'f') {
			return false
		}
	}
	return true
}

// BuildUnsigned returns the decoded unsigned request R for one operation. Its
// Canonical and Preimage members are the daemon canonicalizer's bytes.
func BuildUnsigned(c *Config, op m0.Op, requestID, dispatchRequestID string) (*m0.Request, error) {
	if !ValidRequestID(requestID) || (op == m0.OpCancel && !ValidRequestID(dispatchRequestID)) {
		return nil, errors.New("request ID is not 32 lowercase hex characters")
	}
	doc, err := unsignedDocument(c, op, requestID, dispatchRequestID)
	if err != nil {
		return nil, err
	}
	r, err := m0.DecodeUnsigned(doc)
	if err != nil {
		return nil, err
	}
	if !pinsMatch(c, r) || r.Op != op || r.RequestID != requestID || r.DispatchRequestID != dispatchRequestID {
		return nil, errors.New("built request differs from the configuration")
	}
	return r, nil
}

// pinsMatch reports whether a decoded request carries exactly the configured
// receiver, policy, association, worker and plan.
func pinsMatch(c *Config, r *m0.Request) bool {
	return r.Receiver == c.Receiver && r.Policy == c.Policy && r.Association == c.Association && r.Worker == c.Worker && r.Plan == c.Plan
}

// checkStored validates stored wire bytes for resend without any key
// material: they must be the exact canonical signed form of a request with
// the named ID and the configured pins whose signature verifies under the
// association key. Any altered byte fails one of these checks.
func checkStored(c *Config, requestID string, wire []byte) error {
	r, err := m0.DecodeRequest(wire)
	if err != nil {
		return err
	}
	if r.RequestID != requestID || !pinsMatch(c, r) {
		return errors.New("stored request differs from its name or the configuration")
	}
	again, err := m0.SignedWire(r.Canonical, r.Auth.Key, r.Auth.Sig)
	if err != nil {
		return err
	}
	if string(again) != string(wire) {
		return errors.New("stored request is not the canonical wire form")
	}
	return r.Verify()
}
