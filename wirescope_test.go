package wirescope

import "testing"

// refDirection 是逐字节参照实现：记录每个已见逻辑位置。
type refDirection struct {
	seen  map[int64]bool
	stats DirectionStats
}

func newRefDirection() *refDirection {
	return &refDirection{seen: make(map[int64]bool)}
}

func (r *refDirection) observe(start, length int64) (newBytes, retransBytes uint64) {
	for p := start; p < start+length; p++ {
		if r.seen[p] {
			retransBytes++
		} else {
			newBytes++
			r.seen[p] = true
		}
	}
	r.stats.PayloadBytes += uint64(length)
	r.stats.UniqueBytes += newBytes
	r.stats.RetransBytes += retransBytes
	r.stats.Packets++
	if retransBytes > 0 {
		r.stats.RetransPackets++
	}
	return newBytes, retransBytes
}

// seg 描述一次输入：方向、逻辑起点、长度。
type seg struct {
	dir    Direction
	start  int64
	length int64
}

// seqOf 把逻辑位置换算为 32 位 seq。
func seqOf(initSeq uint32, pos int64) uint32 {
	return initSeq + uint32(pos)
}

func runScript(t *testing.T, initSeqA, initSeqB uint32, script []seg) *Tracker {
	t.Helper()
	tr := New(initSeqA, initSeqB)
	refs := [2]*refDirection{newRefDirection(), newRefDirection()}
	inits := [2]uint32{initSeqA, initSeqB}
	for i, s := range script {
		seq := seqOf(inits[s.dir], s.start)
		gotNew, gotRetrans := tr.Observe(s.dir, seq, uint32(s.length))
		wantNew, wantRetrans := refs[s.dir].observe(s.start, s.length)
		if gotNew != wantNew || gotRetrans != wantRetrans {
			t.Fatalf("packet %d (%v): got (new=%d,retrans=%d), want (new=%d,retrans=%d)",
				i, s, gotNew, gotRetrans, wantNew, wantRetrans)
		}
	}
	st := tr.Stats()
	if st.A != refs[DirA].stats {
		t.Fatalf("dir A stats: got %+v, want %+v", st.A, refs[DirA].stats)
	}
	if st.B != refs[DirB].stats {
		t.Fatalf("dir B stats: got %+v, want %+v", st.B, refs[DirB].stats)
	}
	wantTotal := DirectionStats{
		PayloadBytes:   st.A.PayloadBytes + st.B.PayloadBytes,
		UniqueBytes:    st.A.UniqueBytes + st.B.UniqueBytes,
		RetransBytes:   st.A.RetransBytes + st.B.RetransBytes,
		Packets:        st.A.Packets + st.B.Packets,
		RetransPackets: st.A.RetransPackets + st.B.RetransPackets,
	}
	if st.Total != wantTotal {
		t.Fatalf("total stats: got %+v, want %+v", st.Total, wantTotal)
	}
	return tr
}

// TestInterleaved 两个方向交错输入，含空洞补齐、完全重复、
// 一次跨越多个已见区间的部分重叠。
func TestInterleaved(t *testing.T) {
	script := []seg{
		// 方向 A：先留空洞 [0,10)，再补齐。
		{DirA, 10, 10}, // [10,20) 全新
		{DirA, 0, 10},  // [0,10) 补洞，全新
		{DirA, 0, 20},  // 完全重复
		// 方向 B 独立演进，与 A 使用相同逻辑位置互不影响。
		{DirB, 0, 5},   // [0,5) 全新
		{DirB, 10, 5},  // [10,15) 全新（留洞）
		{DirB, 20, 5},  // [20,25) 全新（留洞）
		{DirB, 3, 19},  // [3,22) 一次跨越三个已见区间：重传 2+5+2=9，新增 10
		{DirA, 15, 10}, // [15,25) 部分重叠：重传 5，新增 5
		{DirA, 25, 3},  // [25,28) 全新
		{DirA, 26, 2},  // [26,28) 完全重复
		{DirB, 0, 25},  // 完全重复（洞已补齐）
	}
	runScript(t, 1000, 5000, script)
}

// TestExplicitCounts 不依赖参照实现，直接核对关键数值。
func TestExplicitCounts(t *testing.T) {
	tr := New(100, 200)

	if n, r := tr.Observe(DirA, 100, 10); n != 10 || r != 0 {
		t.Fatalf("got (%d,%d)", n, r)
	}
	if n, r := tr.Observe(DirA, 100, 10); n != 0 || r != 10 {
		t.Fatalf("dup: got (%d,%d)", n, r)
	}
	// 同一 seq 在另一方向是独立流。
	if n, r := tr.Observe(DirB, 100, 10); n != 10 || r != 0 {
		t.Fatalf("dir B independent: got (%d,%d)", n, r)
	}
	st := tr.Stats()
	if st.A.Packets != 2 || st.A.RetransPackets != 1 || st.A.PayloadBytes != 20 ||
		st.A.UniqueBytes != 10 || st.A.RetransBytes != 10 {
		t.Fatalf("dir A: %+v", st.A)
	}
	if st.B.Packets != 1 || st.B.RetransPackets != 0 {
		t.Fatalf("dir B: %+v", st.B)
	}
	if st.Total.Packets != 3 || st.Total.PayloadBytes != 30 || st.Total.UniqueBytes != 20 {
		t.Fatalf("total: %+v", st.Total)
	}
}
