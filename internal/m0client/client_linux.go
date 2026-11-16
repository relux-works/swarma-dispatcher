//go:build linux

package m0client

import (
	"errors"
	"io"
	"syscall"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

func specOf(c *Config) endpointSpec {
	return endpointSpec{dir: c.EndpointDir, name: c.Endpoint, owner: c.ServiceUID, peer: c.ServiceUID, gid: c.ConnectGID, stage: c.StageTimeout, session: c.SessionTimeout}
}

// SignRequest builds one request, signs its preimage with the configured key
// file and returns the exact wire bytes. On refusal reason is the fixed word
// key_file_unsafe or key_not_associated and nothing has been stored or sent.
func SignRequest(c *Config, op m0.Op, requestID, dispatchRequestID string) (wire []byte, reason string, err error) {
	r, err := BuildUnsigned(c, op, requestID, dispatchRequestID)
	if err != nil {
		return nil, reasonConfig, err
	}
	sig, reason, err := signPreimage(c, r.Preimage)
	if reason != "" {
		return nil, reason, err
	}
	wire, err = m0.SignedWire(r.Canonical, c.Association.Key, sig)
	if err != nil {
		return nil, reasonConfig, err
	}
	return wire, "", nil
}

// Send performs one transport exchange for wire bytes whose request_id is id
// and classifies the result. It never signs and never retries.
func Send(c *Config, id string, wire []byte, stored bool) Outcome {
	reply, sent, stage, err := exchange(specOf(c), id, wire)
	return classify(id, stored, sent, reply, stage, err)
}

// submit is dispatch or cancel: configuration, state directory, key file and
// signature, durable store of the wire bytes, then one exchange.
func submit(configPath string, op m0.Op, dispatchRequestID string, random io.Reader) Outcome {
	c, err := loadConfig(configPath)
	if err != nil {
		return local(reasonConfig, err)
	}
	if op == m0.OpCancel && !ValidRequestID(dispatchRequestID) {
		return local("request_id_invalid", errors.New("--dispatch-request-id must be 32 lowercase hex characters"))
	}
	dir, err := openStateDir(c.StateDir)
	if err != nil {
		return local(reasonState, err)
	}
	defer syscall.Close(dir)
	id, err := NewRequestID(random)
	if err != nil {
		return local("random_source_failed", err)
	}
	wire, reason, err := SignRequest(c, op, id, dispatchRequestID)
	if reason != "" {
		return local(reason, err)
	}
	if err := storeRequest(dir, id, wire); err != nil {
		return local(reasonStore, err)
	}
	return Send(c, id, wire, true)
}

// resend sends the stored bytes of one earlier request unchanged. It reads no
// key file and signs nothing; a missing, unsafe or altered file refuses.
func resend(configPath, id string) Outcome {
	c, err := loadConfig(configPath)
	if err != nil {
		return local(reasonConfig, err)
	}
	if !ValidRequestID(id) {
		return local("request_id_invalid", errors.New("--request-id must be 32 lowercase hex characters"))
	}
	dir, err := openStateDir(c.StateDir)
	if err != nil {
		return local(reasonState, err)
	}
	defer syscall.Close(dir)
	wire, err := loadRequest(dir, id)
	if err != nil {
		return local(reasonStoredRequest, err)
	}
	if err := checkStored(c, id, wire); err != nil {
		return local(reasonStoredRequest, err)
	}
	return Send(c, id, wire, true)
}
