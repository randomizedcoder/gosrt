package crypto

import (
	"fmt"
	"testing"

	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/require"
)

// ═══════════════════════════════════════════════════════════════════════════
// Crypto Table-Driven Tests
// Tests key management, encryption/decryption, and error handling.
// ═══════════════════════════════════════════════════════════════════════════

// CryptoKeyTestCase tests key length validation
type CryptoKeyTestCase struct {
	Name        string
	KeyLength   int
	ExpectError bool
}

var cryptoKeyTests = []CryptoKeyTestCase{
	// Valid key lengths (AES variants)
	{
		Name:        "Valid_AES128",
		KeyLength:   16,
		ExpectError: false,
	},
	{
		Name:        "Valid_AES192",
		KeyLength:   24,
		ExpectError: false,
	},
	{
		Name:        "Valid_AES256",
		KeyLength:   32,
		ExpectError: false,
	},
	// Invalid key lengths
	{
		Name:        "Invalid_Zero",
		KeyLength:   0,
		ExpectError: true,
	},
	{
		Name:        "Invalid_TooSmall",
		KeyLength:   8,
		ExpectError: true,
	},
	{
		Name:        "Invalid_Between16And24",
		KeyLength:   20,
		ExpectError: true,
	},
	{
		Name:        "Invalid_Between24And32",
		KeyLength:   28,
		ExpectError: true,
	},
	{
		Name:        "Invalid_TooLarge",
		KeyLength:   64,
		ExpectError: true,
	},
	{
		Name:        "Invalid_Negative",
		KeyLength:   -1,
		ExpectError: true,
	},
	// Corner cases
	{
		Name:        "Corner_15",
		KeyLength:   15,
		ExpectError: true,
	},
	{
		Name:        "Corner_17",
		KeyLength:   17,
		ExpectError: true,
	},
	{
		Name:        "Corner_23",
		KeyLength:   23,
		ExpectError: true,
	},
	{
		Name:        "Corner_25",
		KeyLength:   25,
		ExpectError: true,
	},
	{
		Name:        "Corner_31",
		KeyLength:   31,
		ExpectError: true,
	},
	{
		Name:        "Corner_33",
		KeyLength:   33,
		ExpectError: true,
	},
}

func TestCrypto_KeyLength_Table(t *testing.T) {
	for _, tc := range cryptoKeyTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			runCryptoKeyTest(t, tc)
		})
	}
}

