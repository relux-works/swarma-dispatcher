package source

import (
	"context"
	"crypto/rand"
	"encoding/hex"

	"errors"
	"github.com/relux-works/a2a-swarm/runorigin"
	"io"
	"net"
	"sync"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
)

// DefaultBudget is the Source dependency budget of H1 and of the validate
// step (r2.6 H1 step 3: 5 s).
const DefaultBudget = 5 * time.Second

// Pins is the operator-pinned downstream service configuration for Source.
type Pins struct {
	// Root is the protected trust anchor above ControlDir (normally "/").
	Root string
	// ControlDir is SourceControlDir.
	ControlDir string
	// SourceUID is the uid of the pinned A.
	SourceUID uint32
	// SourceGeneration is the pinned authoritative generation of A.
	SourceGeneration string
}

// Client is the Source client. Every field except Now is required; a nil
// Wire or Generations is the refusing default.
type Client struct {
	Pins        Pins
	Dialer      Dialer
	Wire        Wire
	Generations Generations
	// Event and InputPrincipal are the defaults Prepare uses. They select a
	// retained event and input, not authority. A caller that serves several
	// runs from one shared Client passes them per call through PrepareFor and
	// never mutates these fields.
	Event          runorigin.EventRef
	InputPrincipal runorigin.Principal
	// Budget caps each Source phase; zero means DefaultBudget. Larger values
	// are clamped to DefaultBudget.
	Budget time.Duration
	Now    func() time.Time
}

func (c *Client) wire() Wire {
	if c.Wire == nil {
		return Unpinned{}
	}
	return c.Wire
}
func (c *Client) generations() Generations {
	if c.Generations == nil {
		return UnpinnedGenerations{}
	}
	return c.Generations
}
func (c *Client) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func (c *Client) deadline(ctx context.Context) time.Time {
	budget := c.Budget
	if budget <= 0 || budget > DefaultBudget {
		budget = DefaultBudget
	}
	d := c.now().Add(budget)
	if cd, ok := ctx.Deadline(); ok && cd.Before(d) {
		d = cd
	}
	return d
}

// Restart tells the caller to restart from H1 with a fresh prepare: the
// validate response was lost or the connection dropped between prepare and
// validate. The old prepared_id is already discarded by the time it returns.
type Restart struct {
	Cause error
}

func (r *Restart) Error() string {
	if r == nil || r.Cause == nil {
		return "source: restart from H1"
	}
	return "source: restart from H1: " + r.Cause.Error()
}
func (r *Restart) Unwrap() error {
	if r == nil {
		return nil
	}
	return r.Cause
}

// IsRestart reports whether err asks for a restart from H1.
func IsRestart(err error) bool {
	var r *Restart
	return errors.As(err, &r) && r != nil
}

// transportLost reports a connection-level failure: EOF, reset, timeout.
func transportLost(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	var oe *net.OpError
	return errors.As(err, &oe)
}

// classifyH1 maps a prepare-phase wire error to a code.
func classifyH1(op string, err error) error {
	switch {
	case errors.Is(err, ErrNotSupported), errors.Is(err, ErrRefused):
		return codes.Refuse(codes.OriginRefused, op, err)
	case errors.Is(err, ErrDependency), transportLost(err):
		return codes.Refuse(codes.OriginUnavailable, op, err)
	case codes.IsUnpinned(err):
		return codes.Refuse(codes.OriginRefused, op, err)
	}
	// Unknown wire error: fail closed as a definite refusal.
	return codes.Refuse(codes.OriginRefused, op+" (unclassified wire error)", err)
}

// Prepared is a live H1 result: one connection, its hello and its prepared
// record. It must be validated exactly once on the same connection.
type Prepared struct {
	mu       sync.Mutex
	client   *Client
	conn     Conn
	peer     Credential
	hello    HelloMessage
	prepared PreparedMessage
	request  PrepareRequest
	expected map[string]string
	consumed bool
	closed   bool
}

// Hello returns the recorded hello of this connection.
func (p *Prepared) Hello() HelloMessage { return p.hello }

// PreparedID returns the prepared record ID of this connection.
func (p *Prepared) PreparedID() string { return p.prepared.PreparedID }

