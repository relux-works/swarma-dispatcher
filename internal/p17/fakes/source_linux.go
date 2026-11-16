//go:build linux

package fakes

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/frame"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
)

// SourceWire is the TEST spelling of the Source exchanges: JSON over the
// repository framing. It is not the pinned runorigin codec.
type SourceWire struct{}

type sourceMsg struct {
	Hello     *sourceHello     `json:"hello,omitempty"`
	Prepare   *sourcePrepare   `json:"prepare,omitempty"`
	Prepared  *sourcePrepared  `json:"prepared,omitempty"`
	Validate  *sourceValidate  `json:"validate,omitempty"`
	Validated *sourceValidated `json:"validated,omitempty"`
	Error     string           `json:"error,omitempty"`
}
type sourceHello struct {
	Capability  bool   `json:"capability"`
	Incarnation string `json:"incarnation"`
}
type sourcePrepare struct {
	Nonce string `json:"nonce"`
}
type sourcePrepared struct {
	PreparedID  string            `json:"prepared_id"`
	Incarnation string            `json:"incarnation"`
	Bindings    map[string]string `json:"bindings"`
}
type sourceValidate struct {
	PreparedID string `json:"prepared_id"`
}
type sourceValidated struct {
	PreparedID  string            `json:"prepared_id"`
	Incarnation string            `json:"incarnation"`
	Bindings    map[string]string `json:"bindings"`
	Origin      json.RawMessage   `json:"origin"`
}

func sendSource(w io.Writer, m sourceMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return frame.Write(w, b)
}
func recvSource(r io.Reader) (sourceMsg, error) {
	var m sourceMsg
	b, err := frame.Read(r)
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	return m, nil
}
func sourceError(code string) error {
	switch code {
	case "origin_not_supported":
		return source.ErrNotSupported
	case "dependency":
		return source.ErrDependency
	}
	return source.ErrRefused
}

func (SourceWire) Hello(c net.Conn) (source.HelloMessage, error) {
	m, err := recvSource(c)
	if err != nil {
		return source.HelloMessage{}, err
	}
	if m.Error != "" {
		return source.HelloMessage{}, sourceError(m.Error)
	}
	if m.Hello == nil {
		return source.HelloMessage{}, errors.New("fakes: no hello")
	}
	return source.HelloMessage{Capability: m.Hello.Capability, Incarnation: m.Hello.Incarnation}, nil
}
func (SourceWire) Prepare(c net.Conn, req source.PrepareRequest) (source.PreparedMessage, error) {
	if err := sendSource(c, sourceMsg{Prepare: &sourcePrepare{Nonce: req.LaunchNonce}}); err != nil {
		return source.PreparedMessage{}, err
	}
	m, err := recvSource(c)
	if err != nil {
		return source.PreparedMessage{}, err
	}
	if m.Error != "" {
		return source.PreparedMessage{}, sourceError(m.Error)
	}
	if m.Prepared == nil {
		return source.PreparedMessage{}, errors.New("fakes: no prepared")
	}
	return source.PreparedMessage{PreparedID: m.Prepared.PreparedID, Incarnation: m.Prepared.Incarnation, Bindings: m.Prepared.Bindings}, nil
}
func (SourceWire) Validate(c net.Conn, req source.ValidateRequest) (source.ValidatedMessage, error) {
	if err := sendSource(c, sourceMsg{Validate: &sourceValidate{PreparedID: req.PreparedID}}); err != nil {
		return source.ValidatedMessage{}, err
	}
	m, err := recvSource(c)
	if err != nil {
		return source.ValidatedMessage{}, err
	}
	if m.Error != "" {
		return source.ValidatedMessage{}, sourceError(m.Error)
	}
	if m.Validated == nil {
		return source.ValidatedMessage{}, errors.New("fakes: no validated")
	}
	return source.ValidatedMessage{PreparedID: m.Validated.PreparedID, Incarnation: m.Validated.Incarnation, Bindings: m.Validated.Bindings, Origin: []byte(m.Validated.Origin)}, nil
}