func runCryptoKeyTest(t *testing.T, tc CryptoKeyTestCase) {
	t.Helper()

	c, err := New(tc.KeyLength)

	if tc.ExpectError {
		require.Error(t, err, "Expected error for key length %d", tc.KeyLength)
		require.Nil(t, c)
		t.Logf("✅ %s: rejected key length %d as expected", tc.Name, tc.KeyLength)
	} else {
		require.NoError(t, err, "Expected success for key length %d", tc.KeyLength)
		require.NotNil(t, c)
		t.Logf("✅ %s: accepted key length %d", tc.Name, tc.KeyLength)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Key Material Unmarshal Tests
// ═══════════════════════════════════════════════════════════════════════════

// KMUnmarshalTestCase tests key material unmarshaling
type KMUnmarshalTestCase struct {
	Name             string
	KeyLength        int
	Salt             string
	Wrap             string
	Passphrase       string
	KeyEncryption    packet.PacketEncryption
	ExpectError      bool
	ExpectedErrorType error
}

var kmUnmarshalTests = []KMUnmarshalTestCase{
	// Valid cases - AES-128
	{
		Name:          "Valid_AES128_Even",
		KeyLength:     16,
		Salt:          "6c438852715a4d26e0e810b3132ca61f",
		Wrap:          "699ab4eac6b7c66c3a9fa0d6836326c2b294a10764233356",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.EvenKeyEncrypted,
		ExpectError:   false,
	},
	{
		Name:          "Valid_AES128_Odd",
		KeyLength:     16,
		Salt:          "6c438852715a4d26e0e810b3132ca61f",
		Wrap:          "ca4decaaf8d7b5c38288e84c8796929c84b7c139f1f769d5",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.OddKeyEncrypted,
		ExpectError:   false,
	},
	{
		Name:          "Valid_AES128_EvenOdd",
		KeyLength:     16,
		Salt:          "6c438852715a4d26e0e810b3132ca61f",
		Wrap:          "5b901889bd106609ca8a83264b12ed1bfab3f02812bad65784ac396b1f57eb16c53e1020d3a3250b",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.EvenAndOddKey,
		ExpectError:   false,
	},
	// Valid cases - AES-192
	{
		Name:          "Valid_AES192_Even",
		KeyLength:     24,
		Salt:          "e636259ccc41e73611b9363bb58586b1",
		Wrap:          "8c6502d6a83e0ab894a43cb5b37b71c2755afc64a682bed9d46912138b60f384",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.EvenKeyEncrypted,
		ExpectError:   false,
	},
	// Valid cases - AES-256
	{
		Name:          "Valid_AES256_Even",
		KeyLength:     32,
		Salt:          "3825bb4163f7d5cf2804ec0b31a7370f",
		Wrap:          "7d1578458e41680dd997d1a185c75753f3344c6711542b35833f881f7c480304cbe9bdbe76035914",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.EvenKeyEncrypted,
		ExpectError:   false,
	},
	// Invalid cases
	{
		Name:              "Invalid_WrongPassphrase",
		KeyLength:         16,
		Salt:              "6c438852715a4d26e0e810b3132ca61f",
		Wrap:              "699ab4eac6b7c66c3a9fa0d6836326c2b294a10764233356",
		Passphrase:        "wrongpassword",
		KeyEncryption:     packet.EvenKeyEncrypted,
		ExpectError:       true,
		ExpectedErrorType: ErrDecryptionFailed, // Should wrap keywrap error with our semantic error
	},
	{
		Name:              "Invalid_UnencryptedKey",
		KeyLength:         16,
		Salt:              "6c438852715a4d26e0e810b3132ca61f",
		Wrap:              "699ab4eac6b7c66c3a9fa0d6836326c2b294a10764233356",
		Passphrase:        "foobarfoobar",
		KeyEncryption:     packet.UnencryptedPacket,
		ExpectError:       true,
		ExpectedErrorType: ErrInvalidKey,
	},
}

func TestCrypto_KMUnmarshal_Table(t *testing.T) {
	for _, tc := range kmUnmarshalTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			runKMUnmarshalTest(t, tc)
		})
	}
}

