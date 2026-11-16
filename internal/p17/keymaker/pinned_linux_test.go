//go:build linux

package keymaker_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/keymaker"
	wire "github.com/relux-works/swarma-keymaker/j7lifecycle"
)

const vectorID = "00000000000000000000000000000001"
const vectorRun = "11111111111111111111111111111111"

type vector struct {
	Name, Direction, Context, Body, Verdict, Error string
	FrameHex                                       string `json:"frame_hex"`
}
type lengthVector struct {
	Name, Error string
	Header      string `json:"header_hex"`
	Length      int
}
type corpus struct {
	Cases   []vector
	Lengths []lengthVector
}

func vectors(t *testing.T) corpus {
	t.Helper()
	b, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	if len(b) != keymaker.VectorsSize || hex.EncodeToString(sum[:]) != keymaker.VectorsSHA256 {
		t.Fatal("pinned corpus digest mismatch")
	}
	var c corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	return c
}
func (c corpus) named(t *testing.T, name string) []byte {
	t.Helper()
	for _, v := range c.Cases {
		if v.Name == name {
			b, e := hex.DecodeString(v.FrameHex)
			if e != nil {
				t.Fatal(e)
			}
			return b
		}
	}
	t.Fatalf("missing vector %s", name)
	return nil
}
func vectorRequest(kind string) wire.Request {
	q := wire.Request{ID: vectorID, Method: wire.HelloMethod, Versions: []int64{1}}
	switch kind {
	case "hello":
	case "hello-no-v1":
		q.Versions = []int64{2}
	case "bind", "child", "owner":
		q.Method, q.Versions, q.TimeoutMS = wire.BindMethod, nil, 2000
		q.Bind = &wire.BindParams{IdentityRegistrationNumber: 1, InputPrincipal: wire.Principal{UID: 22001, Generation: "input-generation-1"}, Origin: &wire.Origin{Kind: "admission", EventID: strings.Repeat("a", 64), GrantID: strings.Repeat("b", 64), TipHash: strings.Repeat("c", 64), Mode: "enforcing"}}
		if kind == "child" {
			q.Bind.Origin = nil
			q.Bind.ParentRun = vectorRun
		}
		if kind == "owner" {
			q.Bind.Origin = &wire.Origin{Kind: "owner_command", CommandID: strings.Repeat("d", 64)}
		}
	case "end", "rebind":
		q.Method, q.Versions, q.TimeoutMS, q.RunID = wire.EndMethod, nil, 2000, vectorRun
		if kind == "rebind" {
			q.Method = wire.RebindMethod
		}
	default:
		panic("unknown corpus context")
	}
	return q
}
func localRequest(kind string) keymaker.Request {
	q := vectorRequest(kind)
	out := keymaker.Request{ID: q.ID, TimeoutMS: q.TimeoutMS, Method: keymaker.Method(q.Method)}
	if q.Bind != nil {
		b := q.Bind
		out.Bind = &keymaker.BindRequest{IdentityRegistrationNumber: b.IdentityRegistrationNumber, InputPrincipal: b.InputPrincipal, ParentRun: b.ParentRun}
		if b.Origin != nil {
			out.Bind.Origin = fakes.ValidOrigin()
		}
	} else if q.RunID != "" {
		out.End = &keymaker.EndRequest{RunID: q.RunID}
	}
	return out
}
func frameBytes(body string) []byte {
	b := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(b, uint32(len(body)))
	copy(b[4:], body)
	return b
}

// Claim: every literal corpus case and length class retains its pinned codec verdict and error category.
func TestCorpusCodecClasses(t *testing.T) {
	c := vectors(t)
	sentinels := map[string]error{"request": wire.ErrRequest, "response": wire.ErrResponse, "canonical": wire.ErrCanonical, "frame_malformed": wire.ErrFrameMalformed, "frame_too_large": wire.ErrFrameTooLarge}
	for _, v := range c.Cases {
		t.Run(v.Name, func(t *testing.T) {
			b, err := hex.DecodeString(v.FrameHex)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(b, frameBytes(v.Body)) {
				t.Fatal("literal body/frame mismatch")
			}
			if v.Direction == "request" {
				_, err = wire.DecodeRequestFrame(b)
			} else {
				_, err = wire.DecodeResponseFrame(vectorRequest(v.Context), b)
			}
			if v.Verdict == "accept" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, sentinels[v.Error]) || sentinels[v.Error] == nil {
				t.Fatalf("want %s, got %v", v.Error, err)
			}
		})
	}
	for _, v := range c.Lengths {
		t.Run("length-"+v.Name, func(t *testing.T) {
			b, err := hex.DecodeString(v.Header)
			if err != nil {
				t.Fatal(err)
			}
			n, err := wire.FrameLength([4]byte(b))
			if n != v.Length || (v.Error == "" && err != nil) || (v.Error != "" && !errors.Is(err, sentinels[v.Error])) {
				t.Fatalf("length %d error %v", n, err)
			}
		})
	}
}

type vectorDialer struct {
	serve func(net.Conn) error
	done  chan error
	dials int
}

