package launcher

import "github.com/relux-works/swarma-dispatcher/internal/p17/runrecord"

// BeginRecordError associates a failed Begin with its record without parsing
// diagnostics. Use errors.As; CodeOf, errors.Is and MustExit still follow Cause.
// Check MustExit first, then the code and RecordUncertain. Only a certain
// terminal Unsent/Refused outcome is a bind-less no-start result. An uncertain
// state is the attempted transition, not a claim about replay or durability.
// A pre-record refusal has no BeginRecordError. Begin's signature is unchanged.
type BeginRecordError struct {
	key       string
	state     runrecord.State
	uncertain bool
	cause     error
}

func (e *BeginRecordError) Error() string                { return e.cause.Error() }
func (e *BeginRecordError) Unwrap() error                { return e.cause }
func (e *BeginRecordError) RecordKey() string            { return e.key }
func (e *BeginRecordError) RecordState() runrecord.State { return e.state }
func (e *BeginRecordError) RecordUncertain() bool        { return e.uncertain }
