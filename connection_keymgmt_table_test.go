package srt

import (
	"testing"

	"github.com/randomizedcoder/gosrt/crypto"
	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Phase 2.2: Key Management Tests
// Table-driven tests for key management logic, encryption key handling,
// and key material validation.
// =============================================================================

// =============================================================================
// PacketEncryption Type Tests
// Tests for packet encryption enum validation, string representation,
// opposite key calculation, and value conversion.
// =============================================================================

func TestPacketEncryption_String_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		enc      packet.PacketEncryption
		expected string
	}{
		{
			name:     "Unencrypted",
			enc:      packet.UnencryptedPacket,
			expected: "unencrypted",
		},
		{
			name:     "Even key encrypted",
			enc:      packet.EvenKeyEncrypted,
			expected: "even key",
		},
		{
			name:     "Odd key encrypted",
			enc:      packet.OddKeyEncrypted,
			expected: "odd key",
		},
		{
			name:     "Even and odd key",
			enc:      packet.EvenAndOddKey,
			expected: "even and odd key",
		},
		{
			name:     "Unknown value (4)",
			enc:      packet.PacketEncryption(4),
			expected: "¯\\_(ツ)_/¯",
		},
		{
			name:     "Unknown value (255)",
			enc:      packet.PacketEncryption(255),
			expected: "¯\\_(ツ)_/¯",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.enc.String()
			require.Equal(t, tc.expected, result)
		})
	}
}

func TestPacketEncryption_IsValid_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		enc      packet.PacketEncryption
		expected bool
	}{
		{
			name:     "Unencrypted (0) - valid",
			enc:      packet.UnencryptedPacket,
			expected: true,
		},
		{
			name:     "Even key (1) - valid",
			enc:      packet.EvenKeyEncrypted,
			expected: true,
		},
		{
			name:     "Odd key (2) - valid",
			enc:      packet.OddKeyEncrypted,
			expected: true,
		},
		{
			name:     "Even and odd (3) - valid",
			enc:      packet.EvenAndOddKey,
			expected: true,
		},
		{
			name:     "Value 4 - invalid",
			enc:      packet.PacketEncryption(4),
			expected: false,
		},
		{
			name:     "Value 5 - invalid",
			enc:      packet.PacketEncryption(5),
			expected: false,
		},
		{
			name:     "Value 255 - invalid",
			enc:      packet.PacketEncryption(255),
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.enc.IsValid()
			require.Equal(t, tc.expected, result, "IsValid() mismatch for %v", tc.enc)
		})
	}
}

func TestPacketEncryption_Opposite_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		enc      packet.PacketEncryption
		expected packet.PacketEncryption
	}{
		{
			name:     "Even -> Odd",
			enc:      packet.EvenKeyEncrypted,
			expected: packet.OddKeyEncrypted,
		},
		{
			name:     "Odd -> Even",
			enc:      packet.OddKeyEncrypted,
			expected: packet.EvenKeyEncrypted,
		},
		{
			name:     "Unencrypted -> Unencrypted (no change)",
			enc:      packet.UnencryptedPacket,
			expected: packet.UnencryptedPacket,
		},
		{
			name:     "EvenAndOdd -> EvenAndOdd (no change)",
			enc:      packet.EvenAndOddKey,
			expected: packet.EvenAndOddKey,
		},
		{
			name:     "Invalid (4) -> Invalid (4) (no change)",
			enc:      packet.PacketEncryption(4),
			expected: packet.PacketEncryption(4),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.enc.Opposite()
			require.Equal(t, tc.expected, result, "Opposite() mismatch for %v", tc.enc)
		})
	}
}

func TestPacketEncryption_Val_TableDriven(t *testing.T) {
	testCases := []struct {
		name     string
		enc      packet.PacketEncryption
		expected uint32
	}{
		{"Unencrypted", packet.UnencryptedPacket, 0},
		{"Even key", packet.EvenKeyEncrypted, 1},
		{"Odd key", packet.OddKeyEncrypted, 2},
		{"Even and odd", packet.EvenAndOddKey, 3},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.enc.Val()
			require.Equal(t, tc.expected, result)
		})
	}
}

