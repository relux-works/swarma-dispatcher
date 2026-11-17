//go:build linux

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"syscall"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

const fixtureM0 = `{"callers":[{"dev":1,"dir":"/example/caller","ino":2,"uid":30001}],"enabled":true,"entry":"established","kind":"swarma-dispatcher m0-config/3","p17":{"bind_timeout_ms":1000,"keymaker_gid":30004,"keymaker_mode":432,"keymaker_socket_dir":"/run/keymaker","keymaker_socket_name":"request.sock","keymaker_uid":30003,"recovery_attested":true,"source_control_dir":"/run/source","source_generation":"11111111111111111111111111111111","source_socket_name":"origin.sock","source_uid":30002,"state_dir":"/var/lib/p17","station":"1","supported_local_storage":true},"recovery_attested":true,"state_dir":"/var/lib/m0","supported_local_storage":true,"v":3}`

type fixtureServer struct {
	bytes   []byte
	serves  int
	onServe func(transport.Extension) error
	context context.Context
}

func (s *fixtureServer) Bytes() []byte { return append([]byte(nil), s.bytes...) }
func (s *fixtureServer) Serve(ctx context.Context, e transport.Extension) error {
	s.context = ctx
	s.serves++
	if s.onServe != nil {
		return s.onServe(e)
	}
	return nil
}

type fixtureExtension struct {
	bytes  []byte
	closes int
}

func (*fixtureExtension) ManagedCallers() []transport.Principal { return nil }
func (*fixtureExtension) Serve(uint32, string, []byte) (string, error) {
	return "", transport.ErrInvalid
}
func (e *fixtureExtension) ConfigBytes() []byte { return append([]byte(nil), e.bytes...) }
func (e *fixtureExtension) Close() error        { e.closes++; return nil }

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, syscall.EIO }

func expectedDigest(server, m0bytes []byte) string {
	x := sha256.Sum256(server)
	members := map[string]string{"server": hex.EncodeToString(x[:])}
	if len(m0bytes) != 0 {
		y := sha256.Sum256(m0bytes)
		members["m0"] = hex.EncodeToString(y[:])
	}
	object, err := json.Marshal(members)
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(object)
	return hex.EncodeToString(sum[:])
}

// Claim: command startup hashes exactly its loaded server and attached M0 snapshots once before serving, without a second read, and closes the extension after serving.
func TestCommandSnapshotTranscript(t *testing.T) {
	serverBytes := []byte("server snapshot A")
	m0bytes := []byte(fixtureM0)
	server := &fixtureServer{bytes: serverBytes}
	extension := &fixtureExtension{bytes: m0bytes}
	loads, reads := 0, 0
	var stdout, stderr bytes.Buffer
	calls := commandCalls{load: func(string) (serverInstance, error) { loads++; return server, nil }, read: func(string) ([]byte, error) { reads++; return append([]byte(nil), m0bytes...), nil }, attach: func(_ context.Context, path string, read func(string) ([]byte, error)) (extensionInstance, error) {
		b, err := read(path)
		if err != nil {
			return nil, err
		}
		extension.bytes = b
		return extension, nil
	}}
	server.onServe = func(e transport.Extension) error {
		if e != extension || extension.closes != 0 || stderr.String() != "config-digest "+expectedDigest(serverBytes, []byte(fixtureM0))+"\n" {
			t.Fatal("startup transcript was not from the consumed objects before serve")
		}
		// Simulate later protected replacements without changing either token.
		m0bytes = []byte(strings.Replace(fixtureM0, `"station":"1"`, `"station":"2"`, 1))
		return nil
	}
	if err := runWith([]string{"--config", "/configuration/server.json", "--m0-config", "/configuration/m0.json"}, &stdout, &stderr, calls); err != nil {
		t.Fatal(err)
	}
	if loads != 1 || reads != 1 || server.serves != 1 || extension.closes != 1 || stdout.Len() != 0 {
		t.Fatal("startup reread, duplicated serve or leaked extension")
	}
}