// ValidOrigin is a well-formed six-member admission origin.
func ValidOrigin() []byte {
	return []byte(`{"event_id":"` + hexID('a') + `","grant_id":"` + hexID('b') + `","tip_hash":"` + hexID('c') + `","kind":"admission","mode":"enforcing","command_id":""}`)
}

func hexID(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

// SourceScript drives a FakeSource. Modes: "" (answer), "not-supported",
// "refused", "dependency", "drop" (read and never answer; hold the
// connection), "close" (read and close). Validate modes add
// "other-incarnation", "other-prepared", "binding-drift", "extra-binding",
// "bad-origin", "matrix-origin".
type SourceScript struct {
	NoCapability bool
	Incarnation  string
	PreparedID   string
	Bindings     map[string]string
	Origin       []byte
	PrepareMode  string
	ValidateMode string
	// FirstValidateMode overrides ValidateMode for the first connection only.
	FirstValidateMode string
	// OmitRegistration drops the identity_registration_number binding.
	OmitRegistration bool

	mu          sync.Mutex
	hellos      int
	prepares    int
	validates   []string
	connections int
}

// SetPrepareMode and SetValidateMode change the script between connections;
// they are the only race-free way to do so once a connection was served.
func (s *SourceScript) SetPrepareMode(m string)  { s.mu.Lock(); s.PrepareMode = m; s.mu.Unlock() }
func (s *SourceScript) SetValidateMode(m string) { s.mu.Lock(); s.ValidateMode = m; s.mu.Unlock() }

// Counts returns hellos sent, prepares and validates received, connections.
func (s *SourceScript) Counts() (hellos, prepares, validates, connections int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hellos, s.prepares, len(s.validates), s.connections
}

// ValidatedIDs returns the prepared IDs validate asked for, in order.
func (s *SourceScript) ValidatedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.validates...)
}

func (s *SourceScript) incarnation() string {
	if s.Incarnation == "" {
		return "inc-1"
	}
	return s.Incarnation
}

// preparedID is distinct per connection, so a fresh prepare after a restart
// is visible as a new prepared_id.
func (s *SourceScript) preparedID(conn int) string {
	base := s.PreparedID
	if base == "" {
		base = "prep"
	}
	return fmt.Sprintf("%s-%d", base, conn)
}
func (s *SourceScript) bindings(conn int) map[string]string {
	out := map[string]string{"source_incarnation": s.incarnation(), "prepared_id": s.preparedID(conn), "station": "station-1", "identity_registration_number": "1"}
	for k, v := range s.Bindings {
		out[k] = v
	}
	if s.OmitRegistration {
		delete(out, "identity_registration_number")
	}
	return out
}
func (s *SourceScript) origin() []byte {
	if s.Origin != nil {
		return s.Origin
	}
	return ValidOrigin()
}

func hold(c net.Conn) {
	buf := make([]byte, 64)
	for {
		if _, err := c.Read(buf); err != nil {
			return
		}
	}
}

