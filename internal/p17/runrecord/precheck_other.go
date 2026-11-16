//go:build !linux || (!amd64 && !arm64)

package runrecord

import (
	"errors"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

// precheck refuses on platforms the journal does not support.
func precheck(journal.Config) error {
	return errors.Join(ErrConfig, errors.New("runrecord: the journal does not support this platform"))
}
