//go:build linux && (amd64 || arm64)

package runrecord

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/relux-works/swarma-dispatcher/internal/journal"
)

// journalName is the journal file basename inside Config.Directory.
const journalName = "journal"

func isDir(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() || st.Uid != uint32(os.Geteuid()) || st.Mode&07777 != 0700 {
		return errors.New("runrecord: directory must be real, private and launcher-owned")
	}
	return nil
}

// precheck validates a fresh store's configuration by reading only: nothing
// is created, written, locked or repaired.
func precheck(cfg journal.Config) error {
	bad := func(what string, cause error) error {
		return errors.Join(ErrConfig, errors.New("runrecord: "+what), cause)
	}
	if cfg.DeclaredUmask == nil || *cfg.DeclaredUmask&0600 != 0 || cfg.ConsumerBudget == 0 || cfg.ConsumerBudget > uint64(^uint(0)>>1) || !cfg.SupportedLocalStorage {
		return bad("declared umask, consumer budget and the local storage attestation are required", nil)
	}
	base := func(s string) bool {
		return s != "" && len(s) <= 255 && s != "." && s != ".." && !strings.ContainsAny(s, "/\x00") && !strings.HasPrefix(s, ".record-tmp-")
	}
	if !filepath.IsAbs(cfg.Directory) || filepath.Clean(cfg.Directory) != cfg.Directory || strings.ContainsRune(cfg.Directory, 0) || !base(cfg.Records) || !base(cfg.Inputs) || cfg.Records == cfg.Inputs {
		return bad("directory, records and inputs paths are required and must be clean", nil)
	}
	if err := isDir(cfg.Directory); err != nil {
		return bad("state directory", err)
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(cfg.Directory, &st); err != nil {
		return bad("state directory filesystem unreadable", err)
	}
	if st.Type != 0xEF53 && st.Type != 0x58465342 {
		return bad("state directory filesystem is neither ext4 nor XFS", nil)
	}
	for _, n := range []string{cfg.Records, cfg.Inputs} {
		if err := isDir(filepath.Join(cfg.Directory, n)); err != nil {
			return bad("records or inputs directory", err)
		}
	}
	if _, err := os.Lstat(filepath.Join(cfg.Directory, journalName)); err == nil {
		return bad("a journal already exists in the state directory; an established store is opened with Open", nil)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return bad("journal path unreadable", err)
	}
	return nil
}
