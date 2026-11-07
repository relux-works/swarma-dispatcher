//go:build !linux

package main

import (
	"github.com/relux-works/swarma-dispatcher/internal/demolaunch"
	"os"
	"time"
)

func intake(*os.File, time.Duration) ([]byte, error) { return nil, demolaunch.ErrUnsupported }
