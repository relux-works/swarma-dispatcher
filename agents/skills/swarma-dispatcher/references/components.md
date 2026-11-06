# Internal Go components

These packages are implemented within this repository's Go module. Go's
`internal` import restriction applies; exported symbols do not make them an
external SDK. They are not additional CLI operations or a launch service.

## Byte framing

[frame.go](../../../../internal/frame/frame.go) exports
`Read(io.Reader) ([]byte, error)`, `Write(io.Writer, []byte) error`,
`MaxPayloadSize`, `ErrEmptyFrame` and `ErrFrameTooLarge`.
The [frame contract](../../../../spec/frame.md) defines the four-byte unsigned
big-endian prefix and inclusive payload bound 1..131072 bytes. Nonempty bytes
remain opaque: no JSON, signature, peer or descriptor validation is supplied.

A failed read returns nil payload. EOF before a new header is clean `io.EOF`;
EOF inside a frame is `io.ErrUnexpectedEOF`. Invalid write lengths emit nothing;
a short write without an error becomes `io.ErrShortWrite`. After any framing
error discard the stream rather than retrying on it. Serialize each direction
and supply finite deadlines/cancellation outside this generic codec.

## In-memory admission

Read [the package contract](../../../../internal/admission/doc.go) before
[admission.go](../../../../internal/admission/admission.go). A trusted future
adapter must supply authenticated `Request.Principal`, exact immutable request
bytes/generation, fresh authority/gates and independently verified evidence.
The store has no journal, downstream calls, process observer or durable recovery.

- Construct `Store` with `New(Config, epoch)`, with nonzero epoch and positive
  limits. Configure independent `Counts` for global/per-caller allocation and
  execution quotas and finite record/input/evidence/retained-byte budgets.
- `SetGates(epoch, Gates)` records observations; all seven barriers must be
  `Open` for new forward admission. Their zero value is `Unknown`.
  `Admit(epoch, Request)` keys identity by principal/request ID: an exact repeat
  returns the retained record, even during a hold; changed bytes, generation or
  resources return `ErrRequestConflict`.
- `Recheck(Binding)` and `Issue(Binding)` validate current epoch, generation,
  version and forward gates. `Issue` records the single local issue boundary;
  it does not execute or authorize an effect. Use returned current bindings for
  later transitions, not saved stale versions.
- `Cancel(Binding)` records intent. Cancellation before issue prevents issue;
  cancellation afterward preserves the original `Call`. `Complete(Binding,
  Call, Outcome, receipt)` accepts only `ResultUnknown` without receipt or
  `ResultPresent` with bounded nonempty verified receipt. Completion after
  cancellation needs the fresh binding and unchanged original call. Unknown
  outcomes retain claims; they are not permission to issue again.
- `ConfirmAbsent(Binding, evidence)` accepts bounded nonempty evidence whose
  truth the adapter has already established: fresh generation-bound account-wide
  absence, resolved pending calls and receiver exclusion of later starts/ingress.
  It releases only the execution claim. Timeout, cancellation or arbitrary bytes
  do not establish absence; allocation claims and dedup history remain retained.
- `Snapshot()` returns bounded independent copies and is available through holds.
  `AdvanceEpoch(current, next)` requires a larger epoch, invalidates open gate
  observations and retains claims/calls. Old-epoch calls cannot complete under
  the new daemon epoch. Replacing the store loses history; it is not recovery.

## Extension boundary

No runtime plugin loader, plugin interface, registration function or plugin-test
command is implemented. The trusted adapter described in the admission contract
is future integration work. The unexported `publicationCalls` /
`createEndpointCalls` seam in [endpoint_linux.go](../../../../internal/transport/endpoint_linux.go)
is for deterministic publication-error tests; configuration cannot provide its
callbacks or bypass validation. Do not describe it as a deployable plugin API.
