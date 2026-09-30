package wirescope

import (
	"math/rand"
	"testing"
)

// refStream is a per-byte reference implementation used to cross-check the
// interval-based implementation on small ranges.
type refStream struct {
	seen  map[uint64]struct{}
	high  uint64
	stats DirectionStats
}

func newRefStream() *refStream {
	return &refStream{seen: make(map[uint64]struct{})}
}

func (r *refStream) observe(start, length uint64) (newBytes, retransBytes uint64) {
	for p := start; p < start+length; p++ {
		if _, ok := r.seen[p]; ok {
			retransBytes++
		} else {
			r.seen[p] = struct{}{}
			newBytes++
		}
	}
	if end := start + length; end > r.high {
		r.high = end
	}
	r.stats.PayloadBytes += length
	r.stats.UniqueBytes += newBytes
	r.stats.RetransBytes += retransBytes
	r.stats.Packets++
	if retransBytes > 0 {
		r.stats.RetransPackets++
	}
	return newBytes, retransBytes
}

// seqFor converts a logical position to a 32-bit sequence number.
func seqFor(initial uint32, pos uint64) uint32 {
	return initial + uint32(pos)
}

// packet is a test input expressed in logical positions.
type packet struct {
	dir    Direction
	start  uint64
	length uint64
}

// runPackets feeds packets into both the WireScope under test and the
// per-byte reference, comparing per-packet results and cumulative stats.
func runPackets(t *testing.T, initialSeqA, initialSeqB uint32, packets []packet) {
	t.Helper()
	w := New(initialSeqA, initialSeqB)
	refs := [2]*refStream{newRefStream(), newRefStream()}
	initials := [2]uint32{initialSeqA, initialSeqB}

	for i, p := range packets {
		if p.length == 0 || p.length > 65535 {
			t.Fatalf("packet %d: invalid length %d", i, p.length)
		}
		gotNew, gotRetrans := w.Observe(p.dir, seqFor(initials[p.dir], p.start), uint32(p.length))
		wantNew, wantRetrans := refs[p.dir].observe(p.start, p.length)
		if gotNew != wantNew || gotRetrans != wantRetrans {
			t.Fatalf("packet %d (%+v): got (new=%d, retrans=%d), want (new=%d, retrans=%d)",
				i, p, gotNew, gotRetrans, wantNew, wantRetrans)
		}
	}

	got := w.Stats()
	wantA := refs[DirA].stats
	wantB := refs[DirB].stats
	if got.A != wantA {
		t.Fatalf("stats A: got %+v, want %+v", got.A, wantA)
	}
	if got.B != wantB {
		t.Fatalf("stats B: got %+v, want %+v", got.B, wantB)
	}
	wantTotal := DirectionStats{
		PayloadBytes:   wantA.PayloadBytes + wantB.PayloadBytes,
		UniqueBytes:    wantA.UniqueBytes + wantB.UniqueBytes,
		RetransBytes:   wantA.RetransBytes + wantB.RetransBytes,
		Packets:        wantA.Packets + wantB.Packets,
		RetransPackets: wantA.RetransPackets + wantB.RetransPackets,
	}
	if got.Total != wantTotal {
		t.Fatalf("stats total: got %+v, want %+v", got.Total, wantTotal)
	}
}

// TestInterleavedDirections exercises both directions with gap filling,
// exact duplicates, and a partial overlap spanning multiple seen intervals.
func TestInterleavedDirections(t *testing.T) {
	packets := []packet{
		{DirA, 0, 10},   // A: new 10
		{DirB, 0, 5},    // B: new 5 (independent history)
		{DirA, 20, 10},  // A: gap [10,20), new 10
		{DirA, 5, 20},   // A: fills hole, overlaps [5,10) and [20,25)
		{DirA, 5, 20},   // A: exact duplicate of the previous segment
		{DirB, 3, 9},    // B: overlaps [3,5), extends to 12
		{DirA, 0, 30},   // A: spans multiple seen intervals, all retrans
		{DirA, 28, 12},  // A: retrans 2, new 12
		{DirB, 12, 1},   // B: adjacent to seen range, all new
		{DirB, 100, 10}, // B: far gap (within window), all new
		{DirB, 50, 60},  // B: overlaps [100,110) only, new 50
	}
	runPackets(t, 1000, 777777, packets)

	// Spot-check exact expectations independent of the reference.
	w := New(0, 0)
	if n, r := w.Observe(DirA, 0, 10); n != 10 || r != 0 {
		t.Fatalf("first segment: got (%d, %d), want (10, 0)", n, r)
	}
	if n, r := w.Observe(DirA, 0, 10); n != 0 || r != 10 {
		t.Fatalf("duplicate segment: got (%d, %d), want (0, 10)", n, r)
	}
	st := w.Stats()
	if st.A.Packets != 2 || st.A.RetransPackets != 1 ||
		st.A.PayloadBytes != 20 || st.A.UniqueBytes != 10 || st.A.RetransBytes != 10 {
		t.Fatalf("unexpected stats: %+v", st.A)
	}
	if st.B != (DirectionStats{}) {
		t.Fatalf("direction B must be untouched: %+v", st.B)
	}
}