// =============================================================================
// Key Swap Logic Tests
// Tests for the key swap behavior used in key management.
// When a new key is exchanged, the opposite key becomes active.
// =============================================================================

func TestKeySwapSequence_TableDriven(t *testing.T) {
	// Simulates the key swap sequence during key refresh
	testCases := []struct {
		name          string
		initialKey    packet.PacketEncryption
		swapCount     int
		expectedFinal packet.PacketEncryption
	}{
		{
			name:          "Even -> 1 swap -> Odd",
			initialKey:    packet.EvenKeyEncrypted,
			swapCount:     1,
			expectedFinal: packet.OddKeyEncrypted,
		},
		{
			name:          "Even -> 2 swaps -> Even",
			initialKey:    packet.EvenKeyEncrypted,
			swapCount:     2,
			expectedFinal: packet.EvenKeyEncrypted,
		},
		{
			name:          "Odd -> 1 swap -> Even",
			initialKey:    packet.OddKeyEncrypted,
			swapCount:     1,
			expectedFinal: packet.EvenKeyEncrypted,
		},
		{
			name:          "Odd -> 3 swaps -> Even",
			initialKey:    packet.OddKeyEncrypted,
			swapCount:     3,
			expectedFinal: packet.EvenKeyEncrypted,
		},
		{
			name:          "Even -> 10 swaps -> Even",
			initialKey:    packet.EvenKeyEncrypted,
			swapCount:     10,
			expectedFinal: packet.EvenKeyEncrypted,
		},
		{
			name:          "Even -> 11 swaps -> Odd",
			initialKey:    packet.EvenKeyEncrypted,
			swapCount:     11,
			expectedFinal: packet.OddKeyEncrypted,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			key := tc.initialKey
			for i := 0; i < tc.swapCount; i++ {
				key = key.Opposite()
			}
			require.Equal(t, tc.expectedFinal, key, "Final key after %d swaps", tc.swapCount)
		})
	}
}

// =============================================================================
// KM Error Code Tests
// Tests for Key Material error code validation.
// =============================================================================

func TestKMErrorCodes_TableDriven(t *testing.T) {
	testCases := []struct {
		name      string
		errorCode uint32
		isError   bool
		errorType string
	}{
		{
			name:      "No error (0)",
			errorCode: 0,
			isError:   false,
			errorType: "",
		},
		{
			name:      "KM_NOSECRET (3)",
			errorCode: packet.KM_NOSECRET,
			isError:   true,
			errorType: "peer didn't enable encryption",
		},
		{
			name:      "KM_BADSECRET (4)",
			errorCode: packet.KM_BADSECRET,
			isError:   true,
			errorType: "peer has different passphrase",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			isError := tc.errorCode != 0
			require.Equal(t, tc.isError, isError)

			// Verify error code constants
			if tc.errorCode == packet.KM_NOSECRET {
				require.Equal(t, uint32(3), tc.errorCode)
			}
			if tc.errorCode == packet.KM_BADSECRET {
				require.Equal(t, uint32(4), tc.errorCode)
			}
		})
	}
}

// =============================================================================
// CIFKeyMaterialExtension Validation Tests
// Tests for key material extension structure validation.
// =============================================================================