func (d *vectorDialer) Dial(context.Context) (net.Conn, error) {
	d.dials++
	a, b, err := fakes.SocketPair()
	if err != nil {
		return nil, err
	}
	go func() { defer b.Close(); _ = b.SetDeadline(time.Now().Add(4 * time.Second)); d.done <- d.serve(b) }()
	return a, nil
}
func openVector(t *testing.T, c corpus, reply, want []byte) (*keymaker.Conn, *vectorDialer) {
	t.Helper()
	return openVectorAt(t, c, reply, want, 2*time.Second)
}
func openVectorAt(t *testing.T, c corpus, reply, want []byte, timeout time.Duration) (*keymaker.Conn, *vectorDialer) {
	t.Helper()
	w := keymaker.Pinned{}
	d := &vectorDialer{done: make(chan error, 1)}
	d.serve = func(peer net.Conn) error {
		hello, err := w.ReadFrame(peer)
		if err != nil {
			return err
		}
		q, err := wire.DecodeRequestFrame(hello)
		if err != nil || q.Method != wire.HelloMethod {
			return fmt.Errorf("hello: %w", err)
		}
		// Only correlation is substituted in this literal hello-success vector.
		response := bytes.ReplaceAll(c.named(t, "hello-success"), []byte(vectorID), []byte(q.ID))
		if err = w.WriteFrame(peer, response); err != nil {
			return err
		}
		got, err := w.ReadFrame(peer)
		if err != nil {
			return err
		}
		if !bytes.Equal(got, want) {
			return errors.New("client request differs from literal corpus request")
		}
		_, err = peer.Write(reply)
		return err
	}
	conn, err := keymaker.Open(context.Background(), d, w, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		select {
		case err := <-d.done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("corpus peer did not stop")
		}
	})
	return conn, d
}

// Claim: literal corpus bind/child/end successes traverse a socket pair and match the client's exact encoded request.
func TestCorpusClientPositive(t *testing.T) {
	c := vectors(t)
	for _, kind := range []string{"bind", "child", "end"} {
		t.Run(kind, func(t *testing.T) {
			reqName, replyName := "admission-bind", "bind-success"
			if kind == "child" {
				reqName, replyName = "inherited-child", "child-success"
			}
			if kind == "end" {
				reqName, replyName = "end", "end-success"
			}
			conn, d := openVector(t, c, c.named(t, replyName), c.named(t, reqName))
			q := localRequest(kind)
			if q.Bind != nil {
				res, _, err := conn.Bind(q.ID, *q.Bind)
				if err != nil || res.RunID != vectorRun {
					t.Fatalf("%+v %v", res, err)
				}
			} else {
				res, _, err := conn.End(q.ID, *q.End)
				if err != nil || !res.Ended || res.RunID != vectorRun {
					t.Fatalf("%+v %v", res, err)
				}
			}
			if d.dials != 1 || conn.Requests() != 1 {
				t.Fatal("unexpected reconnect or retry")
			}
		})
	}
}

// Claim: invalid, mismatched-ID, noncanonical and truncated replies never acknowledge a mutation; adjacent literal successes do.
func TestCorpusClientRefusals(t *testing.T) {
	c := vectors(t)
	names := []string{"end-success", "bind-success", "wrong-response-id", "wrong-end-state", "wrong-end-run", "unexpected-method-echo", "empty-string-id", "wrong-retry-bit", "launcher-code-not-native", "rebind-success-as-end", "state-fenced", "wrong-role", "busy-unattributed", "deadline-correlated", "deadline-unattributed", "framing-unattributed", "noncanonical-space", "truncated"}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			kind, reqName := "end", "end"
			if name == "bind-success" || name == "wrong-retry-bit" || name == "launcher-code-not-native" || name == "wrong-role" || name == "busy-unattributed" || name == "deadline-correlated" {
				kind, reqName = "bind", "admission-bind"
			}
			replyName := name
			if name == "truncated" {
				replyName = "end-success"
			}
			reply := c.named(t, replyName)
			if name == "truncated" {
				reply = reply[:len(reply)-1]
			}
			conn, d := openVector(t, c, reply, c.named(t, reqName))
			q := localRequest(kind)
			var err error
			if q.Bind != nil {
				_, _, err = conn.Bind(q.ID, *q.Bind)
			} else {
				_, _, err = conn.End(q.ID, *q.End)
			}
			switch name {
			case "end-success", "bind-success":
				if err != nil {
					t.Fatal(err)
				}
			default:
				want := codes.EndUnknown
				if kind == "bind" {
					want = codes.BindUnknown
				}
				if !codes.Is(err, want) {
					t.Fatalf("want %s, got %v", want, err)
				}
				v, e := codes.Exit(err, 0, true)
				if e != nil || v.Status != 17 {
					t.Fatalf("exit %+v %v", v, e)
				}
			}
			if d.dials != 1 {
				t.Fatal("ambiguous response caused reconnect")
			}
		})
	}
}

// Claim: the client's frame reader accepts bounded complete frames, rejects invalid lengths before body reads, and treats started-frame loss as gone.
func TestPinnedFrameBounds(t *testing.T) {
	w := keymaker.Pinned{}
	for _, n := range []int{1, 65536} {
		b := frameBytes(strings.Repeat("x", n))
		got, err := w.ReadFrame(bytes.NewReader(b))
		if err != nil || !bytes.Equal(got, b) {
			t.Fatal("positive frame control", err)
		}
	}
	for _, h := range []uint32{0, 65537, ^uint32(0)} {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], h)
		if _, err := w.ReadFrame(bytes.NewReader(b[:])); err == nil {
			t.Fatal("bad length accepted")
		}
	}
	for _, b := range [][]byte{{0, 0}, frameBytes("{}")[0:5]} {
		if _, err := w.ReadFrame(bytes.NewReader(b)); !errors.Is(err, keymaker.ErrConnectionGone) {
			t.Fatalf("partial frame: %v", err)
		}
	}
	if _, err := w.ReadFrame(bytes.NewReader(nil)); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}
