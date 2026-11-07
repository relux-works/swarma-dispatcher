package main

import (
	"fmt"
	"github.com/relux-works/swarma-dispatcher/internal/demolaunch"
	"os"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "--config" {
		fmt.Fprintln(os.Stderr, "demo, not production: swarma-demo-dispatcher --config /absolute/server.json")
		os.Exit(1)
	}
	if err := demolaunch.Daemon(os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
