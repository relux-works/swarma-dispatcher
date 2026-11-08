package main

import (
	"errors"
	"fmt"
	"github.com/relux-works/swarma-dispatcher/internal/demolaunch"
	"os"
	"time"
)

const stdinBudget = 5 * time.Second

func main() {
	if len(os.Args) != 4 || os.Args[1] != "--config" || (os.Args[3] != "launch" && os.Args[3] != "stop" && os.Args[3] != "status") {
		fmt.Fprintln(os.Stderr, "demo, not production: swarma-demo-client --config /absolute/client.json launch|stop|status < request.json")
		os.Exit(1)
	}
	b, err := intake(os.Stdin, stdinBudget)
	if err != nil {
		fmt.Fprintln(os.Stderr, "request intake refused:", err)
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

var errIntakeLimit = errors.New("request_too_large")
