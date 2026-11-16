// Package sweep implements the D5a zero-process proof and the D5b kill-by-uid
// loop around the privilege-drop helper, plus the lease quarantine decision.
//
// The proof enumerates ALL processes under the procfs root and compares the
// real, effective, saved and filesystem uid of each with R_i. Any pid that
// cannot be read counts as not proved. Before any scan is trusted the procfs
// view itself is qualified (View.Qualify): a view that can hide the pids of
// another user (hidepid or a nested pid namespace) is never proved.
// subset=pid is refused conservatively: kernel procfs documentation says pid
// directories remain visible, but deployments must not set it for L.
// Killing goes through an injected
// Killer that receives only the uid, never a pid: the helper drops every id
// to R_i and calls kill(-1, SIGKILL). The default Killer refuses by name.
package sweep

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/relux-works/swarma-dispatcher/internal/p17/codes"
	"github.com/relux-works/swarma-dispatcher/internal/p17/incarnation"
)

// Enumeration is one D5a scan.
type Enumeration struct {
	UID        uint32
	Scanned    int
	Matched    []int
	Unreadable []int
	Err        error
	// qualified is set only by Enumerate, and only after the procfs view was
	// qualified. A hand-built or zero Enumeration is therefore never proved.
	qualified bool
}

// Qualified reports whether the scan ran on a qualified procfs view.
func (e Enumeration) Qualified() bool { return e.qualified }

