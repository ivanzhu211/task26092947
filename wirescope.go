// Package wirescope 提供单条双向 TCP 连接的在线重传统计。
package wirescope

// window 是输入起点相对 H 的保证范围：起点 ∈ [max(0,H-W), H+W)。
const window = 1 << 20

// seqSpace 是 TCP 序号空间大小 2^32。
const seqSpace = 1 << 32

// Direction 标识连接中的一个方向。
type Direction int

const (
	// DirA 表示以 initialSeqA 起始的方向。
	DirA Direction = iota
	// DirB 表示以 initialSeqB 起始的方向。
	DirB
)

// DirectionStats 是单个方向的累计统计。
type DirectionStats struct {
	PayloadBytes   uint64 // 载荷总字节数（含重传）
	UniqueBytes    uint64 // 唯一字节数（首次到达）
	RetransBytes   uint64 // 重传字节数
	Packets        uint64 // 载荷包数
	RetransPackets uint64 // 重传包数（覆盖过任一已见字节的包）
}

// Stats 是两个方向及合计的统计快照。
type Stats struct {
	A     DirectionStats
	B     DirectionStats
	Total DirectionStats
}

// interval 是左闭右开的逻辑字节范围 [start, end)。
type interval struct {
	start, end int64
}

// direction 保存单方向的覆盖历史与计数器。
type direction struct {
	// seen 为互不重叠、按起点升序排列的已见区间。
	seen []interval
	// high 是此前见过的最大结束位置，初值 0。
	high  int64
	stats DirectionStats
}

// Tracker 跟踪一条双向连接的重传统计。连接不会复用。
type Tracker struct {
	initSeq [2]uint32
	dirs    [2]direction
}

// New 以两个方向首个数据字节的序号建立空统计实例。
func New(initialSeqA, initialSeqB uint32) *Tracker {
	return &Tracker{initSeq: [2]uint32{initialSeqA, initialSeqB}}
}

// logicalStart 把 32 位 seq 换算为该方向的 64 位逻辑起点。
// 依据约定起点落在 [max(0,high-W), high+W) 内，该区间宽度 2W < 2^32，
// 故同余类中唯一候选即离 high 最近者。
func (t *Tracker) logicalStart(dir Direction, seq uint32) int64 {
	d := &t.dirs[dir]
	off := int64(seq - t.initSeq[dir]) // mod 2^32 的偏移，范围 [0, 2^32)
	// k = round((high - off) / 2^32)，向负无穷取整实现最近取整。
	n := d.high - off + seqSpace/2
	k := n / seqSpace
	if n < 0 && n%seqSpace != 0 {
		k--
	}
	return off + k*seqSpace
}

// Observe 输入一个分段，返回本包新增字节数和重传字节数。
func (t *Tracker) Observe(dir Direction, seq uint32, length uint32) (newBytes, retransBytes uint64) {
	d := &t.dirs[dir]
	start := t.logicalStart(dir, seq)
	end := start + int64(length)

	// 与已见区间的交集长度即重传字节数。
	overlap := d.covered(start, end)
	retransBytes = uint64(overlap)
	newBytes = uint64(end-start) - retransBytes

	d.insert(start, end)
	if end > d.high {
		d.high = end
	}

	s := &d.stats
	s.PayloadBytes += uint64(length)
	s.UniqueBytes += newBytes
	s.RetransBytes += retransBytes
	s.Packets++
	if retransBytes > 0 {
		s.RetransPackets++
	}
	return newBytes, retransBytes
}

// firstEndAfter 返回首个满足 seen[i].end > pos 的区间下标。
func (d *direction) firstEndAfter(pos int64) int {
	i, j := 0, len(d.seen)
	for i < j {
		m := (i + j) / 2
		if d.seen[m].end > pos {
			j = m
		} else {
			i = m + 1
		}
	}
	return i
}

// covered 返回 [start, end) 与已见区间集合的交集长度。
func (d *direction) covered(start, end int64) int64 {
	var total int64
	for i := d.firstEndAfter(start); i < len(d.seen) && d.seen[i].start < end; i++ {
		s := max64(d.seen[i].start, start)
		e := min64(d.seen[i].end, end)
		total += e - s
	}
	return total
}

// insert 把 [start, end) 并入已见区间集合并合并相邻/重叠区间。
func (d *direction) insert(start, end int64) {
	// lo 为首个 end >= start 的区间（相邻也参与合并）。
	lo := d.firstEndAfter(start - 1)
	hi := lo
	for hi < len(d.seen) && d.seen[hi].start <= end {
		end = max64(end, d.seen[hi].end)
		hi++
	}
	merged := interval{start, end}
	if lo < len(d.seen) && d.seen[lo].end >= start {
		merged.start = min64(start, d.seen[lo].start)
	}
	switch {
	case hi > lo:
		d.seen[lo] = merged
		d.seen = append(d.seen[:lo+1], d.seen[hi:]...)
	default:
		d.seen = append(d.seen, interval{})
		copy(d.seen[lo+1:], d.seen[lo:])
		d.seen[lo] = merged
	}
}

// Stats 返回当前累计统计。
func (t *Tracker) Stats() Stats {
	a := t.dirs[DirA].stats
	b := t.dirs[DirB].stats
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

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
