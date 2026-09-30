// Package wirescope implements online TCP retransmission statistics for a
// single bidirectional connection.
package wirescope

// Direction identifies one of the two directions of the connection.
type Direction int

const (
	// DirA is the direction whose first data byte has sequence number
	// initialSeqA passed to New.
	DirA Direction = iota
	// DirB is the opposite direction.
	DirB
)

// DirectionStats holds cumulative counters for one direction.
type DirectionStats struct {
	PayloadBytes   uint64 // total payload bytes observed (including retransmissions)
	UniqueBytes    uint64 // bytes covering previously unseen logical positions
	RetransBytes   uint64 // bytes overlapping previously seen logical positions
	Packets        uint64 // total data packets observed
	RetransPackets uint64 // packets overlapping any previously seen byte
}

// Stats holds per-direction and combined counters.
type Stats struct {
	A     DirectionStats
	B     DirectionStats
	Total DirectionStats
}

// WireScope tracks retransmission statistics for one bidirectional
// connection. The connection is never reused.
type WireScope struct {
	initialSeq [2]uint32
	streams    [2]stream
}

// seqSpace is the size of the 32-bit sequence number space.
const seqSpace = uint64(1) << 32

// window is the fixed window W (2^20) around the high-water mark within
// which every observed segment start is guaranteed to lie.
const window = uint64(1) << 20

// New creates an empty statistics instance. initialSeqA and initialSeqB are
// the sequence numbers of the first data byte in each direction.
func New(initialSeqA, initialSeqB uint32) *WireScope {
	return &WireScope{
		initialSeq: [2]uint32{initialSeqA, initialSeqB},
	}
}

// Observe records one segment carrying length payload bytes starting at
// sequence number seq in the given direction. It returns the number of newly
// covered bytes and the number of retransmitted bytes in this segment.
func (w *WireScope) Observe(dir Direction, seq uint32, length uint32) (newBytes, retransBytes uint64) {
	s := &w.streams[dir]
	start := w.logicalPos(dir, seq, s.high)
	return s.observe(start, uint64(length))
}

// Stats returns the current cumulative statistics.
func (w *WireScope) Stats() Stats {
	a := w.streams[DirA].stats
	b := w.streams[DirB].stats
	return Stats{
		A: a,
		B: b,
		Total: DirectionStats{
			PayloadBytes:   a.PayloadBytes + b.PayloadBytes,
			UniqueBytes:    a.UniqueBytes + b.UniqueBytes,
			RetransBytes:   a.RetransBytes + b.RetransBytes,
			Packets:        a.Packets + b.Packets,
			RetransPackets: a.RetransPackets + b.RetransPackets,
		},
	}
}

// logicalPos maps a 32-bit sequence number to its logical byte position in
// the given direction. The position p satisfies
//
//	p ≡ seq - initialSeq (mod 2^32)
//
// and, by the input contract, lies in [max(0, high-window), high+window).
// Because 2*window is far smaller than 2^32, exactly one such p exists: the
// representative of the residue class closest to the high-water mark.
func (w *WireScope) logicalPos(dir Direction, seq uint32, high uint64) uint64 {
	r := uint64(seq - w.initialSeq[dir])
	if high < r {
		// r - high < window << seqSpace/2, so the closest representative
		// is r itself.
		return r
	}
	// Round (high - r) / seqSpace to the nearest integer wrap count.
	wraps := (high - r + seqSpace/2) / seqSpace
	return r + wraps*seqSpace
}
