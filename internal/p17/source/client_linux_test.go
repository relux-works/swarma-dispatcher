//go:build linux

package source_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
)

var uid = uint32(os.Getuid())

type env struct {
	root, dir string
	script    *fakes.SourceScript
	dialer    *fakes.SourceDialer
	client    *source.Client
}

func newEnv(t *testing.T, script *fakes.SourceScript) *env {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, stop, err := fakes.ControlDir(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	if script == nil {
		script = &fakes.SourceScript{}
	}
	d := &fakes.SourceDialer{Script: script, Peer: source.Credential{UID: uid, PID: 1}}
	c := &source.Client{
		Pins:        source.Pins{Root: root, ControlDir: dir, SourceUID: uid, SourceGeneration: "gen-a"},
		Dialer:      d,
		Wire:        fakes.SourceWire{},
		Generations: source.StaticGenerations{uid: "gen-a"},
		Budget:      500 * time.Millisecond,
	}
	return &env{root: root, dir: dir, script: script, dialer: d, client: c}
}

func wantCode(t *testing.T, err error, code codes.Code) {
	t.Helper()
	if !codes.Is(err, code) {
		t.Fatalf("want %s, got %v", code, err)
	}
}

// Claim (H1, H2 step 1, positive control): with a correct socket path, peer,
// generation and capability, prepare then one validate on the same
// connection succeed; the validated prepared_id and incarnation equal the
// hello and prepare, the origin bytes are exactly the fake's, and exactly
// one connection was used.
func TestPrepareValidateHappy(t *testing.T) {
	e := newEnv(t, nil)
	p, err := e.client.Prepare(context.Background(), "nonce-1")
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceIncarnation() != "inc-1" || p.PreparedID() != "prep-1" || p.Peer().UID != uid {
		t.Fatalf("prepared %+v", p)
	}
	v, err := p.Validate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.PreparedID != "prep-1" || v.SourceIncarnation != "inc-1" || string(v.Origin) != string(fakes.ValidOrigin()) || v.Bindings["station"] != "station-1" {
		t.Fatalf("validated %+v", v)
	}
	dials, paths := e.dialer.Dials()
	if dials != 1 || paths[0] != filepath.Join(e.dir, source.SocketName) {
		t.Fatalf("dials %d paths %v", dials, paths)
	}
	if ids := e.script.ValidatedIDs(); len(ids) != 1 || ids[0] != "prep-1" {
		t.Fatalf("validated ids %v", ids)
	}
}

// Claim (H2 step 1, "repeated validate"): the prepared record is consumed
// once; a second Validate on the same handle refuses run_origin_refused and
// sends nothing more to Source.
func TestValidateRepeatedRefused(t *testing.T) {
	e := newEnv(t, nil)
	p, err := e.client.Prepare(context.Background(), "nonce-1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, err = p.Validate(context.Background())
	wantCode(t, err, codes.OriginRefused)
	if ids := e.script.ValidatedIDs(); len(ids) != 1 {
		t.Fatalf("second validate reached Source: %v", ids)
	}
}

// Claim (Controls, "no capability -> refused"; H1: origin_not_supported is
// never unavailable): a hello without the capability refuses
// run_origin_refused before prepare, and an explicit origin_not_supported on
// prepare also refuses definitely. Positive control: TestPrepareValidateHappy.
func TestNoCapabilityRefused(t *testing.T) {
	e := newEnv(t, &fakes.SourceScript{NoCapability: true})
	_, err := e.client.Prepare(context.Background(), "n")
	wantCode(t, err, codes.OriginRefused)
	if _, prepares, _, _ := e.script.Counts(); prepares != 0 {
		t.Fatal("prepare sent without capability")
	}
	e = newEnv(t, &fakes.SourceScript{PrepareMode: "not-supported"})
	_, err = e.client.Prepare(context.Background(), "n")
	wantCode(t, err, codes.OriginRefused)
	if !errors.Is(err, source.ErrNotSupported) {
		t.Fatalf("cause lost: %v", err)
	}
}

// Claim (H1 budget): an explicit dependency failure, a dropped prepare
// response and a failed connect are run_origin_unavailable (retryable), and
// the dropped case returns inside the budget.
func TestPrepareUnavailable(t *testing.T) {
	for _, mode := range []string{"dependency", "drop", "close"} {
		e := newEnv(t, &fakes.SourceScript{PrepareMode: mode})
		start := time.Now()
		_, err := e.client.Prepare(context.Background(), "n")
		wantCode(t, err, codes.OriginUnavailable)
		if mode == "drop" && time.Since(start) > 3*time.Second {
			t.Fatalf("drop exceeded the budget")
		}
	}
	e := newEnv(t, nil)
	e.dialer.Fail = errors.New("connect refused")
	_, err := e.client.Prepare(context.Background(), "n")
	wantCode(t, err, codes.OriginUnavailable)
}

// Claim (Controls, "wrong peer UID or generation"): a kernel peer uid other
// than the pinned A, an unreadable peer credential, a pinned generation that
// is not the current authoritative one, and an unpinned generation reader
// each refuse run_origin_refused; the generation checks make no connection.
func TestWrongPeerOrGeneration(t *testing.T) {
	e := newEnv(t, nil)
	e.dialer.Peer = source.Credential{UID: uid + 1}
	_, err := e.client.Prepare(context.Background(), "n")
	wantCode(t, err, codes.OriginRefused)
	if _, prepares, _, _ := e.script.Counts(); prepares != 0 {
		t.Fatal("prepare sent to a wrong peer")
	}
	e = newEnv(t, nil)
	e.dialer.PeerErr = errors.New("no credential")
	_, err = e.client.Prepare(context.Background(), "n")
	wantCode(t, err, codes.OriginRefused)

	e = newEnv(t, nil)
	e.client.Generations = source.StaticGenerations{uid: "gen-b"}
	_, err = e.client.Prepare(context.Background(), "n")
	wantCode(t, err, codes.OriginRefused)
	if dials, _ := e.dialer.Dials(); dials != 0 {
		t.Fatal("connected despite a generation mismatch")
	}
	e = newEnv(t, nil)
	e.client.Generations = nil
	_, err = e.client.Prepare(context.Background(), "n")
	wantCode(t, err, codes.OriginRefused)
	if !codes.IsUnpinned(err) {
		t.Fatalf("unpinned cause lost: %v", err)
	}
	if dials, _ := e.dialer.Dials(); dials != 0 {
		t.Fatal("connected with an unpinned generation reader")
	}
}

// Claim (seam): with the pinned codec absent the default wire refuses by name
// on the first exchange and the connection is closed; no prepare reaches
// Source.
func TestUnpinnedWire(t *testing.T) {
	e := newEnv(t, nil)
	e.client.Wire = nil
	_, err := e.client.Prepare(context.Background(), "n")
	wantCode(t, err, codes.OriginRefused)
	if !codes.IsUnpinned(err) {
		t.Fatalf("not an unpinned refusal: %v", err)
	}
	if _, prepares, _, _ := e.script.Counts(); prepares != 0 {
		t.Fatal("prepare sent through an unpinned wire")
	}
}

// Claim (H2 steps 1-2, "incarnation replay across a Source restart"; byte-
// equal recheck): a validate response carrying another source_incarnation,
// another prepared_id, a drifted binding, an extra binding, a malformed
// origin or a Matrix-ID origin refuses run_origin_refused; an explicit
// Source refusal of validate is also definite.
func TestValidateMismatchesRefused(t *testing.T) {
	for _, mode := range []string{"other-incarnation", "other-prepared", "binding-drift", "extra-binding", "bad-origin", "matrix-origin", "refused"} {
		e := newEnv(t, &fakes.SourceScript{ValidateMode: mode})
		p, err := e.client.Prepare(context.Background(), "n")
		if err != nil {
			t.Fatalf("%s: prepare %v", mode, err)
		}
		_, err = p.Validate(context.Background())
		if !codes.Is(err, codes.OriginRefused) || source.IsRestart(err) {
			t.Fatalf("%s: %v", mode, err)
		}
	}
}

// Claim (H2 step 2, "lost prepare or validate leads to a fresh prepare"): a
// dropped validate response, a connection closed before the validate reply,
// and an explicit dependency failure on validate each return Restart, with
// the handle consumed so the old prepared_id cannot be validated again.
func TestValidateLostRestarts(t *testing.T) {
	for _, mode := range []string{"drop", "close", "dependency"} {
		e := newEnv(t, &fakes.SourceScript{ValidateMode: mode})
		p, err := e.client.Prepare(context.Background(), "n")
		if err != nil {
			t.Fatalf("%s: prepare %v", mode, err)
		}
		_, err = p.Validate(context.Background())
		if !source.IsRestart(err) {
			t.Fatalf("%s: want restart, got %v", mode, err)
		}
		_, err = p.Validate(context.Background())
		wantCode(t, err, codes.OriginRefused)
	}
}

// Claim (H1 step 1): the control socket check accepts the A-owned 0711
// directory with a 0666 non-symlink socket, and refuses each listed
// deviation without repairing it: wrong directory mode, a symlinked socket,
// a regular file, wrong socket mode, a writable ancestor, a symlinked
// control directory, a missing socket, and a wrong owner pin.
func TestCheckControlSocket(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dir, stop, err := fakes.ControlDir(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	sock := filepath.Join(dir, source.SocketName)
	if got, err := source.CheckControlSocket(root, dir, uid); err != nil || got != sock {
		t.Fatalf("positive control: %q %v", got, err)
	}
	refuse := func(name string) {
		t.Helper()
		if _, err := source.CheckControlSocket(root, dir, uid); !errors.Is(err, source.ErrSocketPath) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Chmod(dir, 0o755))
	refuse("dir mode 0755")
	must(os.Chmod(dir, 0o711))
	must(os.Chmod(sock, 0o600))
	refuse("socket mode 0600")
	must(os.Chmod(sock, 0o666))
	if _, err := source.CheckControlSocket(root, dir, uid+1); !errors.Is(err, source.ErrSocketPath) {
		t.Fatalf("wrong owner pin: %v", err)
	}
	// Symlinked socket.
	must(os.Rename(sock, sock+".real"))
	must(os.Symlink(sock+".real", sock))
	refuse("symlinked socket")
	must(os.Remove(sock))
	refuse("missing socket")
	must(os.WriteFile(sock, []byte("x"), 0o666))
	must(os.Chmod(sock, 0o666))
	refuse("regular file")
	must(os.Remove(sock))
	must(os.Rename(sock+".real", sock))
	if _, err := source.CheckControlSocket(root, dir, uid); err != nil {
		t.Fatalf("restored: %v", err)
	}
	// Writable ancestor between root and dir.
	deep := filepath.Join(root, "shared")
	must(os.Mkdir(deep, 0o777))
	must(os.Chmod(deep, 0o777))
	must(os.Rename(dir, filepath.Join(deep, "control")))
	if _, err := source.CheckControlSocket(root, filepath.Join(deep, "control"), uid); !errors.Is(err, source.ErrSocketPath) {
		t.Fatalf("writable ancestor: %v", err)
	}
	must(os.Chmod(deep, 0o755))
	if _, err := source.CheckControlSocket(root, filepath.Join(deep, "control"), uid); err != nil {
		t.Fatalf("protected ancestor: %v", err)
	}
	// Symlinked control directory.
	link := filepath.Join(root, "linkdir")
	must(os.Symlink(filepath.Join(deep, "control"), link))
	if _, err := source.CheckControlSocket(root, link, uid); !errors.Is(err, source.ErrSocketPath) {
		t.Fatalf("symlinked dir: %v", err)
	}
	if _, err := source.CheckControlSocket(root, root, uid); !errors.Is(err, source.ErrSocketPath) {
		t.Fatal("dir equal to root accepted")
	}
	if _, err := source.CheckControlSocket("relative", dir, uid); !errors.Is(err, source.ErrSocketPath) {
		t.Fatal("relative root accepted")
	}
}

// Claim (H1 step 2, positive control for the real kernel path): over a real
// unix socket the UnixDialer reads SO_PEERCRED, the pinned uid matches the
// test process that serves the socket, and prepare plus validate succeed.
func TestRealUnixDialerPeer(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	script := &fakes.SourceScript{}
	dir, stop, err := fakes.ControlDir(root, script)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	c := &source.Client{
		Pins:        source.Pins{Root: root, ControlDir: dir, SourceUID: uid, SourceGeneration: "g"},
		Dialer:      source.UnixDialer{Timeout: time.Second},
		Wire:        fakes.SourceWire{},
		Generations: source.StaticGenerations{uid: "g"},
	}
	p, err := c.Prepare(context.Background(), "n")
	if err != nil {
		t.Fatal(err)
	}
	if p.Peer().UID != uid || p.Peer().PID != int32(os.Getpid()) {
		t.Fatalf("peer %+v", p.Peer())
	}
	if _, err := p.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
