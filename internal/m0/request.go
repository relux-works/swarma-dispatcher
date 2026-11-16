package m0

import (
	"crypto/ed25519"
	"crypto/sha256"
)

const (
	// RequestKind is the closed request discriminator.
	RequestKind = "swarma-dispatcher m0-request/1"
	// SigningDomain precedes one LF and CCJ-1(R) in the preimage. No final LF.
	SigningDomain = "swarma-dispatcher request/1"
	// MaxDispatchPreimage and MaxCancelPreimage are the closed-form maxima of
	// section 2 (28-byte domain and LF included); MaxGenericPreimage is the
	// separate generic signer bound that these maxima never widen to.
	MaxDispatchPreimage = 964
	MaxCancelPreimage   = 1019
	MaxGenericPreimage  = 24576
	// MaxRequestWire bounds the pre-canonical wire body of one request before
	// any allocation. The canonical signed form is far smaller; the slack only
	// admits whitespace and member order variation. Implementation bound.
	MaxRequestWire = 8192
)

// Op is one of the two admitted operations.
type Op string

const (
	OpDispatch Op = "dispatch"
	OpCancel   Op = "cancel"
)

type Association struct {
	ID                 string
	Revision           uint64
	UID                uint32
	Generation         string
	Key                string
	RegistrationNumber uint64
	MasterEpoch        uint64
}

type Worker struct {
	UID        uint32
	Generation string
}

type Plan struct {
	WorkerPlan  string
	Profile     string
	Harness     string
	ExecProfile string
}

type PolicyPin struct {
	ID       string
	Revision uint64
}

type Auth struct {
	Key string
	Sig string
}

// Request is one decoded closed request R. Canonical is CCJ-1(R) without auth;
// Preimage is the exact signed bytes; Digest is SHA-256 of Canonical.
type Request struct {
	RequestID         string
	Receiver          string
	Policy            PolicyPin
	Association       Association
	Worker            Worker
	Plan              Plan
	Op                Op
	DispatchRequestID string
	Auth              Auth
	Canonical         []byte
	Preimage          []byte
	Digest            [32]byte
}

// Principal is the replay/ownership principal: caller association ID and the
// caller generation it was authenticated under.
func (r *Request) Principal() string { return r.Association.ID + ":" + r.Association.Generation }

// DecodeRequest strictly decodes one signed wire request. Byte bounds are
// checked before allocation; unknown, missing, cross-operation and nested
// extra members refuse; every spelling and range follows section 2.
func DecodeRequest(wire []byte) (*Request, error) {
	v, _, err := canonicalize(wire, requestLimits)
	if err != nil {
		return nil, err
	}
	return decodeSigned(v)
}

func decodeSigned(v *value) (*Request, error) {
	if v == nil || v.kind != kindObject {
		return nil, invalid("request is not an object")
	}
	auth := v.member("auth")
	if auth == nil {
		return nil, invalid("auth required")
	}
	r, err := decodeUnsigned(v.without("auth"))
	if err != nil {
		return nil, err
	}
	if err := auth.exactly("key", "sig"); err != nil {
		return nil, err
	}
	key, err := keyMember(auth, "key")
	if err != nil {
		return nil, err
	}
	sig, ok := auth.member("sig").text()
	if !ok {
		return nil, invalid("member spelling: sig")
	}
	if _, ok := decodeSignature(sig); !ok {
		return nil, invalid("member spelling: sig")
	}
	r.Auth = Auth{Key: key, Sig: sig}
	return r, nil
}

// decodeUnsigned decodes R without auth and fixes Canonical, Preimage and Digest.
func decodeUnsigned(v *value) (*Request, error) {
	if v == nil || v.kind != kindObject {
		return nil, invalid("request is not an object")
	}
	op, ok := v.member("op").text()
	if !ok {
		return nil, invalid("member spelling: op")
	}
	keys := []string{"association", "kind", "op", "plan", "policy", "receiver", "request_id", "v", "worker"}
	switch Op(op) {
	case OpDispatch:
	case OpCancel:
		keys = append(keys, "dispatch_request_id")
	default:
		return nil, invalid("unsupported operation")
	}
	if err := v.exactly(keys...); err != nil {
		return nil, err
	}
	if err := versionMember(v); err != nil {
		return nil, err
	}
	if err := constMember(v, "kind", RequestKind); err != nil {
		return nil, err
	}
	r := &Request{Op: Op(op)}
	var err error
	if r.RequestID, err = idMember(v, "request_id"); err != nil {
		return nil, err
	}
	if r.Receiver, err = idMember(v, "receiver"); err != nil {
		return nil, err
	}
	if r.Policy, err = decodePolicyPin(v.member("policy")); err != nil {
		return nil, err
	}
	if r.Association, err = decodeAssociation(v.member("association")); err != nil {
		return nil, err
	}
	if r.Worker, err = decodeWorker(v.member("worker")); err != nil {
		return nil, err
	}
	if r.Plan, err = decodePlan(v.member("plan")); err != nil {
		return nil, err
	}
	if r.Op == OpCancel {
		if r.DispatchRequestID, err = idMember(v, "dispatch_request_id"); err != nil {
			return nil, err
		}
	}
	r.Canonical = v.encode(nil)
	r.Preimage = append(append([]byte(SigningDomain), '\n'), r.Canonical...)
	bound := MaxDispatchPreimage
	if r.Op == OpCancel {
		bound = MaxCancelPreimage
	}
	if len(r.Preimage) > bound || len(r.Preimage) > MaxGenericPreimage {
		return nil, refuse(CodeRequestTooLarge, "preimage exceeds the closed-form maximum")
	}
	r.Digest = sha256.Sum256(r.Canonical)
	return r, nil
}

