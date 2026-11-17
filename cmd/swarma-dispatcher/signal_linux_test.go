//go:build linux

package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

// Claim: must-exit and transport-fatal errors retain the exact operator disposition line, while an ordinary refusal beside them has no fatal hint.
func TestCommandExitHint(t *testing.T) {
	const hint = "journal sync failure: process exit required; recovery is held until host restart or operator disposition\n"
	for _, err := range []error{journal.ErrSyncFailure, transport.ErrFatal, errors.Join(transport.ErrFatal, syscall.EIO), errors.New("ordinary fixture refusal")} {
		var stderr bytes.Buffer
		reportRefusal(&stderr, err)
		wantHint := err != nil && err.Error() != "ordinary fixture refusal"
		if strings.HasPrefix(stderr.String(), hint) != wantHint || !strings.Contains(stderr.String(), "dispatcher refused: "+err.Error()+"\n") {
			t.Fatalf("wrong operator transcript: %q", stderr.String())
		}
	}
}

// Claim: SIGTERM during attach returns nil and closes without serving; the same early context reaches serving, fatal duty still wins, and the unsignaled control serves once.
func TestCommandSignalDuringAttach(t *testing.T) {
	mode := os.Getenv("DISPATCHER_SIGNAL_FIXTURE")
	if mode == "" {
		for _, mode := range []string{"control", "attach", "fatal-attach", "handoff"} {
			t.Run(mode, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandSignalDuringAttach$", "-test.count=1")
				child.Env = append(os.Environ(), "DISPATCHER_SIGNAL_FIXTURE="+mode)
				if output, err := child.CombinedOutput(); err != nil {
					t.Fatalf("signal fixture %s: %v\n%s", mode, err, output)
				}
			})
		}
		return
	}
	waitSignal := func(ctx context.Context) {
		t.Helper()
		if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
			t.Fatal("command did not observe SIGTERM in its startup context")
		}
	}
	server := &fixtureServer{bytes: []byte("server snapshot")}
	ext := &fixtureExtension{bytes: []byte(fixtureM0)}
	calls := commandCalls{
		load: func(string) (serverInstance, error) { return server, nil },
		attach: func(ctx context.Context, _ string, _ func(string) ([]byte, error)) (extensionInstance, error) {
			if mode == "attach" || mode == "fatal-attach" {
				waitSignal(ctx)
				if mode == "fatal-attach" {
					return ext, errors.Join(transport.ErrFatal, context.Canceled)
				}
				return ext, context.Canceled
			}
			return ext, nil
		},
	}
	server.onServe = func(transport.Extension) error {
		if mode == "handoff" {
			waitSignal(server.context)
		} else if server.context.Err() != nil {
			t.Fatal("unsignaled control inherited a canceled context")
		}
		return nil
	}
	var stderr bytes.Buffer
	err := runWith([]string{"--config", "/configuration/server.json", "--m0-config", "/configuration/m0.json"}, io.Discard, &stderr, calls)
	if mode == "fatal-attach" {
		if !errors.Is(err, transport.ErrFatal) {
			t.Fatalf("signal hid fatal duty: %v", err)
		}
	} else if err != nil {
		t.Fatalf("SIGTERM was not graceful: %v", err)
	}
	wantServes := 1
	if mode == "attach" || mode == "fatal-attach" {
		wantServes = 0
		if stderr.Len() != 0 {
			t.Fatal("interrupted attach announced or entered capacity-only serving")
		}
	}
	if server.serves != wantServes || ext.closes != 1 {
		t.Fatalf("signal lifecycle: serves=%d closes=%d", server.serves, ext.closes)
	}
}

// Claim: SIGTERM while the command is blocked inside load is already handled and returns nil without serving, beside an unsignaled load that serves once.
func TestCommandSignalDuringLoad(t *testing.T) {
	mode := os.Getenv("DISPATCHER_LOAD_SIGNAL_FIXTURE")
	if mode != "" {
		server := &fixtureServer{bytes: []byte("server snapshot")}
		calls := commandCalls{load: func(string) (serverInstance, error) {
			fmt.Fprintln(os.Stdout, "load-entered")
			var resume [1]byte
			if _, err := io.ReadFull(os.Stdin, resume[:]); err != nil {
				return nil, err
			}
			return server, nil
		}}
		var stderr bytes.Buffer
		if err := runWith([]string{"--config", "/configuration/server.json"}, io.Discard, &stderr, calls); err != nil {
			t.Fatal(err)
		}
		if mode == "signal" {
			if server.serves != 0 || stderr.Len() != 0 {
				t.Fatal("interrupted load announced or served")
			}
		} else if server.serves != 1 {
			t.Fatal("valid load control did not serve")
		}
		return
	}
	for _, mode := range []string{"control", "signal"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCommandSignalDuringLoad$", "-test.count=1")
			child.Env = append(os.Environ(), "DISPATCHER_LOAD_SIGNAL_FIXTURE="+mode)
			stdout, err := child.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdin, err := child.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			child.Stderr = &stderr
			if err := child.Start(); err != nil {
				t.Fatal(err)
			}
			defer child.Process.Kill()
			line, err := bufio.NewReader(stdout).ReadString('\n')
			if err != nil || line != "load-entered\n" {
				t.Fatalf("load handshake: %q %v", line, err)
			}
			done := make(chan error, 1)
			go func() { done <- child.Wait() }()
			if mode == "signal" {
				if err := child.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					t.Fatalf("SIGTERM terminated during load: %v %s", err, stderr.String())
				case <-time.After(100 * time.Millisecond):
				}
			}
			if _, err := stdin.Write([]byte{1}); err != nil {
				t.Fatal(err)
			}
			_ = stdin.Close()
			if err := <-done; err != nil {
				t.Fatalf("load fixture: %v %s", err, stderr.String())
			}
		})
	}
}
