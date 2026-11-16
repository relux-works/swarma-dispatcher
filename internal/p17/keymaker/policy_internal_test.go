package keymaker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	wire "github.com/relux-works/swarma-keymaker/j7lifecycle"
)

type policyDialer struct {
	serve func(net.Conn) error
	done  chan error
}

func (d policyDialer) Dial(context.Context) (net.Conn, error) {
	a, b := net.Pipe()
	go func() { defer b.Close(); d.done <- d.serve(b) }()
	return a, nil
}
func policyFrame(body string) []byte {
	b := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(b, uint32(len(body)))
	copy(b[4:], body)
	return b
}
func policyBind() BindRequest {
	return BindRequest{IdentityRegistrationNumber: 1, InputPrincipal: Principal{UID: 22001, Generation: "g"}, Origin: []byte(`{"kind":"admission","event_id":"` + strings.Repeat("a", 64) + `","grant_id":"` + strings.Repeat("b", 64) + `","tip_hash":"` + strings.Repeat("c", 64) + `","mode":"enforcing","command_id":""}`)}
}

// Claim: the production table stays exactly empty at pin B; admitting even one extra code violates the reviewed pin.
func TestPinnedNoEffectRegistry(t *testing.T) {
	if PinnedNoEffectCodes != "" {
		t.Fatal("pin B has no published no-effect entries: table was widened")
	}
	q := Request{ID: strings.Repeat("1", 32), TimeoutMS: 100, Method: MethodBind, Bind: func() *BindRequest { b := policyBind(); return &b }()}
	raw := policyFrame(fmt.Sprintf(`{"error":{"code":"role_not_permitted","retryable":false},"request_id":%q}`, q.ID))
	if _, err := (Pinned{}).DecodeResponse(q, raw); err == nil {
		t.Fatal("production table accepted an out-of-set refusal")
	}
	// Positive control: a test-only table entry reaches the definite branch.
	set := "role_not_permitted"
	if r, err := (Pinned{noEffectCodes: &set}).DecodeResponse(q, raw); err != nil || !r.Refused {
		t.Fatalf("injected table: %+v %v", r, err)
	}
}

// Claim: only correlated injected no-effect refusals exit 16; retryable/out-of-set/null/wrong-ID/malformed refusals exit 17, with one bind only.
func TestPinnedRefusalPolicy(t *testing.T) {
	id := strings.Repeat("1", 32)
	for _, mode := range []string{"success", "in-set", "out-of-set", "retryable", "null", "wrong-id", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			set := ""
			if mode == "in-set" || mode == "null" || mode == "wrong-id" {
				set = "role_not_permitted"
			}
			if mode == "retryable" {
				set = "busy"
			}
			w := Pinned{noEffectCodes: &set}
			done := make(chan error, 1)
			var binds atomic.Int32
			d := policyDialer{done: done, serve: func(peer net.Conn) error {
				_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
				b, err := w.ReadFrame(peer)
				if err != nil {
					return err
				}
				hello, err := wire.DecodeRequestFrame(b)
				if err != nil {
					return err
				}
				version := int64(1)
				b, err = wire.EncodeResponseFrame(hello, wire.Response{ID: hello.ID, Version: &version})
				if err != nil {
					return err
				}
				if err = w.WriteFrame(peer, b); err != nil {
					return err
				}
				b, err = w.ReadFrame(peer)
				if err != nil {
					return err
				}
				q, err := wire.DecodeRequestFrame(b)
				if err != nil || q.Method != wire.BindMethod {
					return errors.New("not one valid bind")
				}
				binds.Add(1)
				code, retry := "role_not_permitted", false
				responseID, _ := json.Marshal(q.ID)
				if mode == "retryable" {
					code, retry = "busy", true
				}
				if mode == "null" {
					responseID = []byte("null")
				}
				if mode == "wrong-id" {
					responseID, _ = json.Marshal(strings.Repeat("2", 32))
				}
				body := fmt.Sprintf(`{"error":{"code":%q,"retryable":%t},"request_id":%s}`, code, retry, responseID)
				if mode == "success" {
					body = fmt.Sprintf(`{"request_id":%q,"result":{"run_id":"%s","state":"live"}}`, q.ID, strings.Repeat("3", 32))
				}
				if mode == "malformed" {
					body = `{"result":`
				}
				if err = w.WriteFrame(peer, policyFrame(body)); err != nil {
					return err
				}
				// Closing the client must reveal EOF, not a second request after refusal.
				var one [1]byte
				n, err := peer.Read(one[:])
				if n != 0 || err == nil {
					return errors.New("second bind after refusal")
				}
				if !errors.Is(err, io.EOF) {
					return err
				}
				return nil
			}}
			conn, err := Open(context.Background(), d, w, 100*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = conn.Bind(id, policyBind(), 3)
			if mode == "success" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				want, status := codes.BindUnknown, 17
				if mode == "in-set" {
					want, status = codes.BindRefused, 16
				}
				v, e := codes.Exit(err, 0, true)
				if !codes.Is(err, want) || e != nil || v.Status != status {
					t.Fatalf("%v verdict %+v %v", err, v, e)
				}
			}
			if conn.Requests() != 1 || binds.Load() != 1 {
				t.Fatal("bind was repeated after an outcome")
			}
			_ = conn.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("policy peer did not stop")
			}
		})
	}
}