func TestCIFKeyMaterialExtension_Validation_TableDriven(t *testing.T) {
	testCases := []struct {
		name          string
		setupKM       func() *packet.CIFKeyMaterialExtension
		expectValid   bool
		errorContains string
	}{
		{
			name: "Valid even key",
			setupKM: func() *packet.CIFKeyMaterialExtension {
				return &packet.CIFKeyMaterialExtension{
					KeyBasedEncryption: packet.EvenKeyEncrypted,
					Salt:               make([]byte, 16),
					Wrap:               make([]byte, 24), // 16 + 8 for AES-128
				}
			},
			expectValid: true,
		},
		{
			name: "Valid odd key",
			setupKM: func() *packet.CIFKeyMaterialExtension {
				return &packet.CIFKeyMaterialExtension{
					KeyBasedEncryption: packet.OddKeyEncrypted,
					Salt:               make([]byte, 16),
					Wrap:               make([]byte, 24),
				}
			},
			expectValid: true,
		},
		{
			name: "Valid both keys",
			setupKM: func() *packet.CIFKeyMaterialExtension {
				return &packet.CIFKeyMaterialExtension{
					KeyBasedEncryption: packet.EvenAndOddKey,
					Salt:               make([]byte, 16),
					Wrap:               make([]byte, 40), // 32 + 8 for two AES-128 keys
				}
			},
			expectValid: true,
		},
		{
			name: "Invalid unencrypted",
			setupKM: func() *packet.CIFKeyMaterialExtension {
				return &packet.CIFKeyMaterialExtension{
					KeyBasedEncryption: packet.UnencryptedPacket,
					Salt:               make([]byte, 16),
					Wrap:               make([]byte, 24),
				}
			},
			expectValid:   false,
			errorContains: "invalid key",
		},
		{
			name: "Invalid key type (4)",
			setupKM: func() *packet.CIFKeyMaterialExtension {
				return &packet.CIFKeyMaterialExtension{
					KeyBasedEncryption: packet.PacketEncryption(4),
					Salt:               make([]byte, 16),
					Wrap:               make([]byte, 24),
				}
			},
			expectValid:   false,
			errorContains: "invalid key",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			km := tc.setupKM()

			// Validate KeyBasedEncryption field
			isValid := km.KeyBasedEncryption != packet.UnencryptedPacket &&
				km.KeyBasedEncryption.IsValid()

			require.Equal(t, tc.expectValid, isValid,
				"KeyBasedEncryption validation mismatch for %v", km.KeyBasedEncryption)
		})
	}
}

// =============================================================================
// CIFKeyMaterialExtension String Tests
// Tests for key material extension string representation.
// =============================================================================

func TestCIFKeyMaterialExtension_String(t *testing.T) {
	km := &packet.CIFKeyMaterialExtension{
		S:                     0,
		Version:               1,
		PacketType:            2,
		Sign:                  0x2029,
		KeyBasedEncryption:    packet.EvenKeyEncrypted,
		KeyEncryptionKeyIndex: 0,
		Cipher:                2,
		Authentication:        0,
		StreamEncapsulation:   2,
		SLen:                  16,
		KLen:                  16,
		Salt:                  []byte{0x01, 0x02, 0x03, 0x04},
		Wrap:                  []byte{0x10, 0x20, 0x30, 0x40},
	}

	str := km.String()

	// Verify essential fields are present in string representation
	require.Contains(t, str, "KMExt")
	require.Contains(t, str, "version")
	require.Contains(t, str, "keyBasedEncryption")
}

// =============================================================================
// Crypto Key Generation Tests
// Additional tests for crypto key generation edge cases.
// =============================================================================

func TestCrypto_KeyGeneration_AllKeySizes(t *testing.T) {
	keySizes := []int{16, 24, 32}

	for _, keySize := range keySizes {
		t.Run(keyLengthName(keySize), func(t *testing.T) {
			// Create crypto instance
			c, err := crypto.New(keySize)
			require.NoError(t, err)
			require.NotNil(t, c)

			// Generate new even key
			err = c.GenerateSEK(packet.EvenKeyEncrypted)
			require.NoError(t, err)

			// Generate new odd key
			err = c.GenerateSEK(packet.OddKeyEncrypted)
			require.NoError(t, err)

			// Verify encryption/decryption still works after key regeneration
			testData := []byte("test data for encryption")
			dataCopy := make([]byte, len(testData))
			copy(dataCopy, testData)

			err = c.EncryptOrDecryptPayload(dataCopy, packet.EvenKeyEncrypted, 12345)
			require.NoError(t, err)

			// Data should be modified
			require.NotEqual(t, testData, dataCopy)

			// Decrypt should restore original
			err = c.EncryptOrDecryptPayload(dataCopy, packet.EvenKeyEncrypted, 12345)
			require.NoError(t, err)
			require.Equal(t, testData, dataCopy)
		})
	}
}

func keyLengthName(keyLen int) string {
	switch keyLen {
	case 16:
		return "AES-128"
	case 24:
		return "AES-192"
	case 32:
		return "AES-256"
	default:
		return "Unknown"
	}
}

// =============================================================================
// Crypto Marshal/Unmarshal Edge Cases
// =============================================================================

