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

	"github.com/relux-works/swarma-dispatcher/internal/frame"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
)

// KeymakerWire is the TEST spelling of the lifecycle exchanges: JSON over the
// repository framing. It is not the pinned j7lifecycle codec.
type KeymakerWire struct{}

type kmRequest struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Body   json.RawMessage `json:"body"`
}
type kmBindBody struct {
	IdentityRegistrationNumber string          `json:"identity_registration_number"`
	InputPrincipal             string          `json:"input_principal"`
	Origin                     json.RawMessage `json:"origin"`
	ParentRun                  string          `json:"parent_run"`
}
type kmEndBody struct {
	RunID string `json:"run_id"`
}
type kmResponse struct {
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Refused string          `json:"refused,omitempty"`
}
type kmBindResult struct {
	RunID string `json:"run_id"`
}
type kmEndResult struct {
	RunID string `json:"run_id"`
	Ended bool   `json:"ended"`
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
	return json.Marshal(kmRequest{ID: req.ID, Method: string(req.Method), Body: body})
}
func (KeymakerWire) WriteFrame(w io.Writer, b []byte) error { return frame.Write(w, b) }
func (KeymakerWire) ReadFrame(r io.Reader) ([]byte, error)  { return frame.Read(r) }
func (KeymakerWire) DecodeResponse(b []byte) (keymaker.Response, error) {
	var r kmResponse
	if err := json.Unmarshal(b, &r); err != nil {
		return keymaker.Response{}, err
	}
	return keymaker.Response{ID: r.ID, Body: append([]byte(nil), b...)}, nil
}
func (KeymakerWire) ParseBindResult(body []byte) (keymaker.BindResult, error) {
	var r kmResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return keymaker.BindResult{}, err
	}
	if r.Refused != "" {
		return keymaker.BindResult{}, errors.Join(keymaker.ErrRefused, errors.New(r.Refused))
	}
	var res kmBindResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		return keymaker.BindResult{}, err
	}
	return keymaker.BindResult{RunID: res.RunID}, nil
}
func (KeymakerWire) ParseEndResult(body []byte) (keymaker.EndResult, error) {
	var r kmResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return keymaker.EndResult{}, err
	}
	if r.Refused != "" {
		return keymaker.EndResult{}, errors.Join(keymaker.ErrRefused, errors.New(r.Refused))
	}
	var res kmEndResult
	if err := json.Unmarshal(r.Result, &res); err != nil {
		return keymaker.EndResult{}, err
	}
	return keymaker.EndResult{RunID: res.RunID, Ended: res.Ended}, nil
}

// KeymakerScript drives a FakeKeymaker connection. BindMode: "" (answer),
// "drop-first" (ignore the first bind, answer the identical second),
// "drop-all", "close" (close without answering), "refuse", "no-run-id",
// "wrong-id". EndMode: "" (exact ended), "drop", "close", "refuse",
// "not-ended", "other-run".
type KeymakerScript struct {
	RunID    string
	BindMode string
	EndMode  string

	mu          sync.Mutex
	binds       [][]byte
	ends        [][]byte
	connections int
}

func (s *KeymakerScript) runID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.RunID == "" {
		return "run-1"
	}
	return s.RunID
}

// SetRunID, SetBindMode and SetEndMode change the script while connections
// are being served; they are the only race-free way to do so.
func (s *KeymakerScript) SetRunID(id string)   { s.mu.Lock(); s.RunID = id; s.mu.Unlock() }
func (s *KeymakerScript) SetBindMode(m string) { s.mu.Lock(); s.BindMode = m; s.mu.Unlock() }
func (s *KeymakerScript) SetEndMode(m string)  { s.mu.Lock(); s.EndMode = m; s.mu.Unlock() }
func (s *KeymakerScript) modes() (string, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.BindMode, s.EndMode
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

// Serve handles one connection until the client closes it.
func (s *KeymakerScript) Serve(c net.Conn) {
	defer c.Close()
	s.mu.Lock()
	s.connections++
	s.mu.Unlock()
	bindsSeen := 0
	for {
		raw, err := frame.Read(c)
		if err != nil {
			return
		}
		var req kmRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return
		}
		switch req.Method {
		case string(keymaker.MethodBind):
			s.mu.Lock()
			s.binds = append(s.binds, append([]byte(nil), raw...))
			s.mu.Unlock()
			bindsSeen++
			bindMode, _ := s.modes()
			switch bindMode {
			case "drop-all":
				continue
			case "drop-first":
				if bindsSeen == 1 {
					continue
				}
			case "close":
				return
			case "refuse":
				_ = s.reply(c, kmResponse{ID: req.ID, Refused: "registration_unknown"})
				continue
			case "no-run-id":
				_ = s.reply(c, kmResponse{ID: req.ID, Result: json.RawMessage(`{"run_id":""}`)})
				continue
			case "wrong-id":
				_ = s.reply(c, kmResponse{ID: req.ID + "-other", Result: json.RawMessage(`{"run_id":"` + s.runID() + `"}`)})
				continue
			}
			res, _ := json.Marshal(kmBindResult{RunID: s.runID()})
			if err := s.reply(c, kmResponse{ID: req.ID, Result: res}); err != nil {
				return
			}
		case string(keymaker.MethodEnd):
			s.mu.Lock()
			s.ends = append(s.ends, append([]byte(nil), raw...))
			s.mu.Unlock()
			var body kmEndBody
			_ = json.Unmarshal(req.Body, &body)
			_, endMode := s.modes()
			switch endMode {
			case "drop":
				continue
			case "close":
				return
			case "refuse":
				_ = s.reply(c, kmResponse{ID: req.ID, Refused: "run_unknown"})
				continue
			case "not-ended":
				res, _ := json.Marshal(kmEndResult{RunID: body.RunID, Ended: false})
				_ = s.reply(c, kmResponse{ID: req.ID, Result: res})
				continue
			case "other-run":
				res, _ := json.Marshal(kmEndResult{RunID: body.RunID + "-other", Ended: true})
				_ = s.reply(c, kmResponse{ID: req.ID, Result: res})
				continue
			}
			res, _ := json.Marshal(kmEndResult{RunID: body.RunID, Ended: true})
			if err := s.reply(c, kmResponse{ID: req.ID, Result: res}); err != nil {
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
