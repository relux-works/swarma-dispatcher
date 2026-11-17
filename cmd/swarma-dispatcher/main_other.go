//go:build !linux

package main

import (
	"fmt"
	"github.com/relux-works/swarma-dispatcher/internal/transport"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "dispatcher refused:", transport.ErrUnsupported)
	os.Exit(1)
}
