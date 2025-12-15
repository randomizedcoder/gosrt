package srt

import (
	"testing"

	"github.com/datarhei/gosrt/circular"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// Helper to create a NAK list from sequence numbers
// Pairs of (start, end) - same value for singles
func makeNakList(pairs [][2]uint32) []circular.Number {
	list := make([]circular.Number, 0, len(pairs)*2)
	for _, p := range pairs {
		list = append(list, circular.New(p[0], packet.MAX_SEQUENCENUMBER))
		list = append(list, circular.New(p[1], packet.MAX_SEQUENCENUMBER))
	}
	return list
}

func TestSplitNakList_Empty(t *testing.T) {
	chunks := splitNakList(nil, nakCIFMaxBytes)
	require.Nil(t, chunks)

	chunks = splitNakList([]circular.Number{}, nakCIFMaxBytes)
	require.Nil(t, chunks)
}

func TestSplitNakList_SingleEntry(t *testing.T) {
	list := makeNakList([][2]uint32{{100, 100}}) // Single

	chunks := splitNakList(list, nakCIFMaxBytes)

	require.Len(t, chunks, 1)
	require.Len(t, chunks[0], 2)
	require.Equal(t, uint32(100), chunks[0][0].Val())
	require.Equal(t, uint32(100), chunks[0][1].Val())
}

func TestSplitNakList_FitsInOneChunk(t *testing.T) {
	// 10 singles = 40 bytes, well under 1456 limit
	pairs := make([][2]uint32, 10)
	for i := 0; i < 10; i++ {
		pairs[i] = [2]uint32{uint32(i * 100), uint32(i * 100)}
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	require.Len(t, chunks, 1)
	require.Len(t, chunks[0], 20) // 10 pairs = 20 entries
}

func TestSplitNakList_ExactlyAtLimit(t *testing.T) {
	// Create exactly 364 singles (= 1456 bytes, exactly at limit)
	pairs := make([][2]uint32, 364)
	for i := 0; i < 364; i++ {
		pairs[i] = [2]uint32{uint32(i * 100), uint32(i * 100)}
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	require.Len(t, chunks, 1)
	require.Len(t, chunks[0], 728) // 364 pairs = 728 entries
}

func TestSplitNakList_JustOverLimit(t *testing.T) {
	// Create 365 singles (= 1460 bytes, just over 1456 limit)
	pairs := make([][2]uint32, 365)
	for i := 0; i < 365; i++ {
		pairs[i] = [2]uint32{uint32(i * 100), uint32(i * 100)}
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	require.Len(t, chunks, 2)
	// First chunk should have 364 singles
	require.Len(t, chunks[0], 728)
	// Second chunk should have 1 single
	require.Len(t, chunks[1], 2)
}

func TestSplitNakList_LargeList(t *testing.T) {
	// Create 1000 singles (= 4000 bytes, needs 3 packets)
	pairs := make([][2]uint32, 1000)
	for i := 0; i < 1000; i++ {
		pairs[i] = [2]uint32{uint32(i * 100), uint32(i * 100)}
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	// 1456/4 = 364 singles per chunk
	// 1000/364 = 2.74 -> need 3 chunks
	require.Len(t, chunks, 3)
	require.Len(t, chunks[0], 728) // 364 singles
	require.Len(t, chunks[1], 728) // 364 singles
	require.Len(t, chunks[2], 544) // 272 singles (1000 - 364*2)
}

func TestSplitNakList_Ranges(t *testing.T) {
	// Ranges take 8 bytes each
	// 182 ranges = 1456 bytes (at limit)
	pairs := make([][2]uint32, 182)
	for i := 0; i < 182; i++ {
		pairs[i] = [2]uint32{uint32(i * 1000), uint32(i*1000 + 10)} // Range
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	require.Len(t, chunks, 1)
	require.Len(t, chunks[0], 364) // 182 pairs = 364 entries
}

func TestSplitNakList_RangesOverLimit(t *testing.T) {
	// 200 ranges = 1600 bytes, needs 2 packets
	pairs := make([][2]uint32, 200)
	for i := 0; i < 200; i++ {
		pairs[i] = [2]uint32{uint32(i * 1000), uint32(i*1000 + 10)} // Range
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	require.Len(t, chunks, 2)
	// First chunk: 182 ranges (1456 bytes)
	require.Len(t, chunks[0], 364)
	// Second chunk: 18 ranges (144 bytes)
	require.Len(t, chunks[1], 36)
}

func TestSplitNakList_MixedSinglesAndRanges(t *testing.T) {
	// Mix: 100 singles (400 bytes) + 100 ranges (800 bytes) = 1200 bytes
	// Should fit in one packet
	pairs := make([][2]uint32, 200)
	for i := 0; i < 100; i++ {
		pairs[i] = [2]uint32{uint32(i * 100), uint32(i * 100)} // Single
	}
	for i := 0; i < 100; i++ {
		pairs[100+i] = [2]uint32{uint32(50000 + i*100), uint32(50000 + i*100 + 5)} // Range
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	require.Len(t, chunks, 1)
}

func TestSplitNakList_MixedOverflow(t *testing.T) {
	// Mix that overflows: 200 singles (800) + 100 ranges (800) = 1600 bytes
	pairs := make([][2]uint32, 300)
	for i := 0; i < 200; i++ {
		pairs[i] = [2]uint32{uint32(i * 100), uint32(i * 100)} // Single
	}
	for i := 0; i < 100; i++ {
		pairs[200+i] = [2]uint32{uint32(50000 + i*100), uint32(50000 + i*100 + 5)} // Range
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	require.Len(t, chunks, 2)
	t.Logf("Chunk 1: %d entries, Chunk 2: %d entries", len(chunks[0]), len(chunks[1]))
}

func TestSplitNakList_ExtremeScale_50k(t *testing.T) {
	// 50,000 singles = 200,000 bytes = ~137 packets
	pairs := make([][2]uint32, 50000)
	for i := 0; i < 50000; i++ {
		pairs[i] = [2]uint32{uint32(i * 100), uint32(i * 100)}
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	// 50000 / 364 = 137.36 -> 138 chunks
	require.Len(t, chunks, 138)

	// Verify total entries
	totalEntries := 0
	for _, chunk := range chunks {
		totalEntries += len(chunk) / 2
	}
	require.Equal(t, 50000, totalEntries)

	t.Logf("✅ 50,000 singles split into %d NAK packets", len(chunks))
}

func TestSplitNakList_PreservesOrder(t *testing.T) {
	// Verify entries come out in the same order they went in
	pairs := [][2]uint32{
		{100, 100},
		{200, 205},
		{300, 300},
		{400, 410},
		{500, 500},
	}
	list := makeNakList(pairs)

	chunks := splitNakList(list, nakCIFMaxBytes)

	require.Len(t, chunks, 1)

	// Verify order
	for i, p := range pairs {
		require.Equal(t, p[0], chunks[0][i*2].Val(), "Start mismatch at pair %d", i)
		require.Equal(t, p[1], chunks[0][i*2+1].Val(), "End mismatch at pair %d", i)
	}
}

// Benchmark the split function
func BenchmarkSplitNakList(b *testing.B) {
	sizes := []int{100, 1000, 10000, 50000}

	for _, size := range sizes {
		b.Run(formatBenchSize(size), func(b *testing.B) {
			pairs := make([][2]uint32, size)
			for i := 0; i < size; i++ {
				pairs[i] = [2]uint32{uint32(i * 100), uint32(i * 100)}
			}
			list := makeNakList(pairs)

			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				_ = splitNakList(list, nakCIFMaxBytes)
			}
		})
	}
}

func formatBenchSize(size int) string {
	if size >= 1000 {
		return string(rune('0'+size/1000)) + "k"
	}
	return string(rune('0'+size/100)) + "00"
}
