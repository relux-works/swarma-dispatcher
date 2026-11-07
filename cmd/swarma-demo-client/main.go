package main

import (
	"fmt"
	"github.com/relux-works/swarma-dispatcher/internal/demolaunch"
	"io"
	"os"
	"time"
)

func main() {
	if len(os.Args) != 4 || os.Args[1] != "--config" || (os.Args[3] != "launch" && os.Args[3] != "stop" && os.Args[3] != "status") {
		fmt.Fprintln(os.Stderr, "demo, not production: swarma-demo-client --config /absolute/client.json launch|stop|status < request.json")
		os.Exit(1)
	}
	// Regular redirected files need no poll deadline; pipes must support one.
	info, err := os.Stdin.Stat()
	if err == nil && !info.Mode().IsRegular() {
		err = os.Stdin.SetReadDeadline(time.Now().Add(5 * time.Second))
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "stdin unavailable:", err)
		os.Exit(1)
	}
	b, err := io.ReadAll(io.LimitReader(os.Stdin, 131073))
	if err != nil || len(b) > 131072 {
		fmt.Fprintln(os.Stderr, "request intake refused")
		os.Exit(1)
	}
	reply, err := demolaunch.Client(os.Args[2], os.Args[3], b)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if _, err = fmt.Fprintln(os.Stdout, reply); err != nil {
		os.Exit(1)
	}
}