func TestCrypto_MarshalKM_FieldValues(t *testing.T) {
	// Verify MarshalKM sets correct field values
	c, err := crypto.New(16)
	require.NoError(t, err)

	km := &packet.CIFKeyMaterialExtension{}
	err = c.MarshalKM(km, "test-passphrase", packet.EvenKeyEncrypted)
	require.NoError(t, err)

	// Verify standard field values per SRT spec
	require.Equal(t, uint8(0), km.S, "S should be 0")
	require.Equal(t, uint8(1), km.Version, "Version should be 1")
	require.Equal(t, uint8(2), km.PacketType, "PacketType should be 2 (KMmsg)")
	require.Equal(t, uint16(0x2029), km.Sign, "Sign should be 'HAI' (0x2029)")
	require.Equal(t, uint8(2), km.Cipher, "Cipher should be 2 (AES-CTR)")
	require.Equal(t, uint8(0), km.Authentication, "Authentication should be 0")
	require.Equal(t, uint8(2), km.StreamEncapsulation, "StreamEncapsulation should be 2 (MPEG-TS/SRT)")
	require.Equal(t, uint16(16), km.SLen, "SLen should be 16 (128-bit salt)")
	require.Equal(t, uint16(16), km.KLen, "KLen should be 16 (AES-128)")

	// Salt should be 16 bytes
	require.Len(t, km.Salt, 16)

	// Wrap should be keyLen + 8 = 24 bytes for single key
	require.Len(t, km.Wrap, 24)
}

func TestCrypto_MarshalKM_KeyEncryptionTypes(t *testing.T) {
	testCases := []struct {
		name         string
		keyEnc       packet.PacketEncryption
		keyLen       int
		expectedWrap int // wrap length = n * keyLen + 8, where n = number of keys
	}{
		{
			name:         "Even key AES-128",
			keyEnc:       packet.EvenKeyEncrypted,
			keyLen:       16,
			expectedWrap: 24, // 16 + 8
		},
		{
			name:         "Odd key AES-128",
			keyEnc:       packet.OddKeyEncrypted,
			keyLen:       16,
			expectedWrap: 24, // 16 + 8
		},
		{
			name:         "Both keys AES-128",
			keyEnc:       packet.EvenAndOddKey,
			keyLen:       16,
			expectedWrap: 40, // 32 + 8
		},
		{
			name:         "Even key AES-192",
			keyEnc:       packet.EvenKeyEncrypted,
			keyLen:       24,
			expectedWrap: 32, // 24 + 8
		},
		{
			name:         "Both keys AES-192",
			keyEnc:       packet.EvenAndOddKey,
			keyLen:       24,
			expectedWrap: 56, // 48 + 8
		},
		{
			name:         "Even key AES-256",
			keyEnc:       packet.EvenKeyEncrypted,
			keyLen:       32,
			expectedWrap: 40, // 32 + 8
		},
		{
			name:         "Both keys AES-256",
			keyEnc:       packet.EvenAndOddKey,
			keyLen:       32,
			expectedWrap: 72, // 64 + 8
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := crypto.New(tc.keyLen)
			require.NoError(t, err)

			km := &packet.CIFKeyMaterialExtension{}
			err = c.MarshalKM(km, "test-passphrase-1234", tc.keyEnc)
			require.NoError(t, err)

			require.Len(t, km.Wrap, tc.expectedWrap,
				"Wrap length mismatch for %s", tc.name)
			require.Equal(t, tc.keyEnc, km.KeyBasedEncryption)
		})
	}
}

// =============================================================================
// Crypto UnmarshalKM Error Cases
// =============================================================================

