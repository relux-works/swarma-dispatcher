//go:build linux && (amd64 || arm64)

package compose

import (
	"errors"
	"net"
	"testing"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/fakes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/source"
)

// principalBindingWire substitutes the same binding in both replies so the
// generic prepare/validate drift check cannot substitute for the root check.
type principalBindingWire struct {
	fakes.SourceWire
	binding string
	missing bool
}

func (w principalBindingWire) replace(bindings map[string]string) {
	if w.missing {
		delete(bindings, "input_principal")
	} else {
		bindings["input_principal"] = w.binding
	}
}

func (w principalBindingWire) Prepare(c net.Conn, req source.PrepareRequest) (source.PreparedMessage, error) {
	msg, err := w.SourceWire.Prepare(c, req)
	if err == nil {
		w.replace(msg.Bindings)
	}
	return msg, err
}

func (w principalBindingWire) Validate(c net.Conn, req source.ValidateRequest) (source.ValidatedMessage, error) {
	msg, err := w.SourceWire.Validate(c, req)
	if err == nil {
		w.replace(msg.Bindings)
	}
	return msg, err
}

// Claim: root Begin independently checks the validated input principal even when both Source replies agree on a substituted, missing or malformed binding, beside a matching bind/end control.
func TestValidatedPrincipalBinding(t *testing.T) {
	const valid = "cccccccccccccccccccccccccccccccc"
	const matching = `{"uid":30002,"generation":"` + valid + `"}`
	for _, row := range []struct {
		name, binding string
		missing       bool
	}{
		{"generation", `{"uid":30002,"generation":"dddddddddddddddddddddddddddddddd"}`, false},
		{"uid", `{"uid":30003,"generation":"` + valid + `"}`, false},
		{"missing", "", true},
		{"malformed", "{", false},
	} {
		t.Run(row.name, func(t *testing.T) {
			control := newRootFixture(t)
			control.client.Wire = principalBindingWire{binding: matching}
			control.bindAndEnd(t, valid)
			f := newRootFixture(t)
			f.client.Wire = principalBindingWire{binding: row.binding, missing: row.missing}
			run, err := f.begin(valid)
			var refusal *codes.Refusal
			if run != nil || !errors.As(err, &refusal) || refusal.Code != codes.OriginRefused || refusal.Op != "Source input principal differs from input" || refusal.Detail["binding"] != "input_principal" {
				t.Fatalf("wrong validated principal refusal: run=%t err=%v", run != nil, err)
			}
			_, prepares, validates, _ := f.source.Counts()
			dials, _ := f.sourceDialer.Dials()
			if prepares != 1 || validates != 1 || dials != 1 || f.keymakerDialer.Dials() != 0 || len(f.store.Keys()) != 0 || f.quarantine.Held(30002) {
				t.Fatal("consistent principal substitution escaped Source or leaked its lease")
			}
		})
	}
}
