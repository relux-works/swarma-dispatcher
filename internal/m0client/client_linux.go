//go:build linux

package m0client

import (
	"errors"
	"syscall"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
)

func specOf(c *Config) endpointSpec {
	return endpointSpec{
		dir: c.host.endpointPath(), name: endpointName,
		owner: c.ServiceUID, peer: c.ServiceUID,
		gid: c.ConnectGID, serviceGID: c.ServiceGID,
		dev: c.EndpointDev, ino: c.EndpointIno,
		caller: c.Caller, host: c.host,
		stage: c.StageTimeout, session: c.SessionTimeout,
	}
}

func (c *Config) checkCaller(r *m0.Request) error {
	if c.host.effectiveUID() != r.Association.UID || c.host.effectiveUID() != c.Caller.UID || c.host.realUID() != c.Caller.UID {
		return errors.New("process UID must equal caller.uid and plan association.uid")
	}
	return nil
}

// SignRequest signs exactly the installed plan's R, with no request generation.
// Caller identity is checked before the key file can be opened.
func SignRequest(c *Config, plan *m0.SigningPlan) (wire []byte, reason string, err error) {
	r := plan.Request
	if err := c.checkCaller(r); err != nil {
		return nil, reasonCaller, err
	}
	sig, reason, err := signPreimage(c, r)
	if reason != "" {
		return nil, reason, err
	}
	wire, err = m0.SignedWire(r.Canonical, r.Association.Key, sig)
	if err != nil {
		return nil, reasonPlan, err
	}
	return wire, "", nil
}

// Send performs one transport exchange, without signing or retrying.
func Send(c *Config, id string, wire []byte, stored bool) Outcome {
	reply, sent, stage, err := exchange(specOf(c), id, wire)
	return classify(id, stored, sent, reply, stage, err)
}

// submit reads the selected plan, checks identity, signs, durably stores, then
// exchanges once. O_EXCL is the final arbiter for simultaneous invocations.
func submit(c *Config, op m0.Op) Outcome {
	plan, err := c.loadPlan(op)
	if err != nil {
		return local(reasonPlan, err)
	}
	if err := c.checkCaller(plan.Request); err != nil {
		return local(reasonCaller, err)
	}
	dir, err := c.host.openStateDir(c.StateDir)
	if err != nil {
		return local(reasonState, err)
	}
	defer syscall.Close(dir)
	id := plan.Request.RequestID
	// Refuse an existing entry even when the key is no longer available.
	fd, existsErr := syscall.Openat(dir, requestFile(id), oPath|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if existsErr == nil {
		syscall.Close(fd)
		return existingRequest()
	}
	if !errors.Is(existsErr, syscall.ENOENT) {
		return local(reasonStore, existsErr)
	}
	wire, reason, err := SignRequest(c, plan)
	if reason != "" {
		return local(reason, err)
	}
	if err := storeRequest(dir, id, wire); err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return existingRequest()
		}
		return local(reasonStore, err)
	}
	return Send(c, id, wire, true)
}

func existingRequest() Outcome {
	return local(reasonStore, errors.New("request already stored; use resend --request-id with the installed plan's request_id"))
}

// resend reads no key material: identify the stored operation, load its current
// plan, check UID and exact R/signature, then send the original bytes unchanged.
func resend(c *Config, id string) Outcome {
	if !ValidRequestID(id) {
		return local("request_id_invalid", errors.New("--request-id must be 32 lowercase hex characters"))
	}
	dir, err := c.host.openStateDir(c.StateDir)
	if err != nil {
		return local(reasonState, err)
	}
	defer syscall.Close(dir)
	wire, err := c.host.loadRequest(dir, id)
	if err != nil {
		return local(reasonStoredRequest, err)
	}
	r, err := m0.DecodeRequest(wire)
	if err != nil {
		return local(reasonStoredRequest, err)
	}
	plan, err := c.loadPlan(r.Op)
	if err != nil {
		return local(reasonPlan, err)
	}
	if err := c.checkCaller(plan.Request); err != nil {
		return local(reasonCaller, err)
	}
	if err := checkStored(plan, id, wire); err != nil {
		return local(reasonStoredRequest, err)
	}
	return Send(c, id, wire, true)
}
