package codes

import (
	"errors"
	"fmt"
	"testing"
)

// Claim (r2.6 Codes table): exactly the five codes are known, each with its
// stated class, and a code outside the table is unknown with no class.
func TestClosedCodeSet(t *testing.T) {
	want := map[Code]Class{
		OriginRefused:        Definite,
		OriginUnavailable:    Retryable,
		BindUnknown:          Unknown,
		EndUnknown:           Unknown,
		IsolationUnqualified: Definite,
	}
	for c, cl := range want {
		got, ok := c.Class()
		if !ok || got != cl || !c.Known() {
			t.Fatalf("%s: class=%s ok=%v", c, got, ok)
		}
	}
	if len(classes) != 5 {
		t.Fatalf("code set size %d, want 5", len(classes))
	}
	// Negative: a plausible-looking code outside the table has no class.
	if _, ok := Code("run_origin_unknown").Class(); ok || Code("run_origin_unknown").Known() {
		t.Fatal("unknown code was classified")
	}
}

// Claim (D4): definite exits 16, retryable 1, UNKNOWN 17; an unknown class
// has no exit status.
func TestClassExit(t *testing.T) {
	for cl, st := range map[Class]int{Definite: 16, Retryable: 1, Unknown: 17} {
		got, ok := cl.Exit()
		if !ok || got != st {
			t.Fatalf("%s: exit=%d ok=%v", cl, got, ok)
		}
	}
	if _, ok := Class("maybe").Exit(); ok {
		t.Fatal("unknown class has an exit status")
	}
}

// Claim (D4): under run_end_unknown the exit is 17 regardless of the child's
// status, and the child's status goes into detail child_status. Positive
// control: with no error the launcher exits with the child's status.
func TestExitEndUnknownNeverChildStatus(t *testing.T) {
	for _, child := range []int{0, 1, 3, 16, 42} {
		v, err := Exit(Refuse(EndUnknown, "end lost", nil), child, true)
		if err != nil || v.Status != ExitUnknown || v.Detail["child_status"] != fmt.Sprint(child) || v.Code != EndUnknown {
			t.Fatalf("child %d: verdict %+v err %v", child, v, err)
		}
	}
	v, err := Exit(nil, 3, true)
	if err != nil || v.Status != 3 {
		t.Fatalf("positive control: %+v %v", v, err)
	}
}

// Claim (D4, classes stay distinct): every code exits by its own class, a
// definite refusal does not borrow the child status, and an untyped error or
// an unknown-code refusal is ErrUnclassified rather than a guessed status.
func TestExitClassesDistinct(t *testing.T) {
	cases := map[Code]int{OriginRefused: 16, IsolationUnqualified: 16, OriginUnavailable: 1, BindUnknown: 17, EndUnknown: 17}
	for c, st := range cases {
		v, err := Exit(Refuse(c, "x", nil), 0, true)
		if err != nil || v.Status != st {
			t.Fatalf("%s: %+v %v", c, v, err)
		}
	}
	if _, err := Exit(errors.New("plain"), 0, true); !errors.Is(err, ErrUnclassified) {
		t.Fatalf("untyped error classified: %v", err)
	}
	if _, err := Exit(Refuse(Code("run_made_up"), "x", nil), 0, true); !errors.Is(err, ErrUnclassified) {
		t.Fatalf("unknown code classified: %v", err)
	}
	if _, err := Exit(nil, 0, false); !errors.Is(err, ErrUnclassified) {
		t.Fatal("nil error with unknown child status produced a verdict")
	}
}

// Claim: a SeamUnpinned refusal is recognised through wrapping, names its seam
// and pin, and exits definite; a plain error is not unpinned.
func TestSeamUnpinned(t *testing.T) {
	u := Unpinned("source wire codec", "example-module abcdef")
	wrapped := fmt.Errorf("outer: %w", u)
	if !IsUnpinned(wrapped) || IsUnpinned(errors.New("plain")) {
		t.Fatal("unpinned detection")
	}
	if u.Error() != "p17: seam unpinned: source wire codec (waits for example-module abcdef)" {
		t.Fatalf("message %q", u.Error())
	}
	v, err := Exit(wrapped, 0, false)
	if err != nil || v.Status != ExitDefinite || v.Detail["seam"] != "source wire codec" {
		t.Fatalf("%+v %v", v, err)
	}
}

// Claim: Refusal carries code, detail and cause through errors.As/Unwrap, and
// nil receivers neither panic nor claim a code.
func TestRefusalShape(t *testing.T) {
	cause := errors.New("cause")
	r := Refuse(OriginRefused, "peer", cause).With("peer_uid", "7")
	wrapped := fmt.Errorf("w: %w", r)
	if c, ok := CodeOf(wrapped); !ok || c != OriginRefused || !Is(wrapped, OriginRefused) || Is(wrapped, EndUnknown) {
		t.Fatal("code lookup")
	}
	if !errors.Is(wrapped, cause) {
		t.Fatal("cause lost")
	}
	if r.Error() != "p17: run_origin_refused: peer {peer_uid=7}" {
		t.Fatalf("message %q", r.Error())
	}
	var nilRef *Refusal
	if nilRef.Error() == "" || nilRef.Unwrap() != nil || nilRef.With("a", "b") != nil {
		t.Fatal("nil refusal misbehaves")
	}
	if c, ok := CodeOf(errors.New("plain")); ok || c != "" {
		t.Fatal("plain error has a code")
	}
}

// Claim (D4 timeouts): the Keymaker per-request window is 1..2000 ms inclusive.
func TestRequestTimeoutWindow(t *testing.T) {
	for ms, ok := range map[int64]bool{0: false, 1: true, 1000: true, 2000: true, 2001: false, -5: false} {
		if RequestTimeoutOK(ms) != ok {
			t.Fatalf("%d ms: want %v", ms, ok)
		}
	}
}
