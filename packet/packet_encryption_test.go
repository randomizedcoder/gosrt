package packet

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// =============================================================================
// PacketEncryption Tests
// Tests for encryption enum validation, string representation,
// opposite key calculation, and value conversion.
// =============================================================================

func TestPacketEncryption_String_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		enc      PacketEncryption
		expected string
	}{
		{"Unencrypted", UnencryptedPacket, "unencrypted"},
		{"Even key", EvenKeyEncrypted, "even key"},
		{"Odd key", OddKeyEncrypted, "odd key"},
		{"Even and odd", EvenAndOddKey, "even and odd key"},
		{"Unknown (4)", PacketEncryption(4), "¯\\_(ツ)_/¯"},
		{"Unknown (255)", PacketEncryption(255), "¯\\_(ツ)_/¯"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.enc.String())
		})
	}
}

func TestPacketEncryption_IsValid_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		enc      PacketEncryption
		expected bool
	}{
		{"Unencrypted (0)", UnencryptedPacket, true},
		{"Even key (1)", EvenKeyEncrypted, true},
		{"Odd key (2)", OddKeyEncrypted, true},
		{"Even and odd (3)", EvenAndOddKey, true},
		{"Invalid (4)", PacketEncryption(4), false},
		{"Invalid (255)", PacketEncryption(255), false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.enc.IsValid())
		})
	}
}

func TestPacketEncryption_Opposite_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		enc      PacketEncryption
		expected PacketEncryption
	}{
		{"Even -> Odd", EvenKeyEncrypted, OddKeyEncrypted},
		{"Odd -> Even", OddKeyEncrypted, EvenKeyEncrypted},
		{"Unencrypted unchanged", UnencryptedPacket, UnencryptedPacket},
		{"EvenAndOdd unchanged", EvenAndOddKey, EvenAndOddKey},
		{"Invalid unchanged", PacketEncryption(4), PacketEncryption(4)},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.enc.Opposite())
		})
	}
}

func TestPacketEncryption_Val_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		enc      PacketEncryption
		expected uint32
	}{
		{"Unencrypted", UnencryptedPacket, 0},
		{"Even key", EvenKeyEncrypted, 1},
		{"Odd key", OddKeyEncrypted, 2},
		{"Even and odd", EvenAndOddKey, 3},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.expected, tc.enc.Val())
		})
	}
}

// =============================================================================
// KM Error Constants Tests
// =============================================================================

func TestKMErrorConstants(t *testing.T) {
	require.Equal(t, uint32(3), KM_NOSECRET)
	require.Equal(t, uint32(4), KM_BADSECRET)
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkPacketEncryption_Opposite(b *testing.B) {
	key := EvenKeyEncrypted
	for i := 0; i < b.N; i++ {
		key = key.Opposite()
	}
}

func BenchmarkPacketEncryption_IsValid(b *testing.B) {
	keys := []PacketEncryption{
		UnencryptedPacket,
		EvenKeyEncrypted,
		OddKeyEncrypted,
		EvenAndOddKey,
		PacketEncryption(4),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, k := range keys {
			_ = k.IsValid()
		}
	}
}
