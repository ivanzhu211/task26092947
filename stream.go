package wirescope

import "sort"

// interval is a half-open logical byte range [start, end).
type interval struct {
	start uint64
	end   uint64
}

// stream tracks the coverage history and counters of one direction.
type stream struct {
	// segs holds the merged, sorted, non-overlapping seen intervals.
	segs []interval
	// high is the maximum end position seen so far (0 initially).
	high uint64
	// stats accumulates the counters for this direction.
	stats DirectionStats
}

// observe records the half-open range [start, start+length) and returns the
// number of newly covered and retransmitted bytes.
func (s *stream) observe(start, length uint64) (newBytes, retransBytes uint64) {
	end := start + length

	// First interval whose end reaches start (overlapping or touching).
	i := sort.Search(len(s.segs), func(k int) bool { return s.segs[k].end >= start })

	// Accumulate strict overlap with previously seen intervals and find the
	// merge range [i, j).
	j := i
	merged := interval{start: start, end: end}
	for j < len(s.segs) && s.segs[j].start <= end {
		seg := s.segs[j]
		lo := max(start, seg.start)
		hi := min(end, seg.end)
		if hi > lo {
			retransBytes += hi - lo
		}
		merged.start = min(merged.start, seg.start)
		merged.end = max(merged.end, seg.end)
		j++
	}
	newBytes = length - retransBytes

	// Replace the covered intervals with the merged one.
	s.segs = append(s.segs[:i], append([]interval{merged}, s.segs[j:]...)...)

	s.high = max(s.high, end)

	s.stats.PayloadBytes += length
	s.stats.UniqueBytes += newBytes
	s.stats.RetransBytes += retransBytes
	s.stats.Packets++
	if retransBytes > 0 {
		s.stats.RetransPackets++
	}
	return newBytes, retransBytes
}
