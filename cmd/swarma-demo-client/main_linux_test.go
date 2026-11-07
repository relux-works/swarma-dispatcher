//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Run the actual CLI entrypoint in a child. A valid document must advance to
// protected-config validation, while invalid intake must stop before it.
// A root-owned config / deployed endpoint positive exchange remains unqualified.
func TestCLIChild(t *testing.T) {
	if os.Getenv("DEMO_CLI_CHILD") != "1" {
		return
	}
	os.Args = []string{"swarma-demo-client", "--config", os.Getenv("DEMO_CLI_CONFIG"), "status"}
	main()
	os.Exit(0)
}
func TestCLIStdin(t *testing.T) {
	for _, variant := range []string{"pipe", "file", "too-large", "stalled", "invalid"} {
		t.Run(variant, func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			c := exec.CommandContext(ctx, exe, "-test.run=^TestCLIChild$")
			// A relative config deterministically returns config_invalid after intake.
			c.Env = []string{"DEMO_CLI_CHILD=1", "DEMO_CLI_CONFIG=relative", "GORACE=atexit_sleep_ms=0"}
			payload := []byte(`{"op":"demo.status","account":"demo"}`)
			if variant == "too-large" {
				payload = bytes.Repeat([]byte{'x'}, 131073)
			}
			if variant == "invalid" {
				payload = []byte(`{}`)
			}
			var held *os.File
			switch variant {
			case "file":
				name := filepath.Join(t.TempDir(), "request")
				if err = os.WriteFile(name, payload, 0600); err != nil {
					t.Fatal(err)
				}
				f, e := os.Open(name)
				if e != nil {
					t.Fatal(e)
				}
				defer f.Close()
				c.Stdin = f
			case "stalled":
				r, w, e := os.Pipe()
				if e != nil {
					t.Fatal(e)
				}
				defer r.Close()
				defer w.Close()
				c.Stdin = r
				held = w
			default:
				c.Stdin = bytes.NewReader(payload)
			}
			start := time.Now()
			out, err := c.CombinedOutput()
			elapsed := time.Since(start)
			if err == nil {
				t.Fatal("CLI unexpectedly accepted", string(out))
			}
			if held != nil {
				held.Close()
			}
			expected := "config_invalid"
			if variant == "too-large" {
				expected = "request_too_large"
			}
			if variant == "stalled" {
				expected = "i/o timeout"
			}
			if variant == "invalid" {
				expected = "request_invalid"
			}
			if !strings.Contains(string(out), expected) {
				t.Fatalf("stage=%s output=%q err=%v", variant, out, err)
			}
			if variant == "stalled" && elapsed < 5*time.Second {
				t.Fatal("deadline not five seconds", elapsed)
			}
			if ctx.Err() != nil {
				t.Fatal("CLI exceeded outer safety deadline", ctx.Err())
			}
		})
	}
}

// Byte-bound positive neighbors catch an intake implementation that rejects all
// pipes/files. The pipe writer closes to deliver EOF; the CLI stalled case
// separately exercises the deadline with its writer held open.
func TestIntakeBounds(t *testing.T) {
	for _, size := range []int{0, 131072, 131073} {
		name := filepath.Join(t.TempDir(), "input")
		want := bytes.Repeat([]byte{'x'}, size)
		if err := os.WriteFile(name, want, 0600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		got, err := intake(f, 5*time.Second)
		f.Close()
		if size <= 131072 {
			if err != nil || !bytes.Equal(got, want) {
				t.Fatal(size, len(got), err)
			}
		} else if !errors.Is(err, errIntakeLimit) {
			t.Fatal(size, err)
		}
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	if _, err = w.Write([]byte("pipe control")); err != nil {
		t.Fatal(err)
	}
	w.Close()
	got, err := intake(r, 5*time.Second)
	if err != nil || string(got) != "pipe control" {
		t.Fatal(string(got), err)
	}
}
