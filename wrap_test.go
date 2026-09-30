package wirescope

import "testing"

// TestWrapAround 初始序号靠近 2^32，跨回绕乱序到达。
func TestWrapAround(t *testing.T) {
	const initA = 0xFFFFFFF0 // 距回绕 16 字节
	script := []seg{
		{DirA, 16, 16}, // [16,32) 越过回绕点，先到达，全新
		{DirA, 0, 16},  // [0,16) 乱序补齐回绕点之前，全新
		{DirA, 8, 16},  // [8,24) 跨回绕点的完全重复
		{DirA, 32, 8},  // [32,40) 回绕之后继续，全新
		{DirA, 24, 16}, // [24,40) 部分重叠：重传 8，新增 8
	}
	runScript(t, initA, 7777, script)
}

// TestWrapAroundB 方向 B 同样跨回绕，且两方向初始序号都靠近边界。
func TestWrapAroundB(t *testing.T) {
	script := []seg{
		{DirB, 8, 8},  // 越过回绕点先到
		{DirB, 0, 8},  // 乱序补齐
		{DirA, 0, 4},  // 方向 A 不受影响
		{DirB, 4, 12}, // [4,16) 部分重叠：重传 12，新增 4
	}
	runScript(t, 0xFFFFFFFE, 0xFFFFFFFC, script)
}

// TestLongStreamMultiLap 跨越两圈以上 2^32 边界的长流。
// 规模超过逐字节参照的可行范围，故用解析期望核对。
func TestLongStreamMultiLap(t *testing.T) {
	const initA = 0xFFFFFF00 // 起点本身靠近回绕
	const segLen = 65535
	// 总覆盖 > 2 圈：2*2^32 + 余量。
	const totalBytes = int64(2)*(1<<32) + 3*segLen

	tr := New(initA, 0)
	var pos int64
	var packets uint64
	for pos < totalBytes {
		n, r := tr.Observe(DirA, seqOf(initA, pos), segLen)
		if n != segLen || r != 0 {
			t.Fatalf("in-order at pos=%d: got (new=%d,retrans=%d)", pos, n, r)
		}
		pos += segLen
		packets++
	}

	// 窗口内乱序空洞补齐：先跳过一段再回头补。
	skipStart := pos
	n, r := tr.Observe(DirA, seqOf(initA, pos+segLen), segLen)
	if n != segLen || r != 0 {
		t.Fatalf("skip ahead: got (%d,%d)", n, r)
	}
	n, r = tr.Observe(DirA, seqOf(initA, skipStart), segLen)
	if n != segLen || r != 0 {
		t.Fatalf("backfill: got (%d,%d)", n, r)
	}
	packets += 2
	pos = skipStart + 2*segLen

	// 完全重复最近一段（第三圈上的 seq 与第一圈同值，须解析到正确圈）。
	n, r = tr.Observe(DirA, seqOf(initA, pos-segLen), segLen)
	if n != 0 || r != segLen {
		t.Fatalf("dup latest: got (%d,%d)", n, r)
	}
	packets++

	st := tr.Stats()
	wantUnique := uint64(pos)
	wantPayload := uint64(packets) * segLen
	if st.A.UniqueBytes != wantUnique {
		t.Fatalf("unique: got %d, want %d", st.A.UniqueBytes, wantUnique)
	}
	if st.A.PayloadBytes != wantPayload {
		t.Fatalf("payload: got %d, want %d", st.A.PayloadBytes, wantPayload)
	}
	if st.A.RetransBytes != segLen {
		t.Fatalf("retrans bytes: got %d, want %d", st.A.RetransBytes, segLen)
	}
	if st.A.Packets != packets || st.A.RetransPackets != 1 {
		t.Fatalf("packets: got %d/%d, want %d/1", st.A.Packets, st.A.RetransPackets, packets)
	}
	if st.B.Packets != 0 || st.B.UniqueBytes != 0 {
		t.Fatalf("dir B must stay empty: %+v", st.B)
	}
	if st.Total.UniqueBytes != wantUnique || st.Total.Packets != packets {
		t.Fatalf("total: %+v", st.Total)
	}
}
