package main

import (
	"fmt"
	"os"

	"github.com/relux-works/swarma-dispatcher/internal/m0"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
)

const usage = "usage: swarma-dispatcher --config /absolute/server.json [--m0-config /absolute/m0.json]"

func main() {
	var configPath, m0Path string
	switch {
	case len(os.Args) == 3 && os.Args[1] == "--config":
		configPath = os.Args[2]
	case len(os.Args) == 5 && os.Args[1] == "--config" && os.Args[3] == "--m0-config":
		configPath, m0Path = os.Args[2], os.Args[4]
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}
	// The M0 activation gate: without --m0-config nothing M0 is loaded. With
	// it, the extension attaches only when the configuration selects M0 and
	// the policy source reports an installed policy; the default seams refuse,
	// so the daemon stays capacity-only and says why.
	var ext transport.Extension
	var attached *m0.Extension
	if m0Path != "" {
		x, err := m0.Attach(m0Path, m0.DefaultSeams(), transport.ServerSnapshot)
		switch {
		case err == nil:
			attached, ext = x, x
		case m0.CodeOf(err) == m0.CodeClosed:
			fmt.Fprintln(os.Stderr, "m0 closed, capacity-only:", err)
		default:
			fmt.Fprintln(os.Stderr, "dispatcher refused:", err)
			os.Exit(1)
		}
	}
	err := transport.DaemonWith(configPath, ext)
	if attached != nil {
		if closeErr := attached.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "dispatcher refused:", err)
		if m0.MustExit(err) {
			fmt.Fprintln(os.Stderr, "journal sync failure: process exit required; recovery is held until host restart or operator disposition")
		}
		os.Exit(1)
	}
}