func runKMUnmarshalTest(t *testing.T, tc KMUnmarshalTestCase) {
	t.Helper()

	c, err := New(tc.KeyLength)
	require.NoError(t, err)

	km := &packet.CIFKeyMaterialExtension{}
	km.KeyBasedEncryption = tc.KeyEncryption
	km.Salt = mustDecodeString(tc.Salt)
	km.Wrap = mustDecodeString(tc.Wrap)

	err = c.UnmarshalKM(km, tc.Passphrase)

	if tc.ExpectError {
		require.Error(t, err, "Expected error for %s", tc.Name)
		if tc.ExpectedErrorType != nil {
			require.ErrorIs(t, err, tc.ExpectedErrorType)
		}
		t.Logf("✅ %s: unmarshaling failed as expected", tc.Name)
	} else {
		require.NoError(t, err, "Expected success for %s", tc.Name)
		t.Logf("✅ %s: unmarshaling succeeded", tc.Name)
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Key Material Marshal Tests
// ═══════════════════════════════════════════════════════════════════════════

type KMMarshalTestCase struct {
	Name          string
	KeyLength     int
	Salt          string
	EvenSEK       string
	OddSEK        string
	Passphrase    string
	KeyEncryption packet.PacketEncryption
	ExpectedWrap  string
}

var kmMarshalTests = []KMMarshalTestCase{
	{
		Name:          "Marshal_AES128_Even",
		KeyLength:     16,
		Salt:          "6c438852715a4d26e0e810b3132ca61f",
		EvenSEK:       "047dc22e7f000be55a25ba56ae2e9180",
		OddSEK:        "240c8e76ccf3637641af473edaf15aaf",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.EvenKeyEncrypted,
		ExpectedWrap:  "699ab4eac6b7c66c3a9fa0d6836326c2b294a10764233356",
	},
	{
		Name:          "Marshal_AES128_Odd",
		KeyLength:     16,
		Salt:          "6c438852715a4d26e0e810b3132ca61f",
		EvenSEK:       "047dc22e7f000be55a25ba56ae2e9180",
		OddSEK:        "240c8e76ccf3637641af473edaf15aaf",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.OddKeyEncrypted,
		ExpectedWrap:  "ca4decaaf8d7b5c38288e84c8796929c84b7c139f1f769d5",
	},
	{
		Name:          "Marshal_AES128_EvenOdd",
		KeyLength:     16,
		Salt:          "6c438852715a4d26e0e810b3132ca61f",
		EvenSEK:       "047dc22e7f000be55a25ba56ae2e9180",
		OddSEK:        "240c8e76ccf3637641af473edaf15aaf",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.EvenAndOddKey,
		ExpectedWrap:  "5b901889bd106609ca8a83264b12ed1bfab3f02812bad65784ac396b1f57eb16c53e1020d3a3250b",
	},
	{
		Name:          "Marshal_AES192_Even",
		KeyLength:     24,
		Salt:          "e636259ccc41e73611b9363bb58586b1",
		EvenSEK:       "4dca0ad088da64fdc8e98002d141bc46fed4fa0167b931c8",
		OddSEK:        "2b2bbb64ee3942cfa31bfe58efd1d2102c40b7bc028f8946",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.EvenKeyEncrypted,
		ExpectedWrap:  "8c6502d6a83e0ab894a43cb5b37b71c2755afc64a682bed9d46912138b60f384",
	},
	{
		Name:          "Marshal_AES256_Even",
		KeyLength:     32,
		Salt:          "3825bb4163f7d5cf2804ec0b31a7370f",
		EvenSEK:       "53a088d93431181075f8a9bc4876359afe48967308120c93f97bbd823d8de62a",
		OddSEK:        "7893e88b6296ffcc5a2eab5f53d48efd7adaeced8cb3a851d4f8e2dbda8db17a",
		Passphrase:    "foobarfoobar",
		KeyEncryption: packet.EvenKeyEncrypted,
		ExpectedWrap:  "7d1578458e41680dd997d1a185c75753f3344c6711542b35833f881f7c480304cbe9bdbe76035914",
	},
}

func TestCrypto_KMMarshal_Table(t *testing.T) {
	for _, tc := range kmMarshalTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()
			runKMMarshalTest(t, tc)
		})
	}
}

func runKMMarshalTest(t *testing.T, tc KMMarshalTestCase) {
	t.Helper()

	c, err := New(tc.KeyLength)
	require.NoError(t, err)

	// Access internal crypto struct to set keys
	cr := c.(*crypto)
	cr.salt = mustDecodeString(tc.Salt)
	cr.evenSEK = mustDecodeString(tc.EvenSEK)
	cr.oddSEK = mustDecodeString(tc.OddSEK)

	km := &packet.CIFKeyMaterialExtension{}
	err = c.MarshalKM(km, tc.Passphrase, tc.KeyEncryption)
	require.NoError(t, err)

	expectedWrap := mustDecodeString(tc.ExpectedWrap)
	require.Equal(t, expectedWrap, km.Wrap, "Wrap mismatch for %s", tc.Name)
	t.Logf("✅ %s: marshaling produced expected wrap", tc.Name)
}

// ═══════════════════════════════════════════════════════════════════════════
// Passphrase Validation Tests
// ═══════════════════════════════════════════════════════════════════════════

type PassphraseTestCase struct {
	Name        string
	Passphrase  string
	KeyLength   int
	Salt        string
	Wrap        string
	ExpectError bool
}

var passphraseTests = []PassphraseTestCase{
	{
		Name:        "Valid_MinLength",
		Passphrase:  "1234567890", // 10 chars - minimum
		KeyLength:   16,
		Salt:        "6c438852715a4d26e0e810b3132ca61f",
		Wrap:        "699ab4eac6b7c66c3a9fa0d6836326c2b294a10764233356",
		ExpectError: true, // Wrong passphrase
	},
	{
		Name:        "Valid_MaxLength",
		Passphrase:  "12345678901234567890123456789012345678901234567890123456789012345678901234567890", // 80 chars - maximum
		KeyLength:   16,
		Salt:        "6c438852715a4d26e0e810b3132ca61f",
		Wrap:        "699ab4eac6b7c66c3a9fa0d6836326c2b294a10764233356",
		ExpectError: true, // Wrong passphrase
	},
	{
		Name:        "Valid_CorrectPassphrase",
		Passphrase:  "foobarfoobar",
		KeyLength:   16,
		Salt:        "6c438852715a4d26e0e810b3132ca61f",
		Wrap:        "699ab4eac6b7c66c3a9fa0d6836326c2b294a10764233356",
		ExpectError: false,
	},
}