// TestWrapAroundOutOfOrder uses an initial sequence number near 2^32 and
// delivers segments out of order across the wrap boundary.
func TestWrapAroundOutOfOrder(t *testing.T) {
	const initA = 0xFFFFFFF0 // 16 bytes before wrap
	const initB = 0xFFFFFFFA
	packets := []packet{
		{DirA, 16, 16}, // A: positions 16..32, seq wraps to 0..16
		{DirA, 0, 16},  // A: positions 0..16 arrive late, before the wrap
		{DirB, 6, 10},  // B: positions 6..16, crosses the wrap
		{DirB, 0, 6},   // B: positions 0..6 arrive late
		{DirA, 8, 16},  // A: straddles the wrap, half retrans
		{DirA, 32, 8},  // A: extends past the wrap, all new
		{DirA, 30, 4},  // A: overlaps both sides of position 32
		{DirB, 4, 12},  // B: duplicate spanning the wrap, all retrans
	}
	runPackets(t, initA, initB, packets)
}

// TestLongStreamMultipleWraps pushes an in-order stream across more than two
// full 2^32 wraps, then retransmits segments near the high-water mark.
func TestLongStreamMultipleWraps(t *testing.T) {
	const segLen = 65535
	const init = 0x12345678
	// Total positions: 2*2^32 + extra, crossing two full wraps.
	const total = 2*seqSpace + 3*window

	w := New(init, init)
	var pos uint64
	for pos < total {
		n, r := w.Observe(DirA, seqFor(init, pos), segLen)
		if n != segLen || r != 0 {
			t.Fatalf("in-order segment at %d: got (%d, %d), want (%d, 0)", pos, n, r, segLen)
		}
		pos += segLen
	}

	st := w.Stats()
	if st.A.UniqueBytes != pos || st.A.PayloadBytes != pos || st.A.RetransBytes != 0 {
		t.Fatalf("after in-order stream: %+v, want unique=payload=%d, retrans=0", st.A, pos)
	}

	// Retransmit two segments inside the window below the high-water mark.
	retransPos := pos - segLen // exact duplicate of the last segment
	if n, r := w.Observe(DirA, seqFor(init, retransPos), segLen); n != 0 || r != segLen {
		t.Fatalf("retransmitted tail: got (%d, %d), want (0, %d)", n, r, segLen)
	}
	secondPos := pos - window/2 // straddles a wrap boundary of its own
	if n, r := w.Observe(DirA, seqFor(init, secondPos), segLen); n != 0 || r != segLen {
		t.Fatalf("retransmitted mid-window: got (%d, %d), want (0, %d)", n, r, segLen)
	}

	st = w.Stats()
	want := DirectionStats{
		PayloadBytes:   pos + 2*segLen,
		UniqueBytes:    pos,
		RetransBytes:   2 * segLen,
		Packets:        pos/segLen + 2,
		RetransPackets: 2,
	}
	if st.A != want {
		t.Fatalf("final stats: got %+v, want %+v", st.A, want)
	}
	if st.Total != want {
		t.Fatalf("total should equal direction A: got %+v, want %+v", st.Total, want)
	}
}

// TestFuzzAgainstReference feeds random valid inputs in both directions and
// cross-checks every packet and the final stats against the per-byte
// reference. Initial sequence numbers sit near the 2^32 wrap.
func TestFuzzAgainstReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20261001))
	const initials = (1 << 32) - 500

	w := New(initials, initials)
	refs := [2]*refStream{newRefStream(), newRefStream()}

	for i := 0; i < 20000; i++ {
		dir := Direction(rng.Intn(2))
		ref := refs[dir]
		// Valid start range: [max(0, high-W), high+W).
		lo := uint64(0)
		if ref.high > window {
			lo = ref.high - window
		}
		span := ref.high + window - lo
		start := lo + uint64(rng.Int63n(int64(span)))
		length := uint64(1 + rng.Intn(64))

		gotNew, gotRetrans := w.Observe(dir, seqFor(initials, start), uint32(length))
		wantNew, wantRetrans := ref.observe(start, length)
		if gotNew != wantNew || gotRetrans != wantRetrans {
			t.Fatalf("packet %d dir=%d start=%d len=%d: got (%d, %d), want (%d, %d)",
				i, dir, start, length, gotNew, gotRetrans, wantNew, wantRetrans)
		}
	}

	got := w.Stats()
	if got.A != refs[DirA].stats {
		t.Fatalf("stats A: got %+v, want %+v", got.A, refs[DirA].stats)
	}
	if got.B != refs[DirB].stats {
		t.Fatalf("stats B: got %+v, want %+v", got.B, refs[DirB].stats)
	}
	if got.Total.PayloadBytes != got.A.PayloadBytes+got.B.PayloadBytes ||
		got.Total.UniqueBytes != got.A.UniqueBytes+got.B.UniqueBytes ||
		got.Total.RetransBytes != got.A.RetransBytes+got.B.RetransBytes ||
		got.Total.Packets != got.A.Packets+got.B.Packets ||
		got.Total.RetransPackets != got.A.RetransPackets+got.B.RetransPackets {
		t.Fatalf("total mismatch: %+v vs A=%+v B=%+v", got.Total, got.A, got.B)
	}
}