func TestCrypto_UnmarshalKM_InvalidWrapLength(t *testing.T) {
	testCases := []struct {
		name      string
		keyLen    int
		keyEnc    packet.PacketEncryption
		wrapLen   int
		expectErr bool
	}{
		{
			name:      "AES-128 even - correct wrap (24)",
			keyLen:    16,
			keyEnc:    packet.EvenKeyEncrypted,
			wrapLen:   24,
			expectErr: false,
		},
		{
			name:      "AES-128 even - too short wrap (16)",
			keyLen:    16,
			keyEnc:    packet.EvenKeyEncrypted,
			wrapLen:   16,
			expectErr: true,
		},
		{
			name:      "AES-128 even - too long wrap (32)",
			keyLen:    16,
			keyEnc:    packet.EvenKeyEncrypted,
			wrapLen:   32,
			expectErr: true,
		},
		{
			name:      "AES-128 both - correct wrap (40)",
			keyLen:    16,
			keyEnc:    packet.EvenAndOddKey,
			wrapLen:   40,
			expectErr: false,
		},
		{
			name:      "AES-128 both - wrong wrap (24, single key size)",
			keyLen:    16,
			keyEnc:    packet.EvenAndOddKey,
			wrapLen:   24,
			expectErr: true,
		},
		{
			name:      "AES-256 even - correct wrap (40)",
			keyLen:    32,
			keyEnc:    packet.EvenKeyEncrypted,
			wrapLen:   40,
			expectErr: false,
		},
		{
			name:      "AES-256 even - wrong wrap (24)",
			keyLen:    32,
			keyEnc:    packet.EvenKeyEncrypted,
			wrapLen:   24,
			expectErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create crypto and marshal a valid KM first
			c1, err := crypto.New(tc.keyLen)
			require.NoError(t, err)

			validKM := &packet.CIFKeyMaterialExtension{}
			err = c1.MarshalKM(validKM, "passphrase-123", tc.keyEnc)
			require.NoError(t, err)

			// Create test KM with specific wrap length
			testKM := &packet.CIFKeyMaterialExtension{
				KeyBasedEncryption: tc.keyEnc,
				Salt:               validKM.Salt,
				Wrap:               make([]byte, tc.wrapLen),
			}

			// For correct length cases, use the valid wrap data
			if tc.wrapLen == len(validKM.Wrap) {
				copy(testKM.Wrap, validKM.Wrap)
			}

			// Create new crypto and try to unmarshal
			c2, err := crypto.New(tc.keyLen)
			require.NoError(t, err)

			err = c2.UnmarshalKM(testKM, "passphrase-123")

			if tc.expectErr {
				require.Error(t, err, "Expected error for %s", tc.name)
				require.ErrorIs(t, err, crypto.ErrInvalidWrap)
			} else {
				require.NoError(t, err, "Expected success for %s", tc.name)
			}
		})
	}
}

// =============================================================================
// KM Pre-Announce Countdown Logic Tests
// Tests for the key refresh pre-announce mechanism.
// =============================================================================

func TestKMPreAnnounceCountdown_Logic(t *testing.T) {
	testCases := []struct {
		name                string
		kmPreAnnounce       uint64
		countdown           uint64
		expectInPreAnnounce bool
	}{
		{
			name:                "countdown 0, preAnnounce 4 - in pre-announce period",
			kmPreAnnounce:       4,
			countdown:           0,
			expectInPreAnnounce: true,
		},
		{
			name:                "countdown 3, preAnnounce 4 - in pre-announce period",
			kmPreAnnounce:       4,
			countdown:           3,
			expectInPreAnnounce: true,
		},
		{
			name:                "countdown 4, preAnnounce 4 - NOT in pre-announce period",
			kmPreAnnounce:       4,
			countdown:           4,
			expectInPreAnnounce: false,
		},
		{
			name:                "countdown 5, preAnnounce 4 - NOT in pre-announce period",
			kmPreAnnounce:       4,
			countdown:           5,
			expectInPreAnnounce: false,
		},
		{
			name:                "countdown 0, preAnnounce 0 - NOT in pre-announce period",
			kmPreAnnounce:       0,
			countdown:           0,
			expectInPreAnnounce: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Logic from handleKMResponse:
			// if c.kmPreAnnounceCountdown >= c.config.KMPreAnnounce { ... ignore ... }
			inPreAnnounce := tc.countdown < tc.kmPreAnnounce
			require.Equal(t, tc.expectInPreAnnounce, inPreAnnounce)
		})
	}
}

// =============================================================================
// Sequence Number in Encryption Tests
// Tests for packet sequence number handling in encryption.
// =============================================================================