func decodePolicyPin(v *value) (PolicyPin, error) {
	var p PolicyPin
	if err := v.exactly("id", "revision"); err != nil {
		return p, err
	}
	var err error
	if p.ID, err = idMember(v, "id"); err != nil {
		return p, err
	}
	if p.Revision, err = serialMember(v, "revision"); err != nil {
		return p, err
	}
	return p, nil
}

func decodeAssociation(v *value) (Association, error) {
	var a Association
	if err := v.exactly("generation", "id", "key", "master_epoch", "registration_number", "revision", "uid"); err != nil {
		return a, err
	}
	var err error
	if a.ID, err = idMember(v, "id"); err != nil {
		return a, err
	}
	if a.Revision, err = serialMember(v, "revision"); err != nil {
		return a, err
	}
	if a.UID, err = uidMember(v, "uid"); err != nil {
		return a, err
	}
	if a.Generation, err = idMember(v, "generation"); err != nil {
		return a, err
	}
	if a.Key, err = keyMember(v, "key"); err != nil {
		return a, err
	}
	if a.RegistrationNumber, err = serialMember(v, "registration_number"); err != nil {
		return a, err
	}
	if a.MasterEpoch, err = serialMember(v, "master_epoch"); err != nil {
		return a, err
	}
	return a, nil
}

func decodeWorker(v *value) (Worker, error) {
	var w Worker
	if err := v.exactly("generation", "uid"); err != nil {
		return w, err
	}
	var err error
	if w.UID, err = uidMember(v, "uid"); err != nil {
		return w, err
	}
	if w.Generation, err = idMember(v, "generation"); err != nil {
		return w, err
	}
	return w, nil
}

func decodePlan(v *value) (Plan, error) {
	var p Plan
	if err := v.exactly("exec_profile", "harness", "profile", "worker_plan"); err != nil {
		return p, err
	}
	var err error
	if p.WorkerPlan, err = digestMember(v, "worker_plan"); err != nil {
		return p, err
	}
	if p.Profile, err = digestMember(v, "profile"); err != nil {
		return p, err
	}
	if p.Harness, err = digestMember(v, "harness"); err != nil {
		return p, err
	}
	if p.ExecProfile, err = digestMember(v, "exec_profile"); err != nil {
		return p, err
	}
	return p, nil
}

// Verify checks that auth.key is the association key and that auth.sig is a
// valid Ed25519 signature of the exact preimage under that key. Shape
// validity never substitutes for this check; a schema-valid placeholder
// signature refuses.
func (r *Request) Verify() error {
	if r.Auth.Key == "" || r.Auth.Sig == "" {
		return refuse(CodeSignatureInvalid, "unsigned request")
	}
	if r.Auth.Key != r.Association.Key {
		return refuse(CodeKeyNotAssociated, "auth key is not the association key")
	}
	pub, ok := decodeKey(r.Auth.Key)
	if !ok || !canonicalPointEncoding(pub) {
		return refuse(CodeSignatureInvalid, "key encoding")
	}
	sig, ok := decodeSignature(r.Auth.Sig)
	if !ok {
		return refuse(CodeSignatureInvalid, "signature encoding")
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), r.Preimage, sig) {
		return refuse(CodeSignatureInvalid, "signature does not verify over the preimage")
	}
	return nil
}

// pinsEqual reports whether two requests carry identical receiver, policy,
// association, worker and plan pins. Cancel requires equality with the
// original dispatch record.
func pinsEqual(a, b *Request) bool {
	return a.Receiver == b.Receiver && a.Policy == b.Policy && a.Association == b.Association && a.Worker == b.Worker && a.Plan == b.Plan
}