// ═══════════════════════════════════════════════════════════════════════════
// Error Handling Tests - Validate error chain and error types
// ═══════════════════════════════════════════════════════════════════════════

func TestCrypto_ErrorChain_WrongPassphrase(t *testing.T) {
	// This test ensures that wrong passphrase errors are properly wrapped
	// with ErrDecryptionFailed for better error handling by callers
	c, err := New(16)
	require.NoError(t, err)

	km := &packet.CIFKeyMaterialExtension{}
	km.KeyBasedEncryption = packet.EvenKeyEncrypted
	km.Salt = mustDecodeString("6c438852715a4d26e0e810b3132ca61f")
	km.Wrap = mustDecodeString("699ab4eac6b7c66c3a9fa0d6836326c2b294a10764233356")

	err = c.UnmarshalKM(km, "wrong_passphrase")

	// Must be an error
	require.Error(t, err)

	// Must be our semantic error type
	require.ErrorIs(t, err, ErrDecryptionFailed,
		"Wrong passphrase should return ErrDecryptionFailed for semantic handling")

	// Error message should be informative
	require.Contains(t, err.Error(), "decryption failed",
		"Error message should indicate decryption failure")
	require.Contains(t, err.Error(), "keywrap",
		"Error message should preserve underlying keywrap error info")

	t.Logf("✅ Error chain correctly wraps: %v", err)
}

func TestCrypto_ErrorChain_InvalidKey(t *testing.T) {
	// Test that invalid key encryption type returns ErrInvalidKey
	c, err := New(16)
	require.NoError(t, err)

	km := &packet.CIFKeyMaterialExtension{}
	km.KeyBasedEncryption = packet.UnencryptedPacket
	km.Salt = mustDecodeString("6c438852715a4d26e0e810b3132ca61f")
	km.Wrap = mustDecodeString("699ab4eac6b7c66c3a9fa0d6836326c2b294a10764233356")

	err = c.UnmarshalKM(km, "foobarfoobar")

	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidKey,
		"Unencrypted packet should return ErrInvalidKey")
	t.Logf("✅ ErrInvalidKey correctly returned for unencrypted: %v", err)
}

func TestCrypto_ErrorChain_InvalidWrapLength(t *testing.T) {
	// Test that wrong wrap length returns ErrInvalidWrap
	c, err := New(16)
	require.NoError(t, err)

	km := &packet.CIFKeyMaterialExtension{}
	km.KeyBasedEncryption = packet.EvenKeyEncrypted
	km.Salt = mustDecodeString("6c438852715a4d26e0e810b3132ca61f")
	// Wrong wrap length - too short
	km.Wrap = mustDecodeString("699ab4eac6b7c66c")

	err = c.UnmarshalKM(km, "foobarfoobar")

	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidWrap,
		"Wrong wrap length should return ErrInvalidWrap")
	t.Logf("✅ ErrInvalidWrap correctly returned for bad length: %v", err)
}

// ═══════════════════════════════════════════════════════════════════════════
// GenerateSEK Tests - 0% coverage currently!
// ═══════════════════════════════════════════════════════════════════════════

type GenerateSEKTestCase struct {
	Name          string
	KeyEncryption packet.PacketEncryption
	ExpectError   bool
}

var generateSEKTests = []GenerateSEKTestCase{
	{
		Name:          "Generate_Even",
		KeyEncryption: packet.EvenKeyEncrypted,
		ExpectError:   false,
	},
	{
		Name:          "Generate_Odd",
		KeyEncryption: packet.OddKeyEncrypted,
		ExpectError:   false,
	},
	{
		Name:          "Generate_Invalid_Unencrypted",
		KeyEncryption: packet.UnencryptedPacket,
		ExpectError:   true,
	},
	{
		Name:          "Generate_Invalid_EvenAndOdd",
		KeyEncryption: packet.EvenAndOddKey,
		ExpectError:   true, // EvenAndOddKey is for KM, not GenerateSEK
	},
}