// SourceIncarnation returns the hello incarnation of this connection.
func (p *Prepared) SourceIncarnation() string { return p.hello.Incarnation }

// Peer returns the authenticated kernel peer credential.
func (p *Prepared) Peer() Credential { return p.peer }

// Close drops the connection. A prepared record that was never validated is
// discarded; its prepared_id is never reused.
func (p *Prepared) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closeLocked()
}
func (p *Prepared) closeLocked() error {
	if p.closed {
		return nil
	}
	p.closed = true
	p.consumed = true
	if p.conn == nil {
		return nil
	}
	return p.conn.Close()
}

// Prepare runs H1 with the client's default Event and InputPrincipal.
func (c *Client) Prepare(ctx context.Context, nonce string) (*Prepared, error) {
	return c.PrepareFor(ctx, nonce, c.Event, c.InputPrincipal)
}

// PrepareFor runs H1 for one run's retained event and input principal:
// socket path check, generation check, connect, kernel peer authentication,
// hello, prepare. On any refusal nothing is connected or the connection is
// closed before return. It only reads the Client, so concurrent runs may
// share one Client.
func (c *Client) PrepareFor(ctx context.Context, nonce string, event runorigin.EventRef, input runorigin.Principal) (*Prepared, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, codes.Refuse(codes.OriginUnavailable, "context", err)
	}
	if c.Dialer == nil {
		return nil, codes.Refuse(codes.OriginRefused, "no dialer", nil)
	}
	if nonce == "" {
		return nil, codes.Refuse(codes.OriginRefused, "launch nonce required", nil)
	}
	sock, err := CheckControlSocket(c.Pins.Root, c.Pins.ControlDir, c.Pins.SourceUID)
	if err != nil {
		return nil, codes.Refuse(codes.OriginRefused, "control socket", err)
	}
	gen, err := c.generations().Current(c.Pins.SourceUID)
	if err != nil {
		return nil, codes.Refuse(codes.OriginRefused, "authoritative generation", err)
	}
	if c.Pins.SourceGeneration == "" || gen != c.Pins.SourceGeneration {
		return nil, codes.Refuse(codes.OriginRefused, "generation mismatch", nil)
	}
	deadline := c.deadline(ctx)
	conn, err := c.Dialer.Dial(sock)
	if err != nil {
		return nil, codes.Refuse(codes.OriginUnavailable, "connect", err)
	}
	fail := func(e error) (*Prepared, error) {
		_ = conn.Close()
		return nil, e
	}
	peer, err := conn.Peer()
	if err != nil {
		return fail(codes.Refuse(codes.OriginRefused, "peer credential", err))
	}
	if peer.UID != c.Pins.SourceUID {
		return fail(codes.Refuse(codes.OriginRefused, "peer uid", nil).With("peer_uid", utoa(peer.UID)))
	}
	if err = conn.SetDeadline(deadline); err != nil {
		return fail(codes.Refuse(codes.OriginUnavailable, "deadline", err))
	}
	w := c.wire()
	hello, err := w.Hello(conn)
	if err != nil {
		return fail(classifyH1("hello", err))
	}
	if !hello.Capability {
		return fail(codes.Refuse(codes.OriginRefused, "origin_not_supported", ErrNotSupported))
	}
	if hello.Incarnation == "" {
		return fail(codes.Refuse(codes.OriginRefused, "hello incarnation missing", nil))
	}
	var requestID [16]byte
	if _, err := rand.Read(requestID[:]); err != nil {
		return fail(classifyH1("request ID", err))
	}
	req := PrepareRequest{RequestID: hex.EncodeToString(requestID[:]), LaunchNonce: nonce, Event: event, InputPrincipal: input, SourceIncarnation: hello.Incarnation}
	prepared, err := w.Prepare(conn, req)
	if err != nil {
		return fail(classifyH1("prepare", err))
	}
	if prepared.PreparedID == "" {
		return fail(codes.Refuse(codes.OriginRefused, "prepared_id missing", nil))
	}
	if prepared.Incarnation != hello.Incarnation {
		return fail(codes.Refuse(codes.OriginRefused, "prepare incarnation differs from hello", nil))
	}
	expected := make(map[string]string, len(prepared.Bindings)+2)
	for k, v := range prepared.Bindings {
		expected[k] = v
	}
	if v, ok := expected["source_incarnation"]; ok && v != hello.Incarnation {
		return fail(codes.Refuse(codes.OriginRefused, "binding source_incarnation differs from hello", nil))
	}
	if v, ok := expected["prepared_id"]; ok && v != prepared.PreparedID {
		return fail(codes.Refuse(codes.OriginRefused, "binding prepared_id differs from prepare", nil))
	}
	expected["source_incarnation"] = hello.Incarnation
	expected["prepared_id"] = prepared.PreparedID
	return &Prepared{client: c, conn: conn, peer: peer, hello: hello, prepared: prepared, request: req, expected: expected}, nil
}

