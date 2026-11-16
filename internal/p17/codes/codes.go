// Package codes holds the closed P17 launcher code set, its exit classes and
// the typed refusal carried by every P17 client in this repository.
//
// The code set is closed: exactly five codes, three classes, three exit
// statuses. Nothing here maps an untyped error to a class; an error that is
// neither a Refusal nor a SeamUnpinned cannot be turned into an exit status
// and Exit reports that instead of guessing.
package codes

import (
	"errors"
	"sort"
	"strconv"
	"strings"
)

// Code is one of the five P17 launcher codes.
type Code string

const (
	// OriginRefused is a definite Source refusal: wrong peer, generation,
	// socket path, origin, or origin_not_supported.
	OriginRefused Code = "run_origin_refused"
	// OriginUnavailable is a retryable Source dependency failure inside its
	// five-second budget.
	OriginUnavailable Code = "run_origin_unavailable"
	// BindUnknown is a lost bind response after the assigned connection is
	// gone; the run is HELD.
	BindUnknown Code = "run_bind_unknown"
	// EndUnknown is an end that was not acknowledged; the run stays fenced in
	// state ending.
	EndUnknown Code = "run_end_unknown"
	// IsolationUnqualified means R_i cannot be separated from L.
	IsolationUnqualified Code = "run_isolation_unqualified"
)

// Class is the exit class of a code.
type Class string

const (
	Definite  Class = "definite"
	Retryable Class = "retryable"
	Unknown   Class = "UNKNOWN"
)

// Exit statuses per D4.
const (
	ExitDefinite  = 16
	ExitRetryable = 1
	ExitUnknown   = 17
)

var classes = map[Code]Class{
	OriginRefused:        Definite,
	OriginUnavailable:    Retryable,
	BindUnknown:          Unknown,
	EndUnknown:           Unknown,
	IsolationUnqualified: Definite,
}

// Known reports whether c is in the closed set.
func (c Code) Known() bool { _, ok := classes[c]; return ok }

// Class returns the class of a known code; ok is false outside the set.
func (c Code) Class() (Class, bool) {
	cl, ok := classes[c]
	return cl, ok
}

// Exit returns the exit status of a class; ok is false for an unknown class.
func (c Class) Exit() (int, bool) {
	switch c {
	case Definite:
		return ExitDefinite, true
	case Retryable:
		return ExitRetryable, true
	case Unknown:
		return ExitUnknown, true
	}
	return 0, false
}

// Refusal is the typed P17 refusal. Detail carries diagnostic strings such as
// child_status; it never carries secrets.
type Refusal struct {
	Code   Code
	Op     string
	Detail map[string]string
	Cause  error
}

// Refuse builds a refusal. An unknown code is kept verbatim so that the
// mistake is visible, and Exit refuses to classify it.
func Refuse(code Code, op string, cause error) *Refusal {
	return &Refusal{Code: code, Op: op, Cause: cause}
}

// With adds one detail entry and returns the same refusal.
func (r *Refusal) With(key, value string) *Refusal {
	if r == nil {
		return nil
	}
	if r.Detail == nil {
		r.Detail = make(map[string]string)
	}
	r.Detail[key] = value
	return r
}

func (r *Refusal) Error() string {
	if r == nil {
		return "p17: <nil refusal>"
	}
	var b strings.Builder
	b.WriteString("p17: ")
	b.WriteString(string(r.Code))
	if r.Op != "" {
		b.WriteString(": ")
		b.WriteString(r.Op)
	}
	if len(r.Detail) > 0 {
		keys := make([]string, 0, len(r.Detail))
		for k := range r.Detail {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteString(" {")
		for i, k := range keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(r.Detail[k])
		}
		b.WriteString("}")
	}
	return b.String()
}

func (r *Refusal) Unwrap() error {
	if r == nil {
		return nil
	}
	return r.Cause
}

// CodeOf returns the code of the outermost Refusal in err's tree.
func CodeOf(err error) (Code, bool) {
	var r *Refusal
	if errors.As(err, &r) && r != nil {
		return r.Code, true
	}
	return "", false
}

// Is reports whether err carries exactly the given code.
func Is(err error, code Code) bool {
	c, ok := CodeOf(err)
	return ok && c == code
}

// SeamUnpinned is the refusal of a seam whose implementation depends on a
// slice or module pin that is not present in this build. It names the seam
// and the pin it waits for; it never falls back to a substitute.
type SeamUnpinned struct {
	Seam string
	Pin  string
}

func (e *SeamUnpinned) Error() string {
	if e == nil {
		return "p17: seam unpinned"
	}
	return "p17: seam unpinned: " + e.Seam + " (waits for " + e.Pin + ")"
}

// Unpinned builds a SeamUnpinned refusal.
func Unpinned(seam, pin string) *SeamUnpinned { return &SeamUnpinned{Seam: seam, Pin: pin} }

// IsUnpinned reports whether err's tree contains a SeamUnpinned refusal.
func IsUnpinned(err error) bool {
	var e *SeamUnpinned
	return errors.As(err, &e) && e != nil
}

// Verdict is the launcher exit decision.
type Verdict struct {
	Status int
	Class  Class
	Code   Code
	Detail map[string]string
}

// ErrUnclassified reports an error outside the closed set: Exit refuses to
// map it to a status rather than inventing a class.
var ErrUnclassified = errors.New("p17: error outside the closed code set")

// Exit computes the exit status for the launcher (D4).
//
// With err nil the launcher exits with the child's status, which must be
// known. A Refusal exits by its class. Under EndUnknown the status is 17 and
// never the child's; a known child status goes into Detail["child_status"].
// A SeamUnpinned exits definite (nothing was launched). Anything else is
// ErrUnclassified: the three classes stay distinct all the way to the exit.
func Exit(err error, childStatus int, childKnown bool) (Verdict, error) {
	if err == nil {
		if !childKnown {
			return Verdict{}, ErrUnclassified
		}
		return Verdict{Status: childStatus}, nil
	}
	var r *Refusal
	if errors.As(err, &r) && r != nil {
		cl, ok := r.Code.Class()
		if !ok {
			return Verdict{}, ErrUnclassified
		}
		st, _ := cl.Exit()
		v := Verdict{Status: st, Class: cl, Code: r.Code, Detail: map[string]string{}}
		for k, val := range r.Detail {
			v.Detail[k] = val
		}
		if childKnown {
			v.Detail["child_status"] = strconv.Itoa(childStatus)
		}
		return v, nil
	}
	var u *SeamUnpinned
	if errors.As(err, &u) && u != nil {
		return Verdict{Status: ExitDefinite, Class: Definite, Detail: map[string]string{"seam": u.Seam, "pin": u.Pin}}, nil
	}
	return Verdict{}, ErrUnclassified
}

// RequestTimeoutOK reports whether a Keymaker per-request timeout is inside
// the required 1..2000 ms window. The argument is milliseconds.
func RequestTimeoutOK(ms int64) bool { return ms >= 1 && ms <= 2000 }