func TestCrypto_GenerateSEK_Table(t *testing.T) {
	for _, tc := range generateSEKTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			c, err := New(16)
			require.NoError(t, err)

			err = c.GenerateSEK(tc.KeyEncryption)

			if tc.ExpectError {
				require.Error(t, err, "Expected error for %s", tc.Name)
				t.Logf("✅ %s: rejected as expected", tc.Name)
			} else {
				require.NoError(t, err, "Expected success for %s", tc.Name)
				t.Logf("✅ %s: SEK generated successfully", tc.Name)
			}
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// EncryptOrDecryptPayload Tests - 82.4% coverage, missing edge cases
// ═══════════════════════════════════════════════════════════════════════════

type EncryptDecryptTestCase struct {
	Name          string
	KeyLength     int
	KeyEncryption packet.PacketEncryption
	DataSize      int
	ExpectError   bool
}

var encryptDecryptTests = []EncryptDecryptTestCase{
	// Valid cases
	{
		Name:          "Encrypt_Even_AES128_SmallPayload",
		KeyLength:     16,
		KeyEncryption: packet.EvenKeyEncrypted,
		DataSize:      100,
		ExpectError:   false,
	},
	{
		Name:          "Encrypt_Odd_AES128_SmallPayload",
		KeyLength:     16,
		KeyEncryption: packet.OddKeyEncrypted,
		DataSize:      100,
		ExpectError:   false,
	},
	{
		Name:          "Encrypt_Even_AES256_LargePayload",
		KeyLength:     32,
		KeyEncryption: packet.EvenKeyEncrypted,
		DataSize:      1316, // Max SRT payload
		ExpectError:   false,
	},
	{
		Name:          "Encrypt_Even_AES192",
		KeyLength:     24,
		KeyEncryption: packet.EvenKeyEncrypted,
		DataSize:      500,
		ExpectError:   false,
	},
	// Invalid cases
	{
		Name:          "Encrypt_Invalid_Unencrypted",
		KeyLength:     16,
		KeyEncryption: packet.UnencryptedPacket,
		DataSize:      100,
		ExpectError:   true,
	},
	{
		Name:          "Encrypt_Invalid_EvenAndOdd",
		KeyLength:     16,
		KeyEncryption: packet.EvenAndOddKey,
		DataSize:      100,
		ExpectError:   true,
	},
	// Corner cases
	{
		Name:          "Corner_EmptyPayload",
		KeyLength:     16,
		KeyEncryption: packet.EvenKeyEncrypted,
		DataSize:      0,
		ExpectError:   false, // Empty data should be valid
	},
	{
		Name:          "Corner_SingleByte",
		KeyLength:     16,
		KeyEncryption: packet.EvenKeyEncrypted,
		DataSize:      1,
		ExpectError:   false,
	},
	{
		Name:          "Corner_BlockBoundary_16",
		KeyLength:     16,
		KeyEncryption: packet.EvenKeyEncrypted,
		DataSize:      16, // AES block size
		ExpectError:   false,
	},
	{
		Name:          "Corner_BlockBoundary_17",
		KeyLength:     16,
		KeyEncryption: packet.EvenKeyEncrypted,
		DataSize:      17, // Just over AES block size
		ExpectError:   false,
	},
}

func TestCrypto_EncryptDecrypt_Table(t *testing.T) {
	for _, tc := range encryptDecryptTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			c, err := New(tc.KeyLength)
			require.NoError(t, err)

			// Create test data
			data := make([]byte, tc.DataSize)
			for i := range data {
				data[i] = byte(i % 256)
			}
			originalData := make([]byte, len(data))
			copy(originalData, data)

			// Encrypt
			err = c.EncryptOrDecryptPayload(data, tc.KeyEncryption, 12345)

			if tc.ExpectError {
				require.Error(t, err, "Expected error for %s", tc.Name)
				t.Logf("✅ %s: rejected as expected", tc.Name)
				return
			}

			require.NoError(t, err, "Expected success for %s", tc.Name)

			// Data should be different after encryption (unless empty)
			if tc.DataSize > 0 {
				require.NotEqual(t, originalData, data, "Data should be encrypted")
			}

			// Decrypt (same operation with CTR mode)
			err = c.EncryptOrDecryptPayload(data, tc.KeyEncryption, 12345)
			require.NoError(t, err)

			// Data should match original after decryption
			require.Equal(t, originalData, data, "Data should match after decrypt")
			t.Logf("✅ %s: encrypt/decrypt round-trip successful", tc.Name)
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// Round-Trip Tests - Verify Marshal/Unmarshal works together
// ═══════════════════════════════════════════════════════════════════════════

func TestCrypto_MarshalUnmarshal_RoundTrip(t *testing.T) {
	testCases := []struct {
		KeyLength     int
		KeyEncryption packet.PacketEncryption
	}{
		{16, packet.EvenKeyEncrypted},
		{16, packet.OddKeyEncrypted},
		{16, packet.EvenAndOddKey},
		{24, packet.EvenKeyEncrypted},
		{32, packet.EvenKeyEncrypted},
	}

	for _, tc := range testCases {
		tc := tc
		name := fmt.Sprintf("AES%d_%v", tc.KeyLength*8, tc.KeyEncryption)
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			passphrase := "test-passphrase-1234"

			// Create crypto and marshal
			c1, err := New(tc.KeyLength)
			require.NoError(t, err)

			km := &packet.CIFKeyMaterialExtension{}
			err = c1.MarshalKM(km, passphrase, tc.KeyEncryption)
			require.NoError(t, err)

			// Create new crypto and unmarshal
			c2, err := New(tc.KeyLength)
			require.NoError(t, err)

			err = c2.UnmarshalKM(km, passphrase)
			require.NoError(t, err)

			// Test encryption/decryption works between both
			originalData := []byte("Hello, SRT encryption test!")
			testData := make([]byte, len(originalData))
			copy(testData, originalData)

			// Determine which key to use for encrypt/decrypt
			keyToUse := tc.KeyEncryption
			if keyToUse == packet.EvenAndOddKey {
				keyToUse = packet.EvenKeyEncrypted
			}

			// Encrypt with c1
			err = c1.EncryptOrDecryptPayload(testData, keyToUse, 999)
			require.NoError(t, err)

			// Decrypt with c2 (should work if keys were properly transferred)
			err = c2.EncryptOrDecryptPayload(testData, keyToUse, 999)
			require.NoError(t, err)

			require.Equal(t, originalData, testData, "Round-trip failed")
			t.Logf("✅ %s: Marshal/Unmarshal round-trip successful", name)
		})
	}
}

