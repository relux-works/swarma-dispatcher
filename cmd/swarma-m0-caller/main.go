// Command swarma-m0-caller builds, signs, stores and sends one DRAFT M0
// dispatch or cancel request, or sends a stored one again. See
// internal/m0client for the contract and the exit statuses.
package main

import (
	"os"

	"github.com/relux-works/swarma-dispatcher/internal/m0client"
)

func main() {
	os.Exit(m0client.Run(os.Args[1:], os.Stdout, os.Stderr))
}
