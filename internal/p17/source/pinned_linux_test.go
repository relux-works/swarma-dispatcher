//go:build linux

package source_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/a2a-swarm/runorigin"
	"github.com/relux-works/swarma-dispatcher/internal/frame"
	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
)

type pinnedPeer struct{ net.Conn }

func (p pinnedPeer) Peer() (source.Credential, error) { return source.Credential{UID: uid}, nil }

type pinnedDialer struct {
	serve func(net.Conn) error
	done  chan error
}

func (d *pinnedDialer) Dial(string) (source.Conn, error) {
	a, b, err := fakes.SocketPair()
	if err != nil {
		return nil, err
	}
	go func() { defer b.Close(); _ = b.SetDeadline(time.Now().Add(2 * time.Second)); d.done <- d.serve(b) }()
	return pinnedPeer{a}, nil
}
func pinnedEnv(t *testing.T, mode string) (*source.Client, chan error) {
	t.Helper()
	e := newEnv(t, nil)
	c := e.client
	c.Wire = source.Pinned{}
	c.Event = runorigin.EventRef{RoomID: "!room:example", EventID: "$retained-event"}
	c.InputPrincipal = runorigin.Principal{UID: 22001, Generation: 19}
	inc, prep := strings.Repeat("1", 32), strings.Repeat("2", 32)
	d := &pinnedDialer{done: make(chan error, 1)}
	d.serve = func(peer net.Conn) error {
		hello := `{"capabilities":["source.origin.prepare/1","source.origin.validate/1"],"source_incarnation":"` + inc + `"}`
		if mode == "no-capability" {
			hello = `{"capabilities":[],"source_incarnation":"` + inc + `"}`
		}
		if mode == "duplicate-hello" {
			hello = strings.Replace(hello, `"source_incarnation":`, `"source_incarnation":"`+inc+`","source_incarnation":`, 1)
		}
		if err := frame.Write(peer, []byte(hello)); err != nil {
			return err
		}
		if mode == "no-capability" || mode == "duplicate-hello" {
			return nil
		}
		b, err := frame.Read(peer)
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
		p := req.Params
		if req.Op != "source.origin.prepare/1" || p.Event != c.Event || p.InputPrincipal != c.InputPrincipal || p.LaunchNonce != strings.Repeat("b", 32) || !runorigin.LowerHex(p.RequestID, 32) {
			return errors.New("prepare context differs")
		}
		prepared := runorigin.PreparedOrigin{RequestID: p.RequestID, LaunchNonce: p.LaunchNonce, InputPrincipal: p.InputPrincipal, IdentityRegistrationNumber: "1", SourceIncarnation: inc, PreparedID: prep}
		if err = json.Unmarshal(fakes.ValidOrigin(), &prepared.Origin); err != nil {
			return err
		}
		out, err := json.Marshal(prepared)
		if err != nil {
			return err
		}
		// Prepare and validate use the same immutable origin and full bindings.
		if err = frame.Write(peer, out); err != nil {
			return err
		}
		b, err = frame.Read(peer)
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
		want := source.ValidateRequest{RequestID: p.RequestID, LaunchNonce: p.LaunchNonce, PreparedID: prep, SourceIncarnation: inc}
		if val.Op != "source.origin.validate/1" || val.Params != want {
			return errors.New("validate did not repeat preparation context")
		}
		switch mode {
		case "request-id":
			prepared.RequestID = strings.Repeat("3", 32)
		case "nonce":
			prepared.LaunchNonce = strings.Repeat("3", 32)
		case "uid":
			prepared.InputPrincipal.UID++
		case "generation":
			prepared.InputPrincipal.Generation++
		case "registration":
			prepared.IdentityRegistrationNumber = "2"
		case "incarnation":
			prepared.SourceIncarnation = strings.Repeat("3", 32)
		case "prepared-id":
			prepared.PreparedID = strings.Repeat("3", 32)
		case "origin":
			prepared.Origin.GrantID = strings.Repeat("d", 64)
		case "lost":
			return nil
		case "not-supported", "dependency", "dep-inc":
			code := runorigin.NotSupported
			if mode != "not-supported" {
				code = runorigin.Unavailable
			}
			if mode == "dep-inc" {
				inc = strings.Repeat("3", 32)
			}
			return frame.Write(peer, []byte(fmt.Sprintf(`{"code":%q,"source_incarnation":%q}`, code, inc)))
		}
		out, err = json.Marshal(prepared)
		if err != nil {
			return err
		}
		switch mode {
		case "extra":
			out = append([]byte(`{"extra":1,`), out[1:]...)
		case "case":
			out = []byte(strings.Replace(string(out), `"prepared_id":`, `"Prepared_ID":`, 1))
		case "duplicate":
			out = []byte(strings.Replace(string(out), `"request_id":`, `"request_id":"`+p.RequestID+`","request\u005fid":`, 1))
		case "null":
			out = []byte(strings.Replace(string(out), `"command_id":""`, `"command_id":null`, 1))
		case "nested-duplicate":
			out = []byte(strings.Replace(string(out), `"uid":22001`, `"uid":22001,"u\u0069d":22001`, 1))
		case "wrong-name":
			out = []byte(strings.Replace(string(out), `"grant_id":`, `"another_id":`, 1))
		}
		return frame.Write(peer, out)
	}
	c.Dialer = d
	return c, d.done
}

// Claim: pin A round-trips full bindings once; drift, closed-schema violations and malformed origins refuse, while lost/dependency validate restarts.
func TestPinnedSource(t *testing.T) {
	for _, mode := range []string{"ok", "no-capability", "duplicate-hello", "request-id", "nonce", "uid", "generation", "registration", "incarnation", "prepared-id", "origin", "extra", "case", "duplicate", "null", "nested-duplicate", "wrong-name", "not-supported", "dependency", "dep-inc", "lost"} {
		t.Run(mode, func(t *testing.T) {
			c, done := pinnedEnv(t, mode)
			p, err := c.Prepare(context.Background(), strings.Repeat("b", 32))
			if err == nil {
				var v *source.Validated
				v, err = p.Validate(context.Background())
				if mode == "ok" {
					if err != nil || v.PreparedID != strings.Repeat("2", 32) || len(v.Bindings) != 7 || source.CheckOrigin(v.Origin) != nil {
						t.Fatalf("%+v %v", v, err)
					}
					_, again := p.Validate(context.Background())
					if !codes.Is(again, codes.OriginRefused) {
						t.Fatal("second validate accepted")
					}
				}
			}
			if mode == "ok" {
				if err != nil {
					t.Fatal(err)
				}
			} else if mode == "lost" || mode == "dependency" {
				if !source.IsRestart(err) {
					t.Fatalf("want restart: %v", err)
				}
			} else if !codes.Is(err, codes.OriginRefused) {
				t.Fatalf("want refusal: %v", err)
			}
			select {
			case e := <-done:
				if e != nil {
					t.Fatal(e)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Source fixture did not stop")
			}
		})
	}
}