// ═══════════════════════════════════════════════════════════════════════════
// MarshalKM Error Tests - 91.4% coverage
// ═══════════════════════════════════════════════════════════════════════════

func TestCrypto_MarshalKM_Errors(t *testing.T) {
	c, err := New(16)
	require.NoError(t, err)

	testCases := []struct {
		Name          string
		KeyEncryption packet.PacketEncryption
		ExpectError   bool
	}{
		{"Invalid_Unencrypted", packet.UnencryptedPacket, true},
		{"Valid_Even", packet.EvenKeyEncrypted, false},
		{"Valid_Odd", packet.OddKeyEncrypted, false},
		{"Valid_EvenAndOdd", packet.EvenAndOddKey, false},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			km := &packet.CIFKeyMaterialExtension{}
			err := c.MarshalKM(km, "passphrase123", tc.KeyEncryption)

			if tc.ExpectError {
				require.Error(t, err)
				require.ErrorIs(t, err, ErrInvalidKey)
				t.Logf("✅ %s: rejected as expected", tc.Name)
			} else {
				require.NoError(t, err)
				t.Logf("✅ %s: marshaled successfully", tc.Name)
			}
		})
	}
}

func TestCrypto_Passphrase_Table(t *testing.T) {
	for _, tc := range passphraseTests {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			t.Parallel()

			c, err := New(tc.KeyLength)
			require.NoError(t, err)

			km := &packet.CIFKeyMaterialExtension{}
			km.KeyBasedEncryption = packet.EvenKeyEncrypted
			km.Salt = mustDecodeString(tc.Salt)
			km.Wrap = mustDecodeString(tc.Wrap)

			err = c.UnmarshalKM(km, tc.Passphrase)

			if tc.ExpectError {
				require.Error(t, err)
				t.Logf("✅ %s: passphrase rejected as expected", tc.Name)
			} else {
				require.NoError(t, err)
				t.Logf("✅ %s: passphrase accepted", tc.Name)
			}
		})
	}
}

