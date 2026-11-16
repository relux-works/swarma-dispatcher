package m0

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Executor-entry /1 constants from spec/m0-executor-entry.md.
const (
	EntryKind      = "swarma-dispatcher m0-executor-entry/1"
	EntryPlacement = "station-one-shot-v1"

	maxEntryBytes       = 49152
	maxEntryStringBytes = 32768
	entryObjects        = 9
	entryMembers        = 43
	maxSelector         = 64
	maxCatalogString    = 128
	maxPrompt           = 24576
	maxArgvElement      = 1024
	maxArgvBytes        = 8192
	maxRunnerTimeout    = 240
	runnerCommand       = "run-codex"
	executionPrefix     = "EX-"
)

// Input is the closed worker input body selected by the protected catalog.
type Input struct {
	Account         string
	Profile         string
	Model           string
	ReasoningEffort string
	Prompt          string
}

// Execution identifies the original execution: the launcher effect, the
// execution claim identity and the controller epoch.
type Execution struct {
	EffectID        string
	ExecutionID     string
	ControllerEpoch uint64
}

// Timing is the original absolute deadline and the qualified slack, both in
// milliseconds of the exact qualified tuple's clock coordinate.
type Timing struct {
	DeadlineMs int64
	SlackMs    int64
}

// Entry is the complete FD3 document. Request is the already-bound audit copy
// of the admitted unsigned dispatch.
type Entry struct {
	Request       *Request
	Execution     Execution
	Catalog       string
	Qualification string
	Timing        Timing
	Input         Input
}