// Proved reports zero processes under the uid with every pid readable, on a
// qualified procfs view. An unqualified view is never proved.
func (e Enumeration) Proved() bool {
	return e.qualified && e.Err == nil && len(e.Unreadable) == 0 && len(e.Matched) == 0
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// View names the procfs view the D5a proof reads and how it is qualified.
// The launcher L is not root: hidepid and nested pid namespaces can omit
// processes of R_i. subset=pid is refused conservatively, not because it
// hides pid directories (the kernel documentation says those stay visible).
// A deployment must not set subset=pid for the launcher.
type View struct {
	// ProcRoot is the procfs root; empty means /proc.
	ProcRoot string
	// MountInfo is the mountinfo file that describes the mount at ProcRoot;
	// empty means /proc/self/mountinfo of the calling process. Only tests
	// with a fixture proc tree set it.
	MountInfo string
	// GID returns the launcher's effective gid, compared with the gid=
	// option of a hidepid mount; nil means os.Getegid.
	GID func() uint32
}

// ErrViewUnqualified reports a procfs view that cannot carry the proof.
var ErrViewUnqualified = errors.New("sweep: procfs view not qualified for the zero-process proof")

func unqualified(what string, cause error) error {
	return errors.Join(ErrViewUnqualified, errors.New("sweep: procfs view: "+what), cause)
}

func (v View) root() string {
	if v.ProcRoot == "" {
		return "/proc"
	}
	return filepath.Clean(v.ProcRoot)
}

// unescapeMount decodes the octal escapes of a mountinfo path field.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Qualify checks that the view can carry the zero-process proof:
//
//   - the topmost mount at ProcRoot in mountinfo is a whole procfs (type
//     proc, root "/");
//   - it is not mounted with subset=pid;
//   - it is not mounted with hidepid other than 0/off, unless the mount
//     carries gid= and the launcher's gid equals it (hidepid=ptraceable has
//     no gid exemption and always refuses);
//   - pid 1 is listed and its status is readable;
//   - the NSpid: line of self has exactly one field, so the launcher is not
//     in a pid namespace nested below the one this procfs shows.
//
// Nothing is repaired or remounted.
func (v View) Qualify() error {
	root := v.root()
	mi := v.MountInfo
	if mi == "" {
		mi = "/proc/self/mountinfo"
	}
	b, err := os.ReadFile(mi)
	if err != nil {
		return unqualified("mountinfo unreadable", err)
	}
	var opts []string
	found := false
	for _, line := range strings.Split(string(b), "\n") {
		// id parent major:minor root mountpoint options [optional...] - fstype source superoptions
		pre, post, ok := strings.Cut(line, " - ")
		if !ok {
			continue
		}
		f := strings.Fields(pre)
		g := strings.Fields(post)
		if len(f) < 6 || len(g) < 1 || unescapeMount(f[4]) != root {
			continue
		}
		// The last line for a mount point is the topmost mount there.
		found = true
		opts = opts[:0]
		if g[0] != "proc" {
			opts = append(opts, "\x00notproc")
			continue
		}
		if f[3] != "/" {
			opts = append(opts, "\x00notroot")
			continue
		}
		opts = append(opts, strings.Split(f[5], ",")...)
		if len(g) >= 3 {
			opts = append(opts, strings.Split(g[2], ",")...)
		}
	}
	if !found {
		return unqualified("no mount at the procfs root in mountinfo", nil)
	}
	hide, gid, haveGID := "0", uint64(0), false
	for _, o := range opts {
		switch {
		case o == "\x00notproc":
			return unqualified("the mount at the procfs root is not of type proc", nil)
		case o == "\x00notroot":
			return unqualified("the mount at the procfs root is not the procfs root", nil)
		case o == "subset=pid" || strings.HasPrefix(o, "subset="):
			return unqualified("mounted with "+o, nil)
		case strings.HasPrefix(o, "hidepid="):
			hide = strings.TrimPrefix(o, "hidepid=")
		case strings.HasPrefix(o, "gid="):
			n, err := strconv.ParseUint(strings.TrimPrefix(o, "gid="), 10, 32)
			if err != nil {
				return unqualified("gid option unreadable", err)
			}
			gid, haveGID = n, true
		}
	}
	switch hide {
	case "0", "off":
	case "1", "noaccess", "2", "invisible":
		own := uint32(os.Getegid())
		if v.GID != nil {
			own = v.GID()
		}
		if !haveGID || uint64(own) != gid {
			return unqualified("mounted with hidepid="+hide+" and the launcher gid is not its gid=", nil)
		}
	default:
		return unqualified("mounted with hidepid="+hide, nil)
	}
	if _, err := os.Lstat(filepath.Join(root, "1")); err != nil {
		return unqualified("pid 1 is not listed", err)
	}
	if _, err := incarnation.ReadUIDs(root, 1); err != nil {
		return unqualified("status of pid 1 unreadable", err)
	}
	st, err := os.ReadFile(filepath.Join(root, "self", "status"))
	if err != nil {
		return unqualified("status of self unreadable", err)
	}
	for _, line := range strings.Split(string(st), "\n") {
		if !strings.HasPrefix(line, "NSpid:") {
			continue
		}
		if n := len(strings.Fields(line[len("NSpid:"):])); n != 1 {
			return unqualified("NSpid of self has "+strconv.Itoa(n)+" fields; nested pid namespace", nil)
		}
		return nil
	}
	return unqualified("no NSpid line for self", nil)
}

// Enumerate qualifies the view, then scans every numeric directory under its
// procfs root. On an unqualified view nothing is scanned, Err carries
// ErrViewUnqualified and the result is never proved.
func Enumerate(v View, uid uint32) Enumeration {
	e := Enumeration{UID: uid}
	if err := v.Qualify(); err != nil {
		e.Err = err
		return e
	}
	procRoot := v.root()
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		e.Err = errors.Join(errors.New("sweep: enumeration failed"), err)
		return e
	}
	listedOne := false
	for _, ent := range entries {
		if !allDigits(ent.Name()) {
			continue
		}
		pid, err := strconv.Atoi(ent.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if pid == 1 {
			listedOne = true
		}
		e.Scanned++
		uids, err := incarnation.ReadUIDs(procRoot, pid)
		if err != nil {
			if errors.Is(err, incarnation.ErrGone) {
				// Exited between readdir and read: it is not a process any more.
				e.Scanned--
				continue
			}
			e.Unreadable = append(e.Unreadable, pid)
			continue
		}
		if uids[0] == uid || uids[1] == uid || uids[2] == uid || uids[3] == uid {
			e.Matched = append(e.Matched, pid)
		}
	}
	sort.Ints(e.Matched)
	sort.Ints(e.Unreadable)
	if !listedOne {
		// The listing this scan trusted does not show pid 1.
		e.Err = unqualified("pid 1 is not in the listing", nil)
		return e
	}
	e.qualified = true
	return e
}

// Killer is the helper's sweep mode. It receives only the uid.
type Killer interface {
	KillAll(ctx context.Context, uid uint32) error
}

// HelperPin names the helper the default Killer waits for.
const HelperPin = "privilege-drop helper (r2.6 PENDING item 3, sweep mode)"

// UnpinnedKiller refuses by name.
type UnpinnedKiller struct{}

func (UnpinnedKiller) KillAll(context.Context, uint32) error {
	return codes.Unpinned("uid sweep killer", HelperPin)
}

// Budget bounds the kill loop.
type Budget struct {
	// Attempts is the maximum number of kill rounds.
	Attempts int
	// Pause is the wait between a kill round and the next enumeration.
	Pause time.Duration
}

// DefaultBudget is a bounded default: five rounds, 50 ms apart.
var DefaultBudget = Budget{Attempts: 5, Pause: 50 * time.Millisecond}

// ErrNotProved means the budget ended without a zero-process proof.
var ErrNotProved = errors.New("sweep: zero-process proof not established")

// Outcome is the result of Sweep.
type Outcome struct {
	Final Enumeration
	Kills int
	Err   error
}

// Sweep enumerates, kills by uid, and repeats within the budget until the
// enumeration proves zero. It never signals a pid by number. On an
// unqualified view it still kills within the budget, but cannot prove zero.
func Sweep(ctx context.Context, v View, uid uint32, k Killer, b Budget) Outcome {
	if ctx == nil {
		ctx = context.Background()
	}
	if b.Attempts <= 0 {
		b.Attempts = DefaultBudget.Attempts
	}
	if b.Pause < 0 {
		b.Pause = 0
	}
	var out Outcome
	for attempt := 0; ; attempt++ {
		out.Final = Enumerate(v, uid)
		if out.Final.Proved() {
			return out
		}
		if attempt >= b.Attempts {
			out.Err = errors.Join(ErrNotProved, out.Final.Err)
			return out
		}
		if k == nil {
			out.Err = errors.New("sweep: no killer")
			return out
		}
		if err := k.KillAll(ctx, uid); err != nil {
			out.Err = err
			return out
		}
		out.Kills++
		if b.Pause > 0 {
			t := time.NewTimer(b.Pause)
			select {
			case <-ctx.Done():
				t.Stop()
				out.Err = ctx.Err()
				return out
			case <-t.C:
			}
		}
	}
}

// WorkProductExportResult is the closed export evidence vocabulary. The zero
// value and any unknown or partial status are not proved, never no working copy.
type WorkProductExportResult string

const (
	ExportNotProved WorkProductExportResult = "not_proved"
	Exported        WorkProductExportResult = "exported"
	NoWorkingCopy   WorkProductExportResult = "no_working_copy"
)

// Satisfied requires explicit successful evidence, not absence of a failure.
func (r WorkProductExportResult) Satisfied() bool {
	return r == Exported || r == NoWorkingCopy
}

// WorkProductExport reports completed work-product export or explicit absence
// of a working copy for one run. It never copies or snapshots a home directory.
// Its producer belongs to other slices; the default refuses by name.
type WorkProductExport interface {
	ExportStatus(runKey string) (WorkProductExportResult, error)
}

// OperatorRelease is release seam (b): an explicit operator release of the
// uid for one run, recorded by operator recovery. The default refuses.
type OperatorRelease interface {
	Released(runKey string, uid uint32) (bool, error)
}

// Pins the two release seams wait for.
const (
	WorkProductExportPin = "work-product export producer (S1/S8; book ch. 07, Core G1)"
	OperatorReleasePin   = "operator release record (operator recovery, r2.6 H6)"
)

// UnpinnedWorkProductExport refuses by name: export is not proved.
type UnpinnedWorkProductExport struct{}

func (UnpinnedWorkProductExport) ExportStatus(string) (WorkProductExportResult, error) {
	return ExportNotProved, codes.Unpinned("work-product export evidence", WorkProductExportPin)
}

// UnpinnedOperatorRelease refuses by name: no operator release exists.
type UnpinnedOperatorRelease struct{}

func (UnpinnedOperatorRelease) Released(string, uint32) (bool, error) {
	return false, codes.Unpinned("operator release", OperatorReleasePin)
}

// StaticWorkProductExport is a fixed evidence table. A missing key, including
// a nil table, is not proved; no working copy must be an explicit entry.
type StaticWorkProductExport map[string]WorkProductExportResult

func (s StaticWorkProductExport) ExportStatus(runKey string) (WorkProductExportResult, error) {
	result, ok := s[runKey]
	if !ok {
		return ExportNotProved, nil
	}
	return result, nil
}

// StaticOperatorRelease is a fixed table keyed by run key; the uid must match
// the recorded one.
type StaticOperatorRelease map[string]uint32

func (s StaticOperatorRelease) Released(runKey string, uid uint32) (bool, error) {
	u, ok := s[runKey]
	return ok && u == uid, nil
}

// ReleaseGate is the R_i release decision: the D5a zero-process proof AND one
// of (a) work-product export with an acknowledged end, or (b) an explicit
// operator release. A nil seam is the refusing default.
type ReleaseGate struct {
	WorkProductExport WorkProductExport
	Operator          OperatorRelease
}

// ReleaseDecision records every input of one gate evaluation.
type ReleaseDecision struct {
	RunKey                     string
	UID                        uint32
	Proved                     bool
	EndAcknowledged            bool
	WorkProductExport          WorkProductExportResult
	WorkProductExportSatisfied bool
	WorkProductExportErr       error
	OperatorReleased           bool
	OperatorErr                error
	Release                    bool
	Reason                     string
}

// ReleaseAllowed is the pure decision: proof AND (operator release OR
// (acknowledged end AND work-product export)). Unknown evidence is false.
func ReleaseAllowed(e Enumeration, endAcknowledged, workProductExport, operatorReleased bool) bool {
	return e.Proved() && (operatorReleased || (endAcknowledged && workProductExport))
}

// Decide evaluates the gate for one run. A missing, unreadable or unknown
// export or release status counts as not proved and keeps the uid quarantined.
func (g ReleaseGate) Decide(runKey string, uid uint32, endAcknowledged bool, e Enumeration) ReleaseDecision {
	d := ReleaseDecision{RunKey: runKey, UID: uid, EndAcknowledged: endAcknowledged}
	d.Proved = e.UID == uid && e.Proved()
	a := g.WorkProductExport
	if a == nil {
		a = UnpinnedWorkProductExport{}
	}
	o := g.Operator
	if o == nil {
		o = UnpinnedOperatorRelease{}
	}
	d.WorkProductExport, d.WorkProductExportErr = a.ExportStatus(runKey)
	if d.WorkProductExportErr != nil || !d.WorkProductExport.Satisfied() {
		d.WorkProductExport = ExportNotProved
	}
	d.WorkProductExportSatisfied = d.WorkProductExport.Satisfied()
	d.OperatorReleased, d.OperatorErr = o.Released(runKey, uid)
	if d.OperatorErr != nil {
		d.OperatorReleased = false
	}
	switch {
	case e.UID != uid:
		d.Reason = "enumeration is for another uid"
	case !e.Proved():
		d.Reason = "zero-process proof not established"
	case d.OperatorReleased:
		d.Reason = "operator release"
	case !endAcknowledged:
		d.Reason = "end not acknowledged and no operator release"
	case !d.WorkProductExportSatisfied:
		d.Reason = "work-product export not proved and no operator release"
	default:
		d.Reason = "acknowledged end with work-product export"
	}
	d.Release = d.Proved && ReleaseAllowed(e, endAcknowledged, d.WorkProductExportSatisfied, d.OperatorReleased)
	return d
}

// Quarantine tracks R_i uids that may not be leased. A uid enters on lease
// and leaves only through Release when the gate allows it.
type Quarantine struct {
	// Gate is configured before any run; launcher.New calls must precede runs.
	Gate ReleaseGate
	// Guard reports a non-terminal record on uid, excluding only except.
	// Configure once before Restart; Lease passes an empty except.
	Guard     func(uid uint32, except string) bool
	mu        sync.Mutex
	held      map[uint32]Entry
	busy      map[uint32]int
	started   bool
	directory string
	lock      *os.File
	blocked   error
	// syncFile is a private crash-point seam; production always uses File.Sync.
	syncFile func(*os.File) error
}

// ErrQuarantined refuses a lease of a held uid, and reports a refused release.
var ErrQuarantined = errors.New("sweep: uid quarantined")

func holder(keys []string) string {
	if len(keys) > 0 {
		return keys[0]
	}
	return "standalone"
}

// Hold restores a record's hold without overwriting another lease.
func (q *Quarantine) Hold(uid uint32, keys ...string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.blocked != nil {
		return q.blocked
	}
	if q.held == nil {
		q.held = map[uint32]Entry{}
	}
	if _, ok := q.held[uid]; ok {
		return nil
	}
	q.held[uid] = Entry{UID: uid, Holder: holder(keys), Reason: "run record"}
	return q.persistLocked()
}

// Held reports whether the uid is quarantined.
func (q *Quarantine) Held(uid uint32) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, ok := q.held[uid]
	return ok || q.blocked != nil
}

