package journal

import (
	"encoding/binary"
	"runtime"
	"testing"
)

// Claim (spec :271-276; api.go Budget doc): a charge larger than the remaining
// budget fails and latches even when used+n would overflow uint64. Control: a
// fitting charge succeeds first. Control plant: O-charge-overflow.
func TestGuardBudgetOverflow(t *testing.T) {
	b := Budget{limit: 32}
	if b.Charge(1) != nil {
		t.Fatal("fitting charge refused")
	}
	if ClassOf(b.Charge(^uint64(0))) != ConsumerFailure {
		t.Fatal("FINDING: overflowing charge accepted")
	}
	if ClassOf(b.Charge(1)) != ConsumerFailure {
		t.Fatal("FINDING: budget failure not sticky")
	}
}

// Claim (spec 28-29): invalid N=32 MiB refuses before allocating 4 MiB.
// The literal golden frame is a valid neighbor. No tests run in parallel here.
func TestGuardHeaderNoAllocModerate(t *testing.T) {
	_, controlErr := walk(reader(golden(t)), 57, nil)
	must(t, controlErr)
	h := golden(t)[:24]
	binary.BigEndian.PutUint32(h[12:16], 32<<20)
	read := func(p []byte, o int64) (int, error) {
		if o >= 24 {
			return 0, nil
		}
		return copy(p, h[o:]), nil
	}
	var m0, m1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&m0)
	_, err := walk(read, 24, nil)
	runtime.ReadMemStats(&m1)
	wantClass(t, err, Corruption)
	if d := m1.TotalAlloc - m0.TotalAlloc; d >= 4<<20 {
		t.Fatalf("HEADER-ALLOC: invalid header allocated %d bytes before refusal", d)
	}
}