// Serve handles one connection to completion. It records counts.
func (s *SourceScript) Serve(c net.Conn) {
	defer c.Close()
	s.mu.Lock()
	s.connections++
	conn := s.connections
	s.hellos++
	prepareMode := s.PrepareMode
	validateMode := s.ValidateMode
	if conn == 1 && s.FirstValidateMode != "" {
		validateMode = s.FirstValidateMode
	}
	s.mu.Unlock()
	if err := sendSource(c, sourceMsg{Hello: &sourceHello{Capability: !s.NoCapability, Incarnation: s.incarnation()}}); err != nil {
		return
	}
	m, err := recvSource(c)
	if err != nil || m.Prepare == nil {
		return
	}
	s.mu.Lock()
	s.prepares++
	s.mu.Unlock()
	switch prepareMode {
	case "not-supported":
		_ = sendSource(c, sourceMsg{Error: "origin_not_supported"})
		return
	case "refused":
		_ = sendSource(c, sourceMsg{Error: "refused"})
		return
	case "dependency":
		_ = sendSource(c, sourceMsg{Error: "dependency"})
		return
	case "drop":
		hold(c)
		return
	case "close":
		return
	}
	if err := sendSource(c, sourceMsg{Prepared: &sourcePrepared{PreparedID: s.preparedID(conn), Incarnation: s.incarnation(), Bindings: s.bindings(conn)}}); err != nil {
		return
	}
	m, err = recvSource(c)
	if err != nil || m.Validate == nil {
		return
	}
	s.mu.Lock()
	s.validates = append(s.validates, m.Validate.PreparedID)
	s.mu.Unlock()
	v := &sourceValidated{PreparedID: s.preparedID(conn), Incarnation: s.incarnation(), Bindings: s.bindings(conn), Origin: json.RawMessage(s.origin())}
	switch validateMode {
	case "refused":
		_ = sendSource(c, sourceMsg{Error: "refused"})
		return
	case "dependency":
		_ = sendSource(c, sourceMsg{Error: "dependency"})
		return
	case "drop":
		hold(c)
		return
	case "close":
		return
	case "other-incarnation":
		v.Incarnation = s.incarnation() + "-restarted"
		v.Bindings["source_incarnation"] = v.Incarnation
	case "other-prepared":
		v.PreparedID = s.preparedID(conn) + "-other"
		v.Bindings["prepared_id"] = v.PreparedID
	case "binding-drift":
		v.Bindings["station"] = "station-2"
	case "extra-binding":
		v.Bindings["extra"] = "1"
	case "bad-origin":
		v.Origin = json.RawMessage(`{"mode":"enforcing","command_id":""}`)
	case "matrix-origin":
		v.Origin = json.RawMessage(`{"event_id":"$` + hexID('a')[1:] + `","grant_id":"` + hexID('b') + `","tip_hash":"` + hexID('c') + `","kind":"admission","mode":"enforcing","command_id":""}`)
	}
	_ = sendSource(c, sourceMsg{Validated: v})
	// Hold until the client closes, so a repeated validate sees a live peer.
	hold(c)
}

// SourceDialer is a source.Dialer over socket pairs. Each Dial serves one
// connection with the script. Peer is the credential the client sees.
type SourceDialer struct {
	Script  *SourceScript
	Peer    source.Credential
	PeerErr error
	// RealPeer makes Peer() read SO_PEERCRED instead of the configured value.
	RealPeer bool
	// Fail makes Dial fail with this error.
	Fail error

	mu    sync.Mutex
	dials int
	paths []string
}

type peerConn struct {
	net.Conn
	cred source.Credential
	err  error
	real bool
}

func (p *peerConn) Peer() (source.Credential, error) {
	if p.err != nil {
		return source.Credential{}, p.err
	}
	if p.real {
		uid, err := PeerUID(p.Conn)
		if err != nil {
			return source.Credential{}, err
		}
		return source.Credential{UID: uid, PID: int32(os.Getpid())}, nil
	}
	return p.cred, nil
}

// Dial implements source.Dialer.
func (d *SourceDialer) Dial(path string) (source.Conn, error) {
	d.mu.Lock()
	d.dials++
	d.paths = append(d.paths, path)
	d.mu.Unlock()
	if d.Fail != nil {
		return nil, d.Fail
	}
	a, b, err := SocketPair()
	if err != nil {
		return nil, err
	}
	go d.Script.Serve(b)
	return &peerConn{Conn: a, cred: d.Peer, err: d.PeerErr, real: d.RealPeer}, nil
}

// Dials returns the number of connections opened and the paths asked for.
func (d *SourceDialer) Dials() (int, []string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials, append([]string(nil), d.paths...)
}