// Lease durably records its holder before use, and never erases an older mark.
func (q *Quarantine) Lease(uid uint32, keys ...string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.started && (len(keys) != 1 || keys[0] == "") {
		return ErrQuarantined
	}
	if q.blocked != nil {
		return q.blocked
	}
	if _, ok := q.held[uid]; ok {
		return ErrQuarantined
	}
	if q.Guard != nil && q.Guard(uid, "") {
		return ErrQuarantined
	}
	if q.held == nil {
		q.held = map[uint32]Entry{}
	}
	q.held[uid] = Entry{UID: uid, Holder: holder(keys), Reason: "leased"}
	return q.persistLocked()
}

// MarkHelper records that the privilege-drop helper was invoked for the uid
// (spawn or sweep mode). The mark lasts until the uid is leased again.
func (q *Quarantine) MarkHelper(uid uint32, keys ...string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	e, ok := q.held[uid]
	if !ok || (len(keys) > 0 && e.Holder != keys[0]) {
		return ErrQuarantined
	}
	e.Helper = true
	e.Reason = "helper invoked"
	q.held[uid] = e
	return q.persistLocked()
}

// HelperInvoked reports whether the helper was invoked for the uid since it
// was last leased.
func (q *Quarantine) HelperInvoked(uid uint32) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.held[uid].Helper
}