// Validated is the H2 validate result that the bind request must carry
// verbatim. Origin is exactly the bytes Source returned.
type Validated struct {
	PreparedID        string
	SourceIncarnation string
	Bindings          map[string]string
	Origin            []byte
	Peer              Credential
}

// Validate runs H2 step 1 on the same live connection. The prepared record is
// consumed once: a second call refuses run_origin_refused. A lost response or
// a dropped connection returns *Restart. Every binding, including
// source_incarnation and prepared_id, is compared byte-equal with this
// connection's hello and prepare. The connection is closed on every path.
func (p *Prepared) Validate(ctx context.Context) (*Validated, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.consumed || p.closed {
		return nil, codes.Refuse(codes.OriginRefused, "prepared record already consumed", nil)
	}
	p.consumed = true
	defer func() { _ = p.closeLocked() }()
	if err := ctx.Err(); err != nil {
		return nil, &Restart{Cause: err}
	}
	if err := p.conn.SetDeadline(p.client.deadline(ctx)); err != nil {
		return nil, &Restart{Cause: err}
	}
	msg, err := p.client.wire().Validate(p.conn, ValidateRequest{RequestID: p.request.RequestID, LaunchNonce: p.request.LaunchNonce, PreparedID: p.prepared.PreparedID, SourceIncarnation: p.hello.Incarnation})
	if err != nil {
		switch {
		case errors.Is(err, ErrNotSupported), errors.Is(err, ErrRefused):
			return nil, codes.Refuse(codes.OriginRefused, "validate", err)
		case errors.Is(err, ErrDependency), transportLost(err):
			return nil, &Restart{Cause: codes.Refuse(codes.OriginUnavailable, "validate", err)}
		case codes.IsUnpinned(err):
			return nil, codes.Refuse(codes.OriginRefused, "validate", err)
		}
		return nil, codes.Refuse(codes.OriginRefused, "validate (unclassified wire error)", err)
	}
	if msg.Incarnation != p.hello.Incarnation {
		return nil, codes.Refuse(codes.OriginRefused, "validate incarnation differs from hello", nil)
	}
	if msg.PreparedID != p.prepared.PreparedID {
		return nil, codes.Refuse(codes.OriginRefused, "validate prepared_id differs from prepare", nil)
	}
	if len(msg.Bindings) != len(p.expected) {
		return nil, codes.Refuse(codes.OriginRefused, "binding set differs", nil)
	}
	for k, want := range p.expected {
		got, ok := msg.Bindings[k]
		if !ok || got != want {
			return nil, codes.Refuse(codes.OriginRefused, "binding differs", nil).With("binding", k)
		}
	}
	if err := CheckOrigin(msg.Origin); err != nil {
		return nil, codes.Refuse(codes.OriginRefused, "origin", err)
	}
	origin := make([]byte, len(msg.Origin))
	copy(origin, msg.Origin)
	bindings := make(map[string]string, len(msg.Bindings))
	for k, v := range msg.Bindings {
		bindings[k] = v
	}
	return &Validated{PreparedID: msg.PreparedID, SourceIncarnation: msg.Incarnation, Bindings: bindings, Origin: origin, Peer: p.peer}, nil
}

func utoa(u uint32) string {
	if u == 0 {
		return "0"
	}
	var b [10]byte
	i := len(b)
	for u > 0 {
		i--
		b[i] = byte('0' + u%10)
		u /= 10
	}
	return string(b[i:])
}
