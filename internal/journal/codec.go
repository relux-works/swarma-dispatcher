package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
)

const (
	MaxPayload       = 1048576
	MaxFrame         = MaxPayload + 56
	FileCap    int64 = 67108864
	ioChunk          = 32768
)

func header(n int, seq uint64) [24]byte {
	var h [24]byte
	copy(h[:], "SDJRNL01")
	binary.BigEndian.PutUint16(h[8:10], 1)
	binary.BigEndian.PutUint32(h[12:16], uint32(n))
	binary.BigEndian.PutUint64(h[16:24], seq)
	return h
}

// feasible checks every present byte without allocating from an advertised N.
func feasible(h []byte, off int64, seq uint64) bool {
	if len(h) > 24 || off < 0 || off > FileCap-57 || seq == 0 {
		return false
	}
	u := FileCap - off - 56
	if u > MaxPayload {
		u = MaxPayload
	}
	fixed := header(1, seq)
	for i, b := range h {
		if (i < 12 || i >= 16) && b != fixed[i] {
			return false
		}
	}
	k := len(h) - 12
	if k < 0 {
		k = 0
	}
	if k > 4 {
		k = 4
	}
	var v uint64
	for i := 0; i < k; i++ {
		v = v<<8 | uint64(h[12+i])
	}
	shift := uint(8 * (4 - k))
	lo := v << shift
	hi := ((v + 1) << shift) - 1
	return lo <= uint64(u) && hi >= 1
}

type readAt func([]byte, int64) (int, error)

// readExact exposes all returned errors, including a full count with an error.
// Reads have no EINTR retry exception. Zero progress is EOF for a raw pread.
func readExact(read readAt, p []byte, off int64) (int, error) {
	total := 0
	for total < len(p) {
		end := len(p)
		if end-total > ioChunk {
			end = total + ioChunk
		}
		n, err := read(p[total:end], off+int64(total))
		if err != nil {
			return total, failure(StorageReadFailure, "pread", err)
		}
		if n < 0 || n > end-total {
			return total, failure(StorageReadFailure, "pread count", nil)
		}
		if n == 0 {
			return total, nil
		}
		total += n
	}
	return total, nil
}

// walk retains one frame payload/digest buffer and a constant header. Callbacks
// borrow the payload until return. No frame list or second assembled copy exists.
func walk(read readAt, size int64, visit func(uint64, []byte) error) (uint64, error) {
	if size < 0 || size > FileCap {
		return 0, failure(Corruption, "file cap", nil)
	}
	if size == 0 {
		return 0, failure(MissingState, "empty journal", nil)
	}
	off := int64(0)
	seq := uint64(1)
	var storage []byte
	for off < size {
		var h [24]byte
		want := int64(24)
		if size-off < want {
			want = size - off
		}
		n, err := readExact(read, h[:want], off)
		if err != nil {
			return 0, err
		}
		if !feasible(h[:n], off, seq) {
			return 0, failure(Corruption, "header", nil)
		}
		if n < 24 {
			return 0, failure(IncompleteTail, "header tail", nil)
		}
		length := int(binary.BigEndian.Uint32(h[12:16]))
		// feasible has checked both the payload cap and projected file cap.
		need := length + 32
		if cap(storage) < need {
			storage = nil
			storage = make([]byte, need)
		} else {
			storage = storage[:need]
		}
		wantBody := int64(need)
		if size-off-24 < wantBody {
			wantBody = size - off - 24
		}
		n, err = readExact(read, storage[:wantBody], off+24)
		if err != nil {
			return 0, err
		}
		if n != need {
			return 0, failure(IncompleteTail, "body tail", nil)
		}
		hash := sha256.New()
		_, _ = hash.Write(h[:])
		_, _ = hash.Write(storage[:length])
		var sum [32]byte
		hash.Sum(sum[:0])
		if !bytes.Equal(sum[:], storage[length:]) {
			return 0, failure(Corruption, "digest", nil)
		}
		if visit != nil {
			if err = visit(seq, storage[:length:length]); err != nil {
				return 0, err
			}
		}
		off += int64(56 + length)
		if seq == ^uint64(0) {
			seq = 0
		} else {
			seq++
		}
	}
	return seq, nil
}