func isSelector(s string) bool {
	if len(s) < 1 || len(s) > maxSelector {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		lower := c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
		if i == 0 && !lower {
			return false
		}
		if !lower && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func isCatalogString(s string) bool {
	if len(s) < 1 || len(s) > maxCatalogString || s[0] == '-' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

func isExecutionID(s string) bool {
	return strings.HasPrefix(s, executionPrefix) && isID(s[len(executionPrefix):])
}

func (in Input) validate() error {
	if !isSelector(in.Account) || !isSelector(in.Profile) {
		return invalid("input selector spelling")
	}
	if !isCatalogString(in.Model) || !isCatalogString(in.ReasoningEffort) {
		return invalid("input catalog string spelling")
	}
	if len(in.Prompt) < 1 || len(in.Prompt) > maxPrompt || !utf8.ValidString(in.Prompt) || strings.IndexByte(in.Prompt, 0) >= 0 {
		return invalid("prompt bounds")
	}
	return nil
}

func (in Input) value() *value {
	return object(
		member{"account", text(in.Account)},
		member{"profile", text(in.Profile)},
		member{"model", text(in.Model)},
		member{"reasoning_effort", text(in.ReasoningEffort)},
		member{"placement", text(EntryPlacement)},
		member{"prompt", text(in.Prompt)},
	)
}

// Canonical returns CCJ-1 of the input body and its "sha256:" digest, which
// the request's worker_plan pin must equal.
func (in Input) Canonical() ([]byte, string, error) {
	if err := in.validate(); err != nil {
		return nil, "", err
	}
	b := in.value().encode(nil)
	return b, digestOf(b), nil
}

func digestOf(b []byte) string {
	sum := sha256.Sum256(b)
	return digestPrefix + hex.EncodeToString(sum[:])
}

func (e Execution) validate() error {
	if !isID(e.EffectID) || !isExecutionID(e.ExecutionID) || !isSerial(int64(e.ControllerEpoch)) || e.ControllerEpoch > maxSafeInteger {
		return invalid("execution identity spelling")
	}
	return nil
}

func (t Timing) validate() error {
	if !isSerial(t.DeadlineMs) || !isSerial(t.SlackMs) {
		return invalid("timing range")
	}
	return nil
}

// Canonical builds the entry, enforces its work ceilings (<=49152 canonical
// bytes, depth <=8, <=512 tokens, exactly 9 objects and 43 members, <=32768
// decoded string bytes) and the protected equality worker_plan ==
// HASH(CCJ-1(input)). It returns CCJ-1 of the entry and its digest.
func (e *Entry) Canonical() ([]byte, string, error) {
	if e == nil || e.Request == nil {
		return nil, "", invalid("entry without request")
	}
	if e.Request.Op != OpDispatch || e.Request.Auth.Key != "" {
		return nil, "", invalid("entry request must be the unsigned dispatch")
	}
	if err := e.Execution.validate(); err != nil {
		return nil, "", err
	}
	if err := e.Timing.validate(); err != nil {
		return nil, "", err
	}
	if !isDigest(e.Catalog) || !isDigest(e.Qualification) {
		return nil, "", invalid("catalog or qualification digest spelling")
	}
	_, inputDigest, err := e.Input.Canonical()
	if err != nil {
		return nil, "", err
	}
	if inputDigest != e.Request.Plan.WorkerPlan {
		return nil, "", refuse(CodePlanDigestMismatch, "worker_plan is not the digest of the selected input body")
	}
	request, _, err := canonicalize(e.Request.Canonical, requestLimits)
	if err != nil {
		return nil, "", err
	}
	entry := object(
		member{"v", integer(1)},
		member{"kind", text(EntryKind)},
		member{"request", request},
		member{"execution", object(
			member{"effect_id", text(e.Execution.EffectID)},
			member{"execution_id", text(e.Execution.ExecutionID)},
			member{"controller_epoch", integer(int64(e.Execution.ControllerEpoch))},
		)},
		member{"catalog", text(e.Catalog)},
		member{"qualification", text(e.Qualification)},
		member{"timing", object(
			member{"deadline_ms", integer(e.Timing.DeadlineMs)},
			member{"slack_ms", integer(e.Timing.SlackMs)},
		)},
		member{"input", e.Input.value()},
	)
	b := entry.encode(nil)
	if len(b) > maxEntryBytes {
		return nil, "", refuse(CodeRequestTooLarge, "entry exceeds the canonical byte ceiling")
	}
	// Re-parsing the canonical bytes enforces depth and token ceilings with the
	// same bounded scanner used for inputs.
	parsed, _, err := canonicalize(b, entryLimits)
	if err != nil {
		return nil, "", err
	}
	objects, members, stringBytes := parsed.stats()
	if objects != entryObjects || members != entryMembers || stringBytes > maxEntryStringBytes {
		return nil, "", invalid("entry object, member or string ceiling")
	}
	return b, digestOf(b), nil
}

// RunnerTimeout is the proposed cap T = min(240, floor((deadline - now -
// slack) / 1000)) with T >= 1, in checked integer arithmetic. It never rounds
// upward and never resets the deadline or slack.
func RunnerTimeout(deadlineMs, nowMs, slackMs int64) (int64, error) {
	if !isSerial(deadlineMs) || !isSerial(slackMs) || nowMs < 0 || nowMs > maxSafeInteger {
		return 0, refuse(CodeTimeUnknown, "timing inputs outside the safe range")
	}
	remaining := deadlineMs - nowMs - slackMs
	if remaining < 1000 {
		return 0, refuse(CodeDeadlineExhausted, "less than one second remains after slack")
	}
	t := remaining / 1000
	if t > maxRunnerTimeout {
		t = maxRunnerTimeout
	}
	return t, nil
}

// Runner is the broker runner argv seam. CodexRunner is the executor-entry
// mapping of spec/m0-executor-entry.md section 4; the Claude runner variant
// (slice S3a) is unpublished and refuses.
type Runner interface {
	Argv(e *Entry, nowMs int64) ([]string, error)
}

// UnpinnedRunner refuses by name.
type UnpinnedRunner struct{}

func (UnpinnedRunner) Argv(*Entry, int64) ([]string, error) { return nil, Unpinned(SeamRunner) }

// CodexRunner holds the immutable installed literals E (approved broker
// executable) and H (manager-verified station home). It performs no lookup.
type CodexRunner struct {
	Executable string
	Home       string
}

func validAbsolutePath(s string) bool {
	return len(s) >= 2 && len(s) <= maxArgvElement && s[0] == '/' && strings.IndexByte(s, 0) < 0 && utf8.ValidString(s) && !strings.HasSuffix(s, "/") && !strings.Contains(s, "//") && !strings.Contains(s, "/./") && !strings.Contains(s, "/../")
}

// Argv returns exactly the 19 proposed elements for one admitted entry, with
// paths derived as J=H/tmp/m0/<execution_id>, L=J/home, W=H/work and
// O=J/last-message.txt, each absolute and <=1024 bytes, and total NUL-counted
// storage <=8192 bytes.
func (c CodexRunner) Argv(e *Entry, nowMs int64) ([]string, error) {
	if e == nil {
		return nil, invalid("entry required")
	}
	if !validAbsolutePath(c.Executable) || !validAbsolutePath(c.Home) {
		return nil, refuse(CodePolicyMismatch, "installed mapping literals are not admissible absolute paths")
	}
	if err := e.Execution.validate(); err != nil {
		return nil, err
	}
	if err := e.Input.validate(); err != nil {
		return nil, err
	}
	j := c.Home + "/tmp/m0/" + e.Execution.ExecutionID
	l := j + "/home"
	w := c.Home + "/work"
	o := j + "/last-message.txt"
	for _, p := range []string{j, l, w, o} {
		if !validAbsolutePath(p) {
			return nil, refuse(CodeRequestTooLarge, "derived path exceeds the element bound")
		}
	}
	t, err := RunnerTimeout(e.Timing.DeadlineMs, nowMs, e.Timing.SlackMs)
	if err != nil {
		return nil, err
	}
	argv := []string{
		c.Executable, runnerCommand,
		"--account", e.Input.Account,
		"--profile", e.Input.Profile,
		"--launch-home", l,
		"--model", e.Input.Model,
		"--reasoning-effort", e.Input.ReasoningEffort,
		"--output-last-message", o,
		"--prompt-stdin",
		"--cwd", w,
		"--timeout", strconv.FormatInt(t, 10),
	}
	total := 0
	for _, a := range argv {
		if len(a) > maxArgvElement {
			return nil, refuse(CodeRequestTooLarge, "argv element exceeds 1024 bytes")
		}
		total += len(a) + 1
	}
	if total > maxArgvBytes {
		return nil, refuse(CodeRequestTooLarge, "argv storage exceeds 8192 bytes")
	}
	return argv, nil
}

// EntryComposer is the seam that selects the protected catalog input body and
// the catalog/qualification pins for one admitted dispatch. The published
// catalog content is a gate; the default refuses.
type EntryComposer interface {
	Compose(r *Request, exec Execution, timing Timing) (*Entry, error)
}

// UnpinnedEntryComposer refuses by name.
type UnpinnedEntryComposer struct{}

func (UnpinnedEntryComposer) Compose(*Request, Execution, Timing) (*Entry, error) {
	return nil, Unpinned(SeamEntry)
}
