//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/p17/compose"
	"github.com/relux-works/swarma-dispatcher/internal/p17/compose/digest"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

const usage = "usage: swarma-dispatcher [config-digest] --config /absolute/server.json [--m0-config /absolute/m0.json]"

type serverInstance interface {
	Bytes() []byte
	Serve(transport.Extension) error
}
type extensionInstance interface {
	transport.Extension
	ConfigBytes() []byte
	Close() error
}
type loadedServer struct{ snapshot *transport.ServerSnapshot }

func (s loadedServer) Bytes() []byte { return s.snapshot.Bytes() }
func (s loadedServer) Serve(ext transport.Extension) error {
	return transport.DaemonFromSnapshot(s.snapshot, ext)
}

type commandCalls struct {
	load   func(string) (serverInstance, error)
	read   func(string) ([]byte, error)
	attach func(string, func(string) ([]byte, error)) (extensionInstance, error)
}

func productionCalls() commandCalls {
	return commandCalls{
		load: func(path string) (serverInstance, error) {
			s, err := transport.LoadServer(path)
			if err != nil {
				return nil, err
			}
			return loadedServer{s}, nil
		},
		read: transport.ReadProtectedConfig,
		attach: func(path string, read func(string) ([]byte, error)) (extensionInstance, error) {
			s, err := compose.Attach(context.Background(), path, m0.DefaultSeams(), compose.DefaultDeps(), read)
			if err != nil {
				return nil, err
			}
			return s, nil
		},
	}
}

func main() {
	if err := runWith(os.Args[1:], os.Stdout, os.Stderr, productionCalls()); err != nil {
		fmt.Fprintln(os.Stderr, "dispatcher refused:", err)
		os.Exit(1)
	}
}

func runWith(args []string, stdout, stderr io.Writer, calls commandCalls) error {
	digestOnly := len(args) > 0 && args[0] == "config-digest"
	if digestOnly {
		args = args[1:]
	}
	if (len(args) != 2 && len(args) != 4) || args[0] != "--config" || (len(args) == 4 && args[2] != "--m0-config") {
		return errors.New(usage)
	}
	server, err := calls.load(args[1])
	if err != nil {
		return err
	}
	var config []byte
	var attached extensionInstance
	if len(args) == 4 {
		if digestOnly {
			config, err = calls.read(args[3])
			if err != nil {
				return err
			}
			cfg, err := m0.ParseConfig(config)
			if err != nil {
				return err
			}
			if !cfg.Enabled {
				config = nil
			}
		} else {
			attached, err = calls.attach(args[3], calls.read)
			if err != nil {
				if errors.Is(err, transport.ErrFatal) || m0.MustExit(err) || m0.CodeOf(err) != m0.CodeClosed {
					return err
				}
				if _, writeErr := fmt.Fprintln(stderr, "m0 closed, capacity-only:", err); writeErr != nil {
					return writeErr
				}
			}
			if attached != nil {
				config = attached.ConfigBytes()
			}
		}
	}
	if digestOnly {
		_, err := fmt.Fprintln(stdout, compose.ConfigDigest(server.Bytes(), config))
		return err
	}
	var ext transport.Extension
	if attached != nil {
		ext = attached
	}
	err = digest.AnnounceAndServe(server.Bytes(), config, stderr, func() error { return server.Serve(ext) })
	if attached != nil {
		err = errors.Join(err, attached.Close())
	}
	return err
}
