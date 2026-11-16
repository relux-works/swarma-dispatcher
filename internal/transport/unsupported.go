//go:build !linux

package transport

func Daemon(string) error                   { return ErrUnsupported }
func DaemonWith(string, Extension) error    { return ErrUnsupported }
func Capacity(string) (string, error)       { return "", ErrUnsupported }
func ServerSnapshot(string) ([]byte, error) { return nil, ErrUnsupported }