// ControlDir creates <root>/control with mode 0711 and a listening
// origin.sock with mode 0666, served by the script. It returns the control
// directory and a stop function.
func ControlDir(root string, script *SourceScript) (string, func(), error) {
	dir := filepath.Join(root, "control")
	if err := os.Mkdir(dir, 0o711); err != nil {
		return "", nil, err
	}
	if err := os.Chmod(dir, 0o711); err != nil {
		return "", nil, err
	}
	sock := filepath.Join(dir, source.SocketName)
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return "", nil, err
	}
	l.SetUnlinkOnClose(false)
	if err := os.Chmod(sock, 0o666); err != nil {
		_ = l.Close()
		return "", nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			if script != nil {
				go script.Serve(c)
			} else {
				_ = c.Close()
			}
		}
	}()
	stop := func() {
		_ = l.Close()
		<-done
	}
	return dir, stop, nil
}

// PinnedSource is a fake Source that speaks the documented pin A protocol
// (the one source.Pinned implements) over socket pairs, for launcher tests
// that run the pinned adapter. It answers hello, prepare and validate for any
// number of concurrent connections and records every prepare request.
type PinnedSource struct {
	// Registration is the identity_registration_number it reports.
	Registration string
	Peer         source.Credential

	mu       sync.Mutex
	conns    int
	prepares []source.PrepareRequest
	errs     []error
}

// Prepares returns every prepare request received (its event and input
// principal among them), in arrival order.
func (p *PinnedSource) Prepares() []source.PrepareRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]source.PrepareRequest(nil), p.prepares...)
}

// Errs returns protocol errors the fake observed.
func (p *PinnedSource) Errs() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]error(nil), p.errs...)
}

// Dial implements source.Dialer.
func (p *PinnedSource) Dial(string) (source.Conn, error) {
	a, b, err := SocketPair()
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	p.conns++
	n := p.conns
	p.mu.Unlock()
	go func() {
		defer b.Close()
		if err := p.serve(b, n); err != nil {
			p.mu.Lock()
			p.errs = append(p.errs, err)
			p.mu.Unlock()
		}
	}()
	return &peerConn{Conn: a, cred: p.Peer}, nil
}

func (p *PinnedSource) serve(c net.Conn, n int) error {
	inc := fmt.Sprintf("%032x", 0x1000)
	prep := fmt.Sprintf("%032x", 0x2000+n)
	if err := frame.Write(c, []byte(`{"capabilities":["source.origin.prepare/1","source.origin.validate/1"],"source_incarnation":"`+inc+`"}`)); err != nil {
		return err
	}
	b, err := frame.Read(c)
	if err != nil {
		return err
	}
	var req struct {
		Op     string                `json:"op"`
		Params source.PrepareRequest `json:"params"`
	}
	if err = json.Unmarshal(b, &req); err != nil {
		return err
	}
	if req.Op != "source.origin.prepare/1" {
		return errors.New("fakes: pinned source: not a prepare")
	}
	p.mu.Lock()
	p.prepares = append(p.prepares, req.Params)
	p.mu.Unlock()
	prepared := runorigin.PreparedOrigin{RequestID: req.Params.RequestID, LaunchNonce: req.Params.LaunchNonce, InputPrincipal: req.Params.InputPrincipal, IdentityRegistrationNumber: p.Registration, SourceIncarnation: inc, PreparedID: prep}
	if err = json.Unmarshal(ValidOrigin(), &prepared.Origin); err != nil {
		return err
	}
	out, err := json.Marshal(prepared)
	if err != nil {
		return err
	}
	if err = frame.Write(c, out); err != nil {
		return err
	}
	b, err = frame.Read(c)
	if err != nil {
		return err
	}
	var val struct {
		Op     string                 `json:"op"`
		Params source.ValidateRequest `json:"params"`
	}
	if err = json.Unmarshal(b, &val); err != nil {
		return err
	}
	if val.Op != "source.origin.validate/1" || val.Params.PreparedID != prep {
		return errors.New("fakes: pinned source: validate differs from prepare")
	}
	return frame.Write(c, out)
}