func TestCrypto_SequenceNumber_TableDriven(t *testing.T) {
	c, err := crypto.New(16)
	require.NoError(t, err)

	testCases := []struct {
		name   string
		seqNum uint32
	}{
		{"Sequence 0", 0},
		{"Sequence 1", 1},
		{"Sequence 12345", 12345},
		{"Sequence near max (2^31-1)", 0x7FFFFFFF},
		{"Sequence at 31-bit boundary", 0x7FFFFFFE},
		{"Sequence wrapping", 0x7FFFFF00},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			testData := []byte("test data for sequence number test")
			original := make([]byte, len(testData))
			copy(original, testData)

			// Encrypt with specific sequence number
			encryptErr := c.EncryptOrDecryptPayload(testData, packet.EvenKeyEncrypted, tc.seqNum)
			require.NoError(t, encryptErr)

			// Data should be different
			if len(testData) > 0 {
				require.NotEqual(t, original, testData)
			}

			// Decrypt with same sequence number should restore
			decryptErr := c.EncryptOrDecryptPayload(testData, packet.EvenKeyEncrypted, tc.seqNum)
			require.NoError(t, decryptErr)
			require.Equal(t, original, testData)
		})
	}
}

func TestCrypto_DifferentSequenceNumbers_ProduceDifferentCiphertext(t *testing.T) {
	c, err := crypto.New(16)
	require.NoError(t, err)

	plaintext := []byte("test data for comparing ciphertexts")

	// Encrypt with sequence 1
	data1 := make([]byte, len(plaintext))
	copy(data1, plaintext)
	err = c.EncryptOrDecryptPayload(data1, packet.EvenKeyEncrypted, 1)
	require.NoError(t, err)

	// Encrypt with sequence 2
	data2 := make([]byte, len(plaintext))
	copy(data2, plaintext)
	err = c.EncryptOrDecryptPayload(data2, packet.EvenKeyEncrypted, 2)
	require.NoError(t, err)

	// Ciphertexts should be different
	require.NotEqual(t, data1, data2,
		"Different sequence numbers should produce different ciphertexts")
}

// =============================================================================
// Cross-Key Encryption Tests
// Tests for ensuring even/odd keys produce different ciphertexts.
// =============================================================================

func TestCrypto_EvenOddKeys_ProduceDifferentCiphertext(t *testing.T) {
	c, err := crypto.New(16)
	require.NoError(t, err)

	plaintext := []byte("test data for even/odd key comparison")
	seqNum := uint32(12345)

	// Encrypt with even key
	dataEven := make([]byte, len(plaintext))
	copy(dataEven, plaintext)
	err = c.EncryptOrDecryptPayload(dataEven, packet.EvenKeyEncrypted, seqNum)
	require.NoError(t, err)

	// Encrypt with odd key
	dataOdd := make([]byte, len(plaintext))
	copy(dataOdd, plaintext)
	err = c.EncryptOrDecryptPayload(dataOdd, packet.OddKeyEncrypted, seqNum)
	require.NoError(t, err)

	// Ciphertexts should be different
	require.NotEqual(t, dataEven, dataOdd,
		"Even and odd keys should produce different ciphertexts")
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkPacketEncryption_Opposite(b *testing.B) {
	key := packet.EvenKeyEncrypted
	for i := 0; i < b.N; i++ {
		key = key.Opposite()
	}
}

func BenchmarkCrypto_EncryptDecrypt_AES128(b *testing.B) {
	c, _ := crypto.New(16)
	data := make([]byte, 1316) // Max SRT payload

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.EncryptOrDecryptPayload(data, packet.EvenKeyEncrypted, uint32(i))
	}
}

func BenchmarkCrypto_EncryptDecrypt_AES256(b *testing.B) {
	c, _ := crypto.New(32)
	data := make([]byte, 1316)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.EncryptOrDecryptPayload(data, packet.EvenKeyEncrypted, uint32(i))
	}
}

func BenchmarkCrypto_MarshalKM(b *testing.B) {
	c, _ := crypto.New(16)
	km := &packet.CIFKeyMaterialExtension{}
	passphrase := "benchmark-passphrase"

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.MarshalKM(km, passphrase, packet.EvenKeyEncrypted)
	}
}

func BenchmarkCrypto_GenerateSEK(b *testing.B) {
	c, _ := crypto.New(16)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.GenerateSEK(packet.EvenKeyEncrypted)
	}
}
