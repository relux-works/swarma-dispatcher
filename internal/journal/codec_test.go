package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
)

const goldenHex = "53444a524e4c303100010000000000010000000000000001617cbca5bdd6e3f363ddc0daf2942196ceada4e8d7a329922773e755ad35a6dc83"

func golden(t *testing.T) []byte {
	t.Helper()
	b, e := hex.DecodeString(goldenHex)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func literalFrame(seq uint64, p []byte) []byte {
	// Independent layout fixture: do not call the production encoder.
	b := make([]byte, 56+len(p))
	copy(b, []byte{'S', 'D', 'J', 'R', 'N', 'L', '0', '1', 0, 1, 0, 0})
	binary.BigEndian.PutUint32(b[12:16], uint32(len(p)))
	binary.BigEndian.PutUint64(b[16:24], seq)
	copy(b[24:], p)
	sum := sha256.Sum256(b[:len(b)-32])
	copy(b[len(b)-32:], sum[:])
	return b
}
func reader(b []byte) readAt {
	return func(p []byte, off int64) (int, error) {
		if off >= int64(len(b)) {
			return 0, nil
		}
		return copy(p, b[off:]), nil
	}
}
func wantClass(t *testing.T, err error, c Class) {
	t.Helper()
	if ClassOf(err) != c {
		t.Fatalf("class: got %v (%s), want %s", err, ClassOf(err), c)
	}
}

// The literal vector is valid; altered neighbors distinguish invalid headers
// and digests from admissible headers with incomplete bodies (spec/journal.md:283-299).
func TestGoldenAndCorruption(t *testing.T) {
	b := golden(t)
	if !bytes.Equal(literalFrame(1, []byte{'a'}), b) {
		t.Fatal("literal golden digest")
	}
	h := header(1, 1)
	if !bytes.Equal(h[:], b[:24]) {
		t.Fatal("encoder golden header")
	}
	next, err := walk(reader(b), 57, nil)
	if err != nil || next != 2 {
		t.Fatalf("golden accepted: %d %v", next, err)
	}
	// Spec/journal.md:283-295: fixed fields and sequence must match; at O=0,
	// U=1048576. Flips at 12/13 yield N=2147483649/8388609, both above U.
	// Flips at 14/15 yield admissible N=32769/129 but short bodies: incomplete-tail
	// (:297-298). At 24/56 the complete frame has a bad digest (:299).
	// TestCapsAndBoundedReads also constrains full headers with N=0 and N=U+1.
	for _, tc := range []struct {
		index int
		want  Class
	}{
		{0, Corruption}, {7, Corruption}, {8, Corruption}, {9, Corruption},
		{10, Corruption}, {11, Corruption}, {12, Corruption}, {13, Corruption},
		{14, IncompleteTail}, {15, IncompleteTail},
		{16, Corruption}, {23, Corruption}, {24, Corruption}, {56, Corruption},
	} {
		t.Run(string(rune('A'+tc.index)), func(t *testing.T) {
			bad := append([]byte(nil), b...)
			bad[tc.index] ^= 0x80
			_, e := walk(reader(bad), int64(len(bad)), nil)
			wantClass(t, e, tc.want)
		})
	}
	for _, seq := range []uint64{0, 2, ^uint64(0)} {
		bad := literalFrame(seq, []byte{'a'})
		_, e := walk(reader(bad), 57, nil)
		wantClass(t, e, Corruption)
	}
	for _, seq := range []uint64{1, 3} {
		bad := append(append([]byte(nil), b...), literalFrame(seq, []byte{'b'})...)
		_, e := walk(reader(bad), 114, nil)
		wantClass(t, e, Corruption)
	}
	two := append(append([]byte(nil), b...), literalFrame(2, []byte{'b'})...)
	next, err = walk(reader(two), 114, nil)
	if err != nil || next != 3 {
		t.Fatal("two ordered frames accepted", err)
	}
}

// Every feasible header truncation is a tail; every contradictory present fixed
// byte is corruption. Length intervals use literal boundary arithmetic.
func TestAllHeaderPrefixes(t *testing.T) {
	b := golden(t)
	for k := 1; k < 24; k++ {
		_, err := walk(reader(b[:k]), int64(k), nil)
		wantClass(t, err, IncompleteTail)
		for j := 0; j < k; j++ {
			if j >= 12 && j < 16 {
				continue
			}
			bad := append([]byte(nil), b[:k]...)
			bad[j] ^= 1
			_, err = walk(reader(bad), int64(k), nil)
			if ClassOf(err) != Corruption {
				t.Fatalf("fixed prefix byte %d/%d must corrupt: %v", j, k, err)
			}
		}
	}
	for _, test := range []struct {
		p  []byte
		ok bool
	}{{[]byte{0, 0x10, 0}, true}, {[]byte{0, 0x10, 1}, false}, {[]byte{0, 0, 0, 0}, false}, {[]byte{0, 0, 0, 1}, true}, {[]byte{0xff}, false}} {
		h := append(append([]byte(nil), b[:12]...), test.p...)
		if feasible(h, 0, 1) != test.ok {
			t.Fatalf("length interval %x", test.p)
		}
	}
	if feasible(b[:1], FileCap-56, 1) || feasible(b[:1], -1, 1) || feasible(b[:1], 0, 0) || feasible(b[:24], 0, 0) {
		t.Fatal("impossible offset or successor")
	}
	if !feasible(b[:24], FileCap-57, 1) {
		t.Fatal("one-byte remaining capacity")
	}
	for n := 24; n < 57; n++ {
		_, err := walk(reader(b[:n]), int64(n), nil)
		wantClass(t, err, IncompleteTail)
	}
}

// Cap checks precede body reads/allocation, including a cap+1 file and a final
// header whose projected end exceeds the cap although the file is truncated.
func TestCapsAndBoundedReads(t *testing.T) {
	for _, size := range []int64{-1, FileCap + 1} {
		calls := 0
		_, err := walk(func([]byte, int64) (int, error) { calls++; return 0, nil }, size, nil)
		wantClass(t, err, Corruption)
		if calls != 0 {
			t.Fatal("oversized file read before cap check")
		}
	}
	// Spec/journal.md:283-285,294-295: at O=0, N=0 and N=U+1=1048577
	// are inadmissible full headers, hence corruption even with no body;
	// the maximum uint32 is likewise above U=1048576.
	for _, length := range []uint32{0, 1048577, 0xffffffff} {
		h := append([]byte(nil), golden(t)[:24]...)
		binary.BigEndian.PutUint32(h[12:16], length)
		calls := 0
		_, err := walk(func(p []byte, o int64) (int, error) {
			calls++
			if calls > 1 {
				t.Fatal("invalid length body read")
			}
			return reader(h)(p, o)
		}, 24, nil)
		wantClass(t, err, Corruption)
	}
	max := literalFrame(1, bytes.Repeat([]byte{0x42}, 1048576))
	maxRead := 0
	_, err := walk(func(p []byte, o int64) (int, error) {
		if len(p) > maxRead {
			maxRead = len(p)
		}
		return reader(max)(p, o)
	}, int64(len(max)), nil)
	if err != nil || maxRead > 32768 {
		t.Fatal("max payload and bounded read", maxRead, err)
	}
	// A virtual file avoids storing 64 MiB: 63 maximum frames plus a final frame.
	frame := max
	final := literalFrame(64, make([]byte, 1044992))
	virtual := func(p []byte, o int64) (int, error) {
		idx := o / int64(len(frame))
		pos := o % int64(len(frame))
		if idx == 63 {
			return reader(final)(p, pos)
		}
		// Rehash only once per frame, then serve header/body chunks.
		return reader(frame)(p, pos)
	}
	// Construct each maximum frame only as it is entered, keeping test memory bounded.
	current := int64(-1)
	bounded := func(p []byte, o int64) (int, error) {
		idx := o / int64(1048632)
		if idx < 63 && idx != current {
			frame = literalFrame(uint64(idx+1), max[24:24+1048576])
			current = idx
		}
		return virtual(p, o)
	}
	next, err := walk(bounded, 67108864, nil)
	if err != nil || next != 65 {
		t.Fatal("exact-cap history", next, err)
	}
	bad := append([]byte(nil), final[:24]...)
	binary.BigEndian.PutUint32(bad[12:16], 1044993)
	if feasible(bad, 63*1048632, 64) {
		t.Fatal("projected cap overflow header accepted")
	}
}

// Returned read errors remain read failures even with a complete count; a fault
// source is not a real storage qualification and EOF has distinct neighbors.
func TestReadErrors(t *testing.T) {
	b := golden(t)
	injected := errors.New("read fault")
	for _, count := range []int{0, 1, 24} {
		_, err := walk(func(p []byte, o int64) (int, error) { copy(p, b); return count, injected }, 57, nil)
		wantClass(t, err, StorageReadFailure)
		if !errors.Is(err, injected) {
			t.Fatal("read cause lost")
		}
	}
	for _, count := range []int{-1, 25} {
		_, err := walk(func([]byte, int64) (int, error) { return count, nil }, 57, nil)
		wantClass(t, err, StorageReadFailure)
	}
	_, err := walk(reader(nil), 57, nil)
	wantClass(t, err, IncompleteTail)
}

// Argument and budget failures are independently checked without storage. A
// successful charge/release cannot erase an earlier over-budget failure.
func TestArgumentsAndBudget(t *testing.T) {
	mask := uint32(0077)
	c := Config{Directory: "state", Records: "records", Inputs: "inputs", DeclaredUmask: &mask, SupportedLocalStorage: true, RecoveryPrerequisiteEstablished: true, ConsumerBudget: 32}
	if err := validate(c, []byte{'a'}, true); err != nil {
		t.Fatal("valid arguments", err)
	}
	for _, m := range []uint32{0, 0077} {
		copy := c
		copy.DeclaredUmask = &m
		if err := validate(copy, []byte{'a'}, true); err != nil {
			t.Fatal("valid declared mask", err)
		}
	}
	cases := []struct {
		label  string
		change func(*Config)
		class  Class
	}{
		{"missing mask", func(c *Config) { c.DeclaredUmask = nil }, InvalidState},
		{"restrictive mask", func(c *Config) { m := uint32(0277); c.DeclaredUmask = &m }, InvalidState},
		{"budget zero", func(c *Config) { c.ConsumerBudget = 0 }, ConsumerFailure},
		{"budget unrepresentable", func(c *Config) { c.ConsumerBudget = ^uint64(0) }, ConsumerFailure},
		{"deployment", func(c *Config) { c.SupportedLocalStorage = false }, UnsupportedStorage},
		{"escape", func(c *Config) { c.Inputs = "../inputs" }, InvalidState},
		{"absolute", func(c *Config) { c.Records = "/records" }, InvalidState},
		{"nul", func(c *Config) { c.Directory = "s\x00" }, InvalidState},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			copy := c
			tc.change(&copy)
			wantClass(t, validate(copy, []byte{'a'}, true), tc.class)
		})
	}
	for _, p := range [][]byte{nil, make([]byte, MaxPayload+1)} {
		wantClass(t, validate(c, p, true), PayloadInvalid)
	}
	c.RecoveryPrerequisiteEstablished = false
	wantClass(t, validate(c, nil, false), InvalidState)
	b := Budget{limit: 32}
	if b.Charge(32) != nil || b.Release(32) != nil || b.Charge(1) != nil {
		t.Fatal("budget positive control")
	}
	wantClass(t, b.Charge(32), ConsumerFailure)
	_ = b.Release(1)
	wantClass(t, b.Charge(1), ConsumerFailure)
	b = Budget{limit: 1}
	wantClass(t, b.Release(1), ConsumerFailure)
	wantClass(t, b.Charge(1), ConsumerFailure)
}

// Path syntax is checked before filesystem access, including reserved temporary
// names. Distinct ordinary basenames remain usable; no OS error is the oracle.
func TestBasenameGuards(t *testing.T) {
	for _, s := range []string{"x", "record.name", "input-1"} {
		if !basename(s) {
			t.Fatal("valid basename rejected")
		}
	}
	for _, s := range []string{"", ".", "..", "/absolute", "a/b", "a\x00b", ".record-tmp-x", string(bytes.Repeat([]byte{'x'}, 256))} {
		if basename(s) {
			t.Fatalf("unsafe basename accepted: %q", s)
		}
	}
	m := uint32(0077)
	c := Config{Records: "records", Inputs: "inputs", DeclaredUmask: &m, SupportedLocalStorage: true, ConsumerBudget: 1}
	wantClass(t, validate(c, []byte{'a'}, true), InvalidState)
}