// Claim: config-digest validates the protected M0 grammar once, prints only the correct digest, omits disabled M0 and never attaches or starts a listener; invalid disabled pins refuse beside a valid control.
func TestConfigDigestCommand(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		server := &fixtureServer{bytes: []byte("server snapshot")}
		body := fixtureM0
		if !enabled {
			body = strings.Replace(body, `"enabled":true`, `"enabled":false`, 1)
		}
		loads, reads, attaches := 0, 0, 0
		var stdout, stderr bytes.Buffer
		calls := commandCalls{load: func(string) (serverInstance, error) { loads++; return server, nil }, read: func(string) ([]byte, error) { reads++; return []byte(body), nil }, attach: func(context.Context, string, func(string) ([]byte, error)) (extensionInstance, error) {
			attaches++
			return nil, errors.New("unexpected attach")
		}}
		if err := runWith([]string{"config-digest", "--config", "/configuration/server.json", "--m0-config", "/configuration/m0.json"}, &stdout, &stderr, calls); err != nil {
			t.Fatal(err)
		}
		var m0bytes []byte
		if enabled {
			m0bytes = []byte(body)
		}
		if stdout.String() != expectedDigest(server.bytes, m0bytes)+"\n" || stderr.Len() != 0 || loads != 1 || reads != 1 || attaches != 0 || server.serves != 0 {
			t.Fatal("digest command side effect, reread or unexpected output")
		}
		stdout.Reset()
		body = strings.Replace(body, `"station":"1"`, `"station":"01"`, 1)
		if err := runWith([]string{"config-digest", "--config", "/configuration/server.json", "--m0-config", "/configuration/m0.json"}, &stdout, &stderr, calls); m0.CodeOf(err) != "request_invalid" || stdout.Len() != 0 || server.serves != 0 {
			t.Fatal("invalid pins were silently hashed")
		}
	}
}

// Claim: actual default composition closes M0 by name while command startup remains capacity-only, omits the M0 digest member and never hands a nonnil extension to the listener.
func TestDefaultDaemonCapacityOnly(t *testing.T) {
	server := &fixtureServer{bytes: []byte("server snapshot")}
	calls := productionCalls()
	loads, reads := 0, 0
	calls.load = func(string) (serverInstance, error) { loads++; return server, nil }
	calls.read = func(string) ([]byte, error) { reads++; return []byte(fixtureM0), nil }
	var stdout, stderr bytes.Buffer
	server.onServe = func(e transport.Extension) error {
		if e != nil {
			t.Fatal("default daemon activated M0")
		}
		return nil
	}
	if err := runWith([]string{"--config", "/configuration/server.json", "--m0-config", "/configuration/m0.json"}, &stdout, &stderr, calls); err != nil {
		t.Fatal(err)
	}
	if loads != 1 || reads != 1 || server.serves != 1 || !strings.Contains(stderr.String(), "static gate refused composition") || !strings.HasSuffix(stderr.String(), "config-digest "+expectedDigest(server.bytes, nil)+"\n") {
		t.Fatal("closed defaults lost capacity-only transcript")
	}
	// A selected fixture is handed through; selection itself is not hardcoded off.
	ext := &fixtureExtension{bytes: []byte(fixtureM0)}
	calls.attach = func(context.Context, string, func(string) ([]byte, error)) (extensionInstance, error) {
		return ext, nil
	}
	server.onServe = func(e transport.Extension) error {
		if e != ext {
			t.Fatal("selected fixture was not handed through")
		}
		return nil
	}
	if err := runWith([]string{"--config", "/configuration/server.json", "--m0-config", "/configuration/m0.json"}, io.Discard, io.Discard, calls); err != nil {
		t.Fatal(err)
	}
}

// Claim: startup fatal duty wins over a joined m0_closed and write failure prevents listening, while the selected valid fixture beside each failure serves and closes once.
func TestCommandFatalAndTranscriptFailure(t *testing.T) {
	for _, kind := range []string{"fatal", "write"} {
		server := &fixtureServer{bytes: []byte("server snapshot")}
		ext := &fixtureExtension{bytes: []byte(fixtureM0)}
		calls := commandCalls{load: func(string) (serverInstance, error) { return server, nil }, attach: func(context.Context, string, func(string) ([]byte, error)) (extensionInstance, error) {
			return ext, nil
		}}
		var stderr io.Writer = io.Discard
		if kind == "fatal" {
			calls.attach = func(context.Context, string, func(string) ([]byte, error)) (extensionInstance, error) {
				return nil, errors.Join(transport.ErrFatal, syscall.EIO, &m0.Refusal{Code: m0.CodeClosed})
			}
		} else {
			stderr = failedWriter{}
		}
		err := runWith([]string{"--config", "/configuration/server.json", "--m0-config", "/configuration/m0.json"}, io.Discard, stderr, calls)
		if !errors.Is(err, syscall.EIO) || server.serves != 0 {
			t.Fatalf("startup failure reached serve: %v", err)
		}
		if kind == "fatal" && !errors.Is(err, transport.ErrFatal) {
			t.Fatal("fatal joined closed became capacity-only")
		}
		if kind == "write" && ext.closes != 1 {
			t.Fatal("failed transcript leaked extension")
		}
		ext.closes = 0
		calls.attach = func(context.Context, string, func(string) ([]byte, error)) (extensionInstance, error) {
			return ext, nil
		}
		if err := runWith([]string{"--config", "/configuration/server.json", "--m0-config", "/configuration/m0.json"}, io.Discard, io.Discard, calls); err != nil || server.serves != 1 || ext.closes != 1 {
			t.Fatalf("valid startup control: %v", err)
		}
	}
}