// WithLease keeps a sweep scoped to its holder until all kill rounds finish.
// A stale end path never touches a newer holder's uid.
func (q *Quarantine) WithLease(uid uint32, key string, sweep func() Outcome) Outcome {
	q.mu.Lock()
	e, ok := q.held[uid]
	if q.blocked != nil || !ok || e.Holder != key || (q.Guard != nil && q.Guard(uid, key)) {
		q.mu.Unlock()
		err := errors.Join(ErrQuarantined, errors.New("sweep: uid not held exclusively for this record; not proved, uid untouched"))
		return Outcome{Final: Enumeration{UID: uid, Err: err}, Err: err}
	}
	if q.busy == nil {
		q.busy = map[uint32]int{}
	}
	q.busy[uid]++
	q.mu.Unlock()
	defer func() { q.mu.Lock(); q.busy[uid]--; q.mu.Unlock() }()
	return sweep()
}

// ReturnUnused returns a lease under which nothing was ever started. BOTH
// conditions must hold: the privilege-drop helper was never invoked for this
// lease (no MarkHelper since Lease), AND e is a proved enumeration for this
// uid on a qualified view. A helper invocation is not excused by a clean
// proof, and the proof is never skipped. Otherwise the uid stays quarantined
// and ErrQuarantined is returned. This is not the release gate; a lease that was ever used leaves
// only through Release.
func (q *Quarantine) ReturnUnused(uid uint32, e Enumeration, keys ...string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.blocked != nil {
		return q.blocked
	}
	entry, ok := q.held[uid]
	if !ok || entry.Holder != holder(keys) || q.busy[uid] != 0 {
		return ErrQuarantined
	}
	if entry.Helper || e.UID != uid || !e.Proved() {
		return ErrQuarantined
	}
	if q.Guard != nil && q.Guard(uid, entry.Holder) {
		return ErrQuarantined
	}
	delete(q.held, uid)
	return q.persistLocked()
}

// Release frees the uid only when the gate allows it for this run; otherwise
// the uid stays held and ErrQuarantined is returned with the decision.
func (q *Quarantine) Release(runKey string, uid uint32, endAcknowledged bool, e Enumeration) (ReleaseDecision, error) {
	d := q.Gate.Decide(runKey, uid, endAcknowledged, e)
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.blocked != nil {
		return d, q.blocked
	}
	entry, ok := q.held[uid]
	if !ok || entry.Holder != runKey || q.busy[uid] != 0 || (q.Guard != nil && q.Guard(uid, runKey)) {
		d.Release = false
		d.Reason = "uid not exclusively held for this record"
		return d, ErrQuarantined
	}
	if !d.Release {
		return d, ErrQuarantined
	}
	delete(q.held, uid)
	return d, q.persistLocked()
}
