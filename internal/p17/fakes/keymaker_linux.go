//go:build linux

package fakes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/frame"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
)

// KeymakerWire is the TEST spelling of the lifecycle exchanges: JSON over the
// repository framing. It is not the pinned j7lifecycle codec.
type KeymakerWire struct{}

type kmRequest struct {
	ID        string          `json:"id"`
	Method    string          `json:"method"`
	TimeoutMS int64           `json:"timeout_ms,omitempty"`
	Body      json.RawMessage `json:"body,omitempty"`
}
type kmBindBody struct {
	IdentityRegistrationNumber uint64             `json:"identity_registration_number"`
	InputPrincipal             keymaker.Principal `json:"input_principal"`
	Origin                     json.RawMessage    `json:"origin"`
	ParentRun                  string             `json:"parent_run"`
}
type kmEndBody struct {
	RunID string `json:"run_id"`
}

// kmError is the test spelling of a native refusal: a code and its retryable
// bit. Refused (below) is the separate test-only no-effect certification.
type kmError struct {
	Code      string `json:"code"`
	Retryable bool   `json:"retryable"`
}
type kmResponse struct {
	// ID is a pointer so that the fake can send an explicit null.
	ID      *string         `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Refused string          `json:"refused,omitempty"`
	Error   *kmError        `json:"error,omitempty"`
}
type kmBindResult struct {
	RunID string `json:"run_id"`
}
type kmEndResult struct {
	RunID string `json:"run_id"`
	Ended bool   `json:"ended"`
}

const kmHello = "hello"

// Hello is the test spelling of the native hello: one request, one reply.
func (KeymakerWire) Hello(c net.Conn, budget time.Duration) error {
	if err := c.SetDeadline(time.Now().Add(budget)); err != nil {
		return err
	}
	b, err := json.Marshal(kmRequest{ID: kmHello, Method: kmHello})
	if err != nil {
		return err
	}
	if err = frame.Write(c, b); err != nil {
		return err
	}
	raw, err := frame.Read(c)
	if err != nil {
		return err
	}
	var r kmResponse
	if err = json.Unmarshal(raw, &r); err != nil {
		return err
	}
	if r.ID == nil || *r.ID != kmHello || r.Error != nil || r.Refused != "" {
		return errors.New("fakes: hello refused")
	}
	return nil
}

func (KeymakerWire) Encode(req keymaker.Request) ([]byte, error) {
	var body []byte
	var err error
	switch req.Method {
	case keymaker.MethodBind:
		if req.Bind == nil {
			return nil, errors.New("fakes: bind body required")
		}
		origin := json.RawMessage("null")
		if req.Bind.Origin != nil {
			origin = json.RawMessage(req.Bind.Origin)
		}
		body, err = json.Marshal(kmBindBody{req.Bind.IdentityRegistrationNumber, req.Bind.InputPrincipal, origin, req.Bind.ParentRun})
	case keymaker.MethodEnd:
		if req.End == nil {
			return nil, errors.New("fakes: end body required")
		}
		body, err = json.Marshal(kmEndBody{req.End.RunID})
	default:
		return nil, errors.New("fakes: unknown method")
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(kmRequest{ID: req.ID, Method: string(req.Method), TimeoutMS: req.TimeoutMS, Body: body})
}
func (KeymakerWire) WriteFrame(w io.Writer, b []byte) error { return frame.Write(w, b) }
func (KeymakerWire) ReadFrame(r io.Reader) ([]byte, error)  { return frame.Read(r) }
func (w KeymakerWire) DecodeResponse(expected keymaker.Request, b []byte) (keymaker.Response, error) {
	var r kmResponse
	if err := json.Unmarshal(b, &r); err != nil {
		return keymaker.Response{}, err
	}
	out := keymaker.Response{}
	if r.ID != nil {
		out.ID = *r.ID
	}
	if r.Error != nil {
		out.Refusal = &keymaker.NativeRefusal{Code: r.Error.Code, Retryable: r.Error.Retryable}
		return out, nil
	}
	if r.Refused != "" {
		// The test wire certifies a test-only no-effect refusal.
		out.Refused = true
		out.Refusal = &keymaker.NativeRefusal{Code: r.Refused}
		return out, nil
	}
	if expected.Method == keymaker.MethodBind {
		var res kmBindResult
		if err := json.Unmarshal(r.Result, &res); err != nil {
			return keymaker.Response{}, err
		}
		out.Bind = &keymaker.BindResult{RunID: res.RunID}
		return out, nil
	}
	var res kmEndResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		return keymaker.Response{}, err
	}
	out.End = &keymaker.EndResult{RunID: res.RunID, Ended: res.Ended}
	return out, nil
}
func (KeymakerWire) DecodeEndRequest(b []byte) (keymaker.Request, error) {
	var r kmRequest
	if err := json.Unmarshal(b, &r); err != nil {
		return keymaker.Request{}, err
	}
	var body kmEndBody
	if err := json.Unmarshal(r.Body, &body); err != nil {
		return keymaker.Request{}, err
	}
	if r.Method != string(keymaker.MethodEnd) || r.ID == "" || body.RunID == "" {
		return keymaker.Request{}, errors.New("fakes: invalid durable end request")
	}
	return keymaker.Request{ID: r.ID, Method: keymaker.MethodEnd, TimeoutMS: r.TimeoutMS, End: &keymaker.EndRequest{RunID: body.RunID}}, nil
}

// KeymakerScript drives a FakeKeymaker connection. Like the pinned holder it
// reads frames strictly in sequence and always either replies to a request
// or closes; it never silently skips one request and answers the next.
//
// HelloMode: "" (answer), "close" (close without answering), "refuse".
//
// BindMode: "" (answer), "late" (answer success after Delay),
// "deadline-then-ok" (the first bind is answered after Delay with the
// correlated retryable refusal request_deadline_exceeded, the identical
// second with success), "busy" (every bind is answered with the correlated
// retryable refusal busy), "nonretryable" (a correlated non-retryable
// refusal outside the no-effect set), "null-id" (a retryable refusal with a
// null ID), "silent" (read and never answer, holding the connection; the
// reply stays owed), "close" (close without answering), "refuse" (the
// test-only no-effect refusal), "no-run-id", "wrong-id" (a retryable
// refusal whose ID differs from the request's).
//
// EndMode: "" (exact ended), "late" (exact ended after Delay), "silent",
// "close", "refuse", "not-ended", "other-run".
//
// While a reply is owed (during Delay, and for as long as a "silent" request
// is held) the fake watches the connection: any byte that arrives then is a
// request written while another is in flight and is counted in Overlaps.
type KeymakerScript struct {
	RunID     string
	HelloMode string
	BindMode  string
	EndMode   string
	// Delay is how long "late" and "deadline-then-ok" replies are held back.
	Delay time.Duration

	mu          sync.Mutex
	hellos      int
	binds       [][]byte
	ends        [][]byte
	connections int
	closed      int
	overlaps    int
	bindBytes   int
}

// BindBytes counts bytes read in the first post-hello frame, even partial frames.
func (s *KeymakerScript) BindBytes() int { s.mu.Lock(); defer s.mu.Unlock(); return s.bindBytes }

type bindReader struct {
	io.Reader
	s *KeymakerScript
}

func (r bindReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.s.mu.Lock()
	r.s.bindBytes += n
	r.s.mu.Unlock()
	return n, err
}

func (s *KeymakerScript) runID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RunID == "" {
		return "run-1"
	}
	return s.RunID
}

// SetRunID, SetHelloMode, SetBindMode and SetEndMode change the script while
// connections are being served; they are the only race-free way to do so.
func (s *KeymakerScript) SetRunID(id string)    { s.mu.Lock(); s.RunID = id; s.mu.Unlock() }
func (s *KeymakerScript) SetHelloMode(m string) { s.mu.Lock(); s.HelloMode = m; s.mu.Unlock() }
func (s *KeymakerScript) SetBindMode(m string)  { s.mu.Lock(); s.BindMode = m; s.mu.Unlock() }
func (s *KeymakerScript) SetEndMode(m string)   { s.mu.Lock(); s.EndMode = m; s.mu.Unlock() }
func (s *KeymakerScript) modes() (hello, bind, end string, delay time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.HelloMode, s.BindMode, s.EndMode, s.Delay
}

// Hellos returns the number of hello requests received.
func (s *KeymakerScript) Hellos() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hellos
}

// Overlaps returns how many times bytes arrived while a reply was owed.
func (s *KeymakerScript) Overlaps() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overlaps
}

// Binds returns the raw bind request frames received, in order.
func (s *KeymakerScript) Binds() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.binds))
	for i, b := range s.binds {
		out[i] = append([]byte(nil), b...)
	}
	return out
}

// Ends returns the raw end request frames received, in order.
func (s *KeymakerScript) Ends() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.ends))
	for i, b := range s.ends {
		out[i] = append([]byte(nil), b...)
	}
	return out
}

// Connections returns the number of connections served.
func (s *KeymakerScript) Connections() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections
}

// ParseBind decodes a raw bind frame the fake received.
func ParseBind(raw []byte) (id string, body keymaker.BindRequest, err error) {
	var r kmRequest
	if err = json.Unmarshal(raw, &r); err != nil {
		return "", body, err
	}
	var b kmBindBody
	if err = json.Unmarshal(r.Body, &b); err != nil {
		return "", body, err
	}
	body = keymaker.BindRequest{IdentityRegistrationNumber: b.IdentityRegistrationNumber, InputPrincipal: b.InputPrincipal, ParentRun: b.ParentRun}
	if !bytes.Equal(bytes.TrimSpace(b.Origin), []byte("null")) {
		body.Origin = []byte(b.Origin)
	}
	return r.ID, body, nil
}

func (s *KeymakerScript) reply(c net.Conn, r kmResponse) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return frame.Write(c, b)
}

// owed holds a reply back while watching the connection: for d when forever
// is false (a zero d returns at once), until the client closes when forever
// is true ("silent"). It returns false when the client closed the connection
// meanwhile.
func (s *KeymakerScript) owed(c net.Conn, d time.Duration, forever bool) bool {
	switch {
	case forever:
		_ = c.SetReadDeadline(time.Time{})
	case d <= 0:
		return true
	default:
		_ = c.SetReadDeadline(time.Now().Add(d))
	}
	defer func() { _ = c.SetReadDeadline(time.Time{}) }()
	var one [1]byte
	for {
		n, err := c.Read(one[:])
		if n > 0 {
			s.mu.Lock()
			s.overlaps++
			s.mu.Unlock()
			continue
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return true
		}
		if err != nil {
			return false
		}
	}
}

// Closed returns the number of served connections that have ended.
func (s *KeymakerScript) Closed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Serve handles one connection until the client closes it.
func (s *KeymakerScript) Serve(c net.Conn) {
	defer func() {
		_ = c.Close()
		s.mu.Lock()
		s.closed++
		s.mu.Unlock()
	}()
	s.mu.Lock()
	s.connections++
	s.mu.Unlock()
	bindsSeen := 0
	postHello := false
	for {
		var reader io.Reader = c
		if postHello {
			reader = bindReader{c, s}
		}
		raw, err := frame.Read(reader)
		if err != nil {
			var ne net.Error
			if postHello && errors.As(err, &ne) && ne.Timeout() {
				_ = s.reply(c, kmResponse{Error: &kmError{Code: "request_deadline_exceeded", Retryable: true}})
			}
			return
		}
		postHello = false
		_ = c.SetReadDeadline(time.Time{})
		var req kmRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return
		}
		helloMode, bindMode, endMode, delay := s.modes()
		id := req.ID
		switch req.Method {
		case kmHello:
			s.mu.Lock()
			s.hellos++
			s.mu.Unlock()
			switch helloMode {
			case "silent":
				s.owed(c, 0, true)
				return
			case "close":
				return
			case "refuse":
				_ = s.reply(c, kmResponse{ID: &id, Error: &kmError{Code: "version_unsupported"}})
				return
			}
			if err := s.reply(c, kmResponse{ID: &id}); err != nil {
				return
			}
			postHello = true
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		case string(keymaker.MethodBind):
			s.mu.Lock()
			s.binds = append(s.binds, append([]byte(nil), raw...))
			s.mu.Unlock()
			bindsSeen++
			ok, _ := json.Marshal(kmBindResult{RunID: s.runID()})
			switch bindMode {
			case "silent":
				s.owed(c, 0, true)
				return
			case "close":
				return
			case "late":
				if !s.owed(c, delay, false) {
					return
				}
			case "deadline-then-ok":
				if bindsSeen == 1 {
					if !s.owed(c, delay, false) {
						return
					}
					_ = s.reply(c, kmResponse{ID: &id, Error: &kmError{Code: "request_deadline_exceeded", Retryable: true}})
					continue
				}
			case "busy":
				_ = s.reply(c, kmResponse{ID: &id, Error: &kmError{Code: "busy", Retryable: true}})
				continue
			case "nonretryable":
				_ = s.reply(c, kmResponse{ID: &id, Error: &kmError{Code: "role_not_permitted"}})
				continue
			case "null-id":
				_ = s.reply(c, kmResponse{Error: &kmError{Code: "busy", Retryable: true}})
				continue
			case "refuse":
				_ = s.reply(c, kmResponse{ID: &id, Refused: "registration_unknown"})
				continue
			case "no-run-id":
				_ = s.reply(c, kmResponse{ID: &id, Result: json.RawMessage(`{"run_id":""}`)})
				continue
			case "wrong-id":
				other := id + "-other"
				_ = s.reply(c, kmResponse{ID: &other, Error: &kmError{Code: "busy", Retryable: true}})
				continue
			}
			if err := s.reply(c, kmResponse{ID: &id, Result: ok}); err != nil {
				return
			}
		case string(keymaker.MethodEnd):
			s.mu.Lock()
			s.ends = append(s.ends, append([]byte(nil), raw...))
			s.mu.Unlock()
			var body kmEndBody
			_ = json.Unmarshal(req.Body, &body)
			switch endMode {
			case "silent":
				s.owed(c, 0, true)
				return
			case "close":
				return
			case "late":
				if !s.owed(c, delay, false) {
					return
				}
			case "refuse":
				_ = s.reply(c, kmResponse{ID: &id, Error: &kmError{Code: "run_unknown"}})
				continue
			case "not-ended":
				res, _ := json.Marshal(kmEndResult{RunID: body.RunID, Ended: false})
				_ = s.reply(c, kmResponse{ID: &id, Result: res})
				continue
			case "other-run":
				res, _ := json.Marshal(kmEndResult{RunID: body.RunID + "-other", Ended: true})
				_ = s.reply(c, kmResponse{ID: &id, Result: res})
				continue
			}
			res, _ := json.Marshal(kmEndResult{RunID: body.RunID, Ended: true})
			if err := s.reply(c, kmResponse{ID: &id, Result: res}); err != nil {
				return
			}
		default:
			return
		}
	}
}

// KeymakerDialer is a keymaker.Dialer over socket pairs.
type KeymakerDialer struct {
	Script *KeymakerScript
	Fail   error

	mu    sync.Mutex
	dials int
}

// Dial implements keymaker.Dialer.
func (d *KeymakerDialer) Dial(context.Context) (net.Conn, error) {
	d.mu.Lock()
	d.dials++
	d.mu.Unlock()
	if d.Fail != nil {
		return nil, d.Fail
	}
	a, b, err := SocketPair()
	if err != nil {
		return nil, err
	}
	go d.Script.Serve(b)
	return a, nil
}

// Dials returns the number of connections opened.
func (d *KeymakerDialer) Dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dials
}

// RequestTimeoutMS returns the timeout_ms a raw request frame the fake
// received carries.
func RequestTimeoutMS(raw []byte) (int64, error) {
	var r kmRequest
	if err := json.Unmarshal(raw, &r); err != nil {
		return 0, err
	}
	return r.TimeoutMS, nil
}
