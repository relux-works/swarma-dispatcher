//go:build linux

package demolaunch

import (
	"io"
	"os"
	"strconv"
	"syscall"
)

// Seal supervisor-inherited descriptors before daemon initialization. This only
// changes exec inheritance; it never closes a runtime or supervisor descriptor.
func sealInheritedDescriptors() error {
	return sealDescriptorsIn("/proc/self/fd")
}

func sealDescriptorsIn(directory string) error {
	d, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer d.Close()
	walkFD := int(d.Fd()) // os.Open created this descriptor close-on-exec.
	for {
		names, err := d.Readdirnames(128)
		if err != nil && err != io.EOF {
			return err
		}
		for _, name := range names {
			fd, parseErr := strconv.Atoi(name)
			if parseErr != nil {
				return parseErr
			}
			if fd > 2 && fd != walkFD {
				syscall.CloseOnExec(fd)
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}
