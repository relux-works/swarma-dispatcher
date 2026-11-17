//go:build !linux

package transport

import "context"

func Daemon(string) error                                 { return ErrUnsupported }
func DaemonWith(string, Extension) error                  { return ErrUnsupported }
func Capacity(string) (string, error)                     { return "", ErrUnsupported }
func ReadProtectedConfig(string) ([]byte, error)          { return nil, ErrUnsupported }
func LoadServer(string) (*ServerSnapshot, error)          { return nil, ErrUnsupported }
func DaemonFromSnapshot(*ServerSnapshot, Extension) error { return ErrUnsupported }
func DaemonFromSnapshotContext(context.Context, *ServerSnapshot, Extension) error {
	return ErrUnsupported
}
