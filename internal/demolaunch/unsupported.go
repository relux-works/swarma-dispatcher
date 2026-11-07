//go:build !linux

package demolaunch

func Daemon(string) error                           { return ErrUnsupported }
func Client(string, string, []byte) (string, error) { return "", ErrUnsupported }
