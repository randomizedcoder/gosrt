package receive

import (
	"net"
	"testing"

	"github.com/randomizedcoder/gosrt/circular"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Table-Driven Drop Tests
// Tests for packet drop scenarios (too late, already ACKed, already received)
// ═══════════════════════════════════════════════════════════════════════════

// PacketBatch defines a batch of packets to push
type PacketBatch struct {
	StartSeq  uint32 // Starting sequence
	Count     int    // Number of packets
	TsbpdBase uint64 // TSBPD base time (packet i gets TsbpdBase + i + 1)
}

// DropTestCase defines parameters for drop tests
type DropTestCase struct {
	Name string

	// CODE_PARAM: Maps to Config.InitialSequenceNumber (critical for wraparound)
	StartSeq uint32 // Starting sequence for receiver (default 0)

	// CODE_PARAM: Maps to Config.TsbpdDelay (affects drop timing)
	TsbpdDelayUs uint64 // TSBPD delay (default uses implicit timing)

	InitialBatches  []PacketBatch // Batches to push before ticks
	TickTimes       []uint64      // Tick times to call
	MoreBatches     []PacketBatch // Additional batches after initial ticks
	MoreTickTimes   []uint64      // Additional tick times
	DuplicateSeq    uint32        // Sequence to push as duplicate
	DuplicateTsbpd  uint64        // TSBPD time for duplicate
	ExpectedDrops   uint64        // Expected PktDrop count
	ExpectedCP      uint32        // Expected contiguousPoint after test
	ExpectedLastACK uint32        // Expected lastACKSequenceNumber
	ExpectedMaxSeen uint32        // Expected maxSeenSequenceNumber
}

func TestRecvDrop_Table(t *testing.T) {
	t.Parallel()

	testCases := []DropTestCase{
		{
			Name: "TooLate",
			InitialBatches: []PacketBatch{
				{StartSeq: 0, Count: 10, TsbpdBase: 0}, // Packets 0-9, TSBPD 1-10
			},
			TickTimes:       []uint64{10}, // ACK period
			DuplicateSeq:    3,
			DuplicateTsbpd:  4,
			ExpectedDrops:   1,
			ExpectedCP:      9,
			ExpectedLastACK: 9,
			ExpectedMaxSeen: 9,
		},
		{
			Name: "AlreadyACK",
			InitialBatches: []PacketBatch{
				{StartSeq: 0, Count: 5, TsbpdBase: 0},  // Packets 0-4, TSBPD 1-5
				{StartSeq: 5, Count: 5, TsbpdBase: 10}, // Packets 5-9, TSBPD 16-20
			},
			TickTimes:       []uint64{10}, // ACK period
			DuplicateSeq:    6,            // Seq 6 <= contiguousPoint(9)
			DuplicateTsbpd:  7,
			ExpectedDrops:   1,
			ExpectedCP:      9, // All contiguous
			ExpectedLastACK: 9,
			ExpectedMaxSeen: 9,
		},
		{
			Name: "AlreadyRecvNoACK",
			InitialBatches: []PacketBatch{
				{StartSeq: 0, Count: 5, TsbpdBase: 0},  // Packets 0-4, TSBPD 1-5
				{StartSeq: 5, Count: 5, TsbpdBase: 10}, // Packets 5-9, TSBPD 16-20
			},
			TickTimes: []uint64{10}, // First ACK - contiguousPoint -> 9
			MoreBatches: []PacketBatch{
				{StartSeq: 10, Count: 10, TsbpdBase: 20}, // Packets 10-19, TSBPD 21-30
			},
			MoreTickTimes:   []uint64{20}, // Second ACK - contiguousPoint -> 19
			DuplicateSeq:    15,           // Seq 15 <= contiguousPoint(19)
			DuplicateTsbpd:  26,
			ExpectedDrops:   1,
			ExpectedCP:      19,
			ExpectedLastACK: 19,
			ExpectedMaxSeen: 19,
		},
		// ═══════════════════════════════════════════════════════════════════════
		// CORNER CASE TESTS: StartSeq extremes
		// Note: mockLiveRecvWithStartSeq uses TsbpdDelay=100_000 (100ms)
		// So tick time must be > TsbpdBase + i + TsbpdDelay to expire packets
		// ═══════════════════════════════════════════════════════════════════════

		// Corner: StartSeq=0 (explicit baseline)
		{
			Name:     "Corner_StartSeq_Zero",
			StartSeq: 0,
			InitialBatches: []PacketBatch{
				{StartSeq: 0, Count: 10, TsbpdBase: 0},
			},
			TickTimes:       []uint64{200_000},
			DuplicateSeq:    5,
			DuplicateTsbpd:  6,
			ExpectedDrops:   1,
			ExpectedCP:      9,
			ExpectedLastACK: 9,
			ExpectedMaxSeen: 9,
		},

		// Corner: StartSeq near MAX (MAX-100)
		{
			Name:     "Corner_StartSeq_NearMax",
			StartSeq: packet.MAX_SEQUENCENUMBER - 100,
			InitialBatches: []PacketBatch{
				{StartSeq: packet.MAX_SEQUENCENUMBER - 100, Count: 10, TsbpdBase: 0},
			},
			TickTimes:      []uint64{200_000},
			DuplicateSeq:   packet.MAX_SEQUENCENUMBER - 95,
			DuplicateTsbpd: 6,
			ExpectedDrops:  1,
			// MAX-100 + 9 = MAX-91
			ExpectedCP:      packet.MAX_SEQUENCENUMBER - 91,
			ExpectedLastACK: packet.MAX_SEQUENCENUMBER - 91,
			ExpectedMaxSeen: packet.MAX_SEQUENCENUMBER - 91,
		},

		// Corner: StartSeq at MAX (crosses wraparound boundary)
		{
			Name:     "Corner_StartSeq_AtMax",
			StartSeq: packet.MAX_SEQUENCENUMBER,
			InitialBatches: []PacketBatch{
				// Start at MAX, wraps to 0, 1, 2, ... 8
				{StartSeq: packet.MAX_SEQUENCENUMBER, Count: 10, TsbpdBase: 0},
			},
			TickTimes:      []uint64{200_000},
			DuplicateSeq:   3, // Wrapped sequence
			DuplicateTsbpd: 5,
			ExpectedDrops:  1,
			// MAX + 9 wraps to 8
			ExpectedCP:      8,
			ExpectedLastACK: 8,
			ExpectedMaxSeen: 8,
		},

		// Corner: Wraparound boundary test (original)
		{
			Name:     "Corner_Wraparound_NearMax",
			StartSeq: packet.MAX_SEQUENCENUMBER - 5, // Receiver starts near MAX
			InitialBatches: []PacketBatch{
				// Packets MAX-5 to MAX-5+9 (i.e., MAX-5 to 3 after wrap)
				{StartSeq: packet.MAX_SEQUENCENUMBER - 5, Count: 10, TsbpdBase: 0},
			},
			TickTimes:      []uint64{200_000},
			DuplicateSeq:   packet.MAX_SEQUENCENUMBER - 3, // Duplicate near MAX
			DuplicateTsbpd: 3,
			ExpectedDrops:  1,
			// After wrap: MAX-5 + 9 packets = seq 3
			ExpectedCP:      3,
			ExpectedLastACK: 3,
			ExpectedMaxSeen: 3,
		},

		// ═══════════════════════════════════════════════════════════════════════
		// NOTE (DISC-004): TsbpdDelayUs corner tests deferred
		// The test runner uses mockLiveRecvWithStartSeq which has fixed TsbpdDelay=100_000.
		// To test different TSBPD delays, we would need to modify the mock or create
		// a new receiver factory. This is deferred for later implementation.
		// ═══════════════════════════════════════════════════════════════════════
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			// Use mockLiveRecvWithStartSeq if StartSeq is non-zero
			var recv *receiver
			if tc.StartSeq != 0 {
				recv = mockLiveRecvWithStartSeq(tc.StartSeq, nil, nil, nil)
			} else {
				recv = mockLiveRecv(nil, nil, nil)
			}
			addr, _ := net.ResolveIPAddr("ip", "127.0.0.1")

			// Push initial batches
			for _, batch := range tc.InitialBatches {
				for i := 0; i < batch.Count; i++ {
					p := packet.NewPacket(addr)
					p.Header().PacketSequenceNumber = circular.New(batch.StartSeq+uint32(i), packet.MAX_SEQUENCENUMBER)
					p.Header().PktTsbpdTime = batch.TsbpdBase + uint64(i) + 1
					recv.Push(p)
				}
			}

			// Run initial ticks
			for _, tickTime := range tc.TickTimes {
				recv.Tick(tickTime)
			}

			// Push more batches if specified
			for _, batch := range tc.MoreBatches {
				for i := 0; i < batch.Count; i++ {
					p := packet.NewPacket(addr)
					p.Header().PacketSequenceNumber = circular.New(batch.StartSeq+uint32(i), packet.MAX_SEQUENCENUMBER)
					p.Header().PktTsbpdTime = batch.TsbpdBase + uint64(i) + 1
					recv.Push(p)
				}
			}

			// Run more ticks if specified
			for _, tickTime := range tc.MoreTickTimes {
				recv.Tick(tickTime)
			}

			// Verify state before duplicate push
			stats := recv.Stats()
			require.Equal(t, uint64(0), stats.PktDrop, "No drops before duplicate")
			require.Equal(t, tc.ExpectedCP, recv.contiguousPoint.Load(), "contiguousPoint before duplicate")
			require.Equal(t, tc.ExpectedLastACK, recv.lastACKSequenceNumber.Val(), "lastACK before duplicate")
			require.Equal(t, tc.ExpectedMaxSeen, recv.maxSeenSequenceNumber.Val(), "maxSeen before duplicate")

			// Push duplicate
			dupPkt := packet.NewPacket(addr)
			dupPkt.Header().PacketSequenceNumber = circular.New(tc.DuplicateSeq, packet.MAX_SEQUENCENUMBER)
			dupPkt.Header().PktTsbpdTime = tc.DuplicateTsbpd
			recv.Push(dupPkt)

			// Verify drop occurred
			stats = recv.Stats()
			require.Equal(t, tc.ExpectedDrops, stats.PktDrop, "Expected drop count after duplicate")
		})
	}
}
