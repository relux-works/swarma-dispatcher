//go:build !linux || (!amd64 && !arm64)

package journal

func platformError() error { return failure(UnsupportedStorage, "platform", nil) }

type Session struct{}
type Recovery struct{ Budget *Budget }

func Initialize(Config, []byte) (*Session, error)            { return nil, platformError() }
func OpenEstablished(Config, Consumer) (*Session, error)     { return nil, platformError() }
func (*Session) Append([]byte) (uint64, error)               { return 0, platformError() }
func (*Session) ReplaceRecord(string, []byte) error          { return platformError() }
func (*Session) CreateInput(string, []byte) error            { return platformError() }
func (*Session) ReadInput(string, [32]byte) ([]byte, error)  { return nil, platformError() }
func (*Session) State() State                                { return State{Closed: true} }
func (*Session) Close() error                                { return platformError() }
func (*Recovery) ReplaceRecord(string, []byte) error         { return platformError() }
func (*Recovery) ReadInput(string, [32]byte) ([]byte, error) { return nil, platformError() }
