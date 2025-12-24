package packet

import (
	"bytes"
	"encoding/hex"
	"net"
	"sync"
	"testing"

	"github.com/datarhei/gosrt/circular"
	srtnet "github.com/datarhei/gosrt/net"

	"github.com/stretchr/testify/require"
)

func TestEmptyPacket(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	p := NewPacket(addr)

	var buf bytes.Buffer

	p.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "00000000c00000010000000000000000", data)
}

func TestArbitraryPacket(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	p := NewPacket(addr)
	p.SetData([]byte("hello world!"))

	var buf bytes.Buffer

	p.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "00000000c0000001000000000000000068656c6c6f20776f726c6421", data)
}

func TestArbitraryControlPacket(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	p := NewPacket(addr)
	p.Header().IsControlPacket = true
	p.Header().ControlType = CTRLTYPE_KEEPALIVE
	p.Header().SubType = 112
	p.Header().TypeSpecific = 42

	var buf bytes.Buffer

	p.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "800100700000002a0000000000000000", data)
}

func FuzzPacket(f *testing.F) {
	f.Add("00000000c00000010000000000000000")
	f.Add("00000000c0000001000000000000000068656c6c6f20776f726c6421")
	f.Add("800100700000002a0000000000000000")

	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	f.Fuzz(func(t *testing.T, orig string) {
		data, err := hex.DecodeString(orig)
		if err != nil {
			return
		}
		if len(data) == 0 {
			return
		}
		p, err := NewPacketFromData(addr, data)
		if err != nil {
			return
		}

		var buf bytes.Buffer
		buf.Reset()
		p.Marshal(&buf)

		if !bytes.Equal(data, buf.Bytes()) {
			t.Errorf("Before: %q, after: %q\n%s", orig, hex.EncodeToString(buf.Bytes()), p.Dump())
		}
	})
}

func TestUnmarshalPacket(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	data, _ := hex.DecodeString("00000000c0000001000000000000000068656c6c6f20776f726c6421")

	p, err := NewPacketFromData(addr, data)
	require.NoError(t, err)

	require.Equal(t, p.Header().Timestamp, uint32(0))
	require.Equal(t, p.Header().Addr.String(), "127.0.0.1:6000")
	require.False(t, p.Header().IsControlPacket)
	require.Equal(t, p.Header().PacketPositionFlag, SinglePacket)
	require.Equal(t, p.Header().KeyBaseEncryptionFlag, UnencryptedPacket)
	require.Equal(t, p.Header().MessageNumber, uint32(1))

	require.Equal(t, uint64(12), p.Len())
	require.Equal(t, "hello world!", string(p.Data()))
}

func TestPacketString(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	p := NewPacket(addr)
	p.SetData([]byte("hello world!"))

	require.Greater(t, len(p.String()), 0)
}

func TestHandshakeV4(t *testing.T) {
	ip := srtnet.IP{}
	ip.Parse("127.0.0.1")

	cif := &CIFHandshake{
		IsRequest:                   false,
		Version:                     4,
		EncryptionField:             0,
		ExtensionField:              2,
		InitialPacketSequenceNumber: circular.New(42, MAX_SEQUENCENUMBER),
		MaxTransmissionUnitSize:     1500,
		MaxFlowWindowSize:           100,
		HandshakeType:               HSTYPE_CONCLUSION,
		SRTSocketId:                 0x274921,
		SynCookie:                   0x123456,
		PeerIP:                      ip,
		HasHS:                       false,
		HasKM:                       false,
		HasSID:                      false,
		HasCongestionCtl:            false,
	}

	var buf bytes.Buffer

	cif.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "00000004000000020000002a000005dc00000064ffffffff00274921001234560100007f000000000000000000000000", data)

	cif2 := &CIFHandshake{}

	err := cif2.Unmarshal(buf.Bytes())

	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

func TestHandshakeV5(t *testing.T) {
	ip := srtnet.IP{}
	ip.Parse("127.0.0.1")

	cif := &CIFHandshake{
		IsRequest:                   false,
		Version:                     5,
		EncryptionField:             0,
		ExtensionField:              0,
		InitialPacketSequenceNumber: circular.New(42, MAX_SEQUENCENUMBER),
		MaxTransmissionUnitSize:     1500,
		MaxFlowWindowSize:           100,
		HandshakeType:               HSTYPE_CONCLUSION,
		SRTSocketId:                 0x274921,
		SynCookie:                   0x123456,
		PeerIP:                      ip,
		HasHS:                       true,
		HasKM:                       true,
		HasSID:                      true,
		HasCongestionCtl:            true,
		SRTHS: &CIFHandshakeExtension{
			SRTVersion: 0x010402,
			SRTFlags: CIFHandshakeExtensionFlags{
				TSBPDSND:      true,
				TSBPDRCV:      true,
				CRYPT:         true,
				TLPKTDROP:     true,
				PERIODICNAK:   true,
				REXMITFLG:     true,
				STREAM:        false,
				PACKET_FILTER: false,
			},
			RecvTSBPDDelay: 100,
			SendTSBPDDelay: 100,
		},
		SRTKM: &CIFKeyMaterialExtension{
			S:                     0,
			Version:               1,
			PacketType:            2,
			Sign:                  0x2029,
			Resv1:                 0,
			KeyBasedEncryption:    EvenKeyEncrypted,
			KeyEncryptionKeyIndex: 0,
			Cipher:                2,
			Authentication:        0,
			StreamEncapsulation:   2,
			Resv2:                 0,
			Resv3:                 0,
			SLen:                  16,
			KLen:                  16,
			Salt:                  []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
			Wrap:                  []byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20},
		},
		StreamId:      "/live/stream.foobar",
		CongestionCtl: "foob",
	}

	var buf bytes.Buffer

	cif.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "00000005000200070000002a000005dc00000064ffffffff00274921001234560100007f00000000000000000000000000020003000104020000003f006400640004000e122029010000000002000200000004040102030405060708090a0b0c0d0e0f10f0f1f2f3f4f5f6f71112131415161718191a1b1c1d1e1f200005000576696c2f74732f656d6165726f6f662e0072616200060001626f6f66", data)

	cif2 := &CIFHandshake{}

	err := cif2.Unmarshal(buf.Bytes())

	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

func TestHandshakeString(t *testing.T) {
	ip := srtnet.IP{}
	ip.Parse("127.0.0.1")

	cif := &CIFHandshake{
		IsRequest:                   false,
		Version:                     5,
		EncryptionField:             0,
		ExtensionField:              0,
		InitialPacketSequenceNumber: circular.New(42, MAX_SEQUENCENUMBER),
		MaxTransmissionUnitSize:     1500,
		MaxFlowWindowSize:           100,
		HandshakeType:               HSTYPE_CONCLUSION,
		SRTSocketId:                 0x274921,
		SynCookie:                   0x123456,
		PeerIP:                      ip,
		HasHS:                       true,
		HasKM:                       false,
		HasSID:                      true,
		HasCongestionCtl:            false,
		SRTHS: &CIFHandshakeExtension{
			SRTVersion: 0x010402,
			SRTFlags: CIFHandshakeExtensionFlags{
				TSBPDSND:      true,
				TSBPDRCV:      true,
				CRYPT:         true,
				TLPKTDROP:     true,
				PERIODICNAK:   true,
				REXMITFLG:     true,
				STREAM:        false,
				PACKET_FILTER: false,
			},
			RecvTSBPDDelay: 100,
			SendTSBPDDelay: 100,
		},
		SRTKM:    nil,
		StreamId: "/live/stream.foobar",
	}

	require.Greater(t, len(cif.String()), 0)
}

func TestKM(t *testing.T) {
	cif := &CIFKeyMaterialExtension{
		S:                     0,
		Version:               1,
		PacketType:            2,
		Sign:                  0x2029,
		Resv1:                 0,
		KeyBasedEncryption:    EvenKeyEncrypted,
		KeyEncryptionKeyIndex: 0,
		Cipher:                2,
		Authentication:        0,
		StreamEncapsulation:   2,
		Resv2:                 0,
		Resv3:                 0,
		SLen:                  16,
		KLen:                  16,
		Salt:                  []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		Wrap:                  []byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20},
	}

	var buf bytes.Buffer

	cif.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "122029010000000002000200000004040102030405060708090a0b0c0d0e0f10f0f1f2f3f4f5f6f71112131415161718191a1b1c1d1e1f20", data)

	cif2 := &CIFKeyMaterialExtension{}

	err := cif2.Unmarshal(buf.Bytes())

	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

func TestKMString(t *testing.T) {
	cif := &CIFKeyMaterialExtension{
		S:                     0,
		Version:               1,
		PacketType:            2,
		Sign:                  0x2029,
		Resv1:                 0,
		KeyBasedEncryption:    EvenKeyEncrypted,
		KeyEncryptionKeyIndex: 0,
		Cipher:                2,
		Authentication:        0,
		StreamEncapsulation:   2,
		Resv2:                 0,
		Resv3:                 0,
		SLen:                  16,
		KLen:                  16,
		Salt:                  []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		Wrap:                  []byte{0xf0, 0xf1, 0xf2, 0xf3, 0xf4, 0xf5, 0xf6, 0xf7, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20},
	}

	require.Greater(t, len(cif.String()), 0)
}

func TestFullACK(t *testing.T) {
	cif := &CIFACK{
		IsLite:                      false,
		IsSmall:                     false,
		LastACKPacketSequenceNumber: circular.New(42, MAX_SEQUENCENUMBER),
		RTT:                         38473,
		RTTVar:                      9084,
		AvailableBufferSize:         48533,
		PacketsReceivingRate:        20,
		EstimatedLinkCapacity:       0,
		ReceivingRate:               73637,
	}

	var buf bytes.Buffer

	cif.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "0000002a000096490000237c0000bd95000000140000000000011fa5", data)

	cif2 := &CIFACK{}

	err := cif2.Unmarshal(buf.Bytes())

	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

func TestFullACKString(t *testing.T) {
	cif := &CIFACK{
		IsLite:                      false,
		IsSmall:                     false,
		LastACKPacketSequenceNumber: circular.New(42, MAX_SEQUENCENUMBER),
		RTT:                         38473,
		RTTVar:                      9084,
		AvailableBufferSize:         48533,
		PacketsReceivingRate:        20,
		EstimatedLinkCapacity:       0,
		ReceivingRate:               73637,
	}

	require.Greater(t, len(cif.String()), 0)
}

func TestSmallACK(t *testing.T) {
	cif := &CIFACK{
		IsLite:                      false,
		IsSmall:                     true,
		LastACKPacketSequenceNumber: circular.New(42, MAX_SEQUENCENUMBER),
		RTT:                         38473,
		RTTVar:                      9084,
		AvailableBufferSize:         48533,
		PacketsReceivingRate:        0,
		EstimatedLinkCapacity:       0,
		ReceivingRate:               0,
	}

	var buf bytes.Buffer

	cif.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "0000002a000096490000237c0000bd95", data)

	cif2 := &CIFACK{}

	err := cif2.Unmarshal(buf.Bytes())

	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

func TestSmallACKString(t *testing.T) {
	cif := &CIFACK{
		IsLite:                      false,
		IsSmall:                     true,
		LastACKPacketSequenceNumber: circular.New(42, MAX_SEQUENCENUMBER),
		RTT:                         38473,
		RTTVar:                      9084,
		AvailableBufferSize:         48533,
		PacketsReceivingRate:        0,
		EstimatedLinkCapacity:       0,
		ReceivingRate:               0,
	}

	require.Greater(t, len(cif.String()), 0)
}

func TestLiteACK(t *testing.T) {
	cif := &CIFACK{
		IsLite:                      true,
		IsSmall:                     false,
		LastACKPacketSequenceNumber: circular.New(42, MAX_SEQUENCENUMBER),
		RTT:                         0,
		RTTVar:                      0,
		AvailableBufferSize:         0,
		PacketsReceivingRate:        0,
		EstimatedLinkCapacity:       0,
		ReceivingRate:               0,
	}

	var buf bytes.Buffer

	cif.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "0000002a", data)

	cif2 := &CIFACK{}

	err := cif2.Unmarshal(buf.Bytes())

	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

func TestLiteACKString(t *testing.T) {
	cif := &CIFACK{
		IsLite:                      true,
		IsSmall:                     false,
		LastACKPacketSequenceNumber: circular.New(42, MAX_SEQUENCENUMBER),
		RTT:                         0,
		RTTVar:                      0,
		AvailableBufferSize:         0,
		PacketsReceivingRate:        0,
		EstimatedLinkCapacity:       0,
		ReceivingRate:               0,
	}

	require.Greater(t, len(cif.String()), 0)
}

// TestFullACKPacketRoundTrip tests the full packet round-trip for ACK packets
// This verifies that a packet with header + CIF can be marshalled and unmarshalled correctly
func TestFullACKPacketRoundTrip(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	// Create ACK packet as it would be created in sendACK()
	p := NewPacket(addr)
	p.Header().IsControlPacket = true
	p.Header().ControlType = CTRLTYPE_ACK
	p.Header().Timestamp = 12345678
	p.Header().DestinationSocketId = 0x12345678
	p.Header().TypeSpecific = 42

	cif := &CIFACK{
		IsLite:                      false,
		IsSmall:                     false,
		LastACKPacketSequenceNumber: circular.New(100, MAX_SEQUENCENUMBER),
		RTT:                         38473,
		RTTVar:                      9084,
		AvailableBufferSize:         48533,
		PacketsReceivingRate:        20,
		EstimatedLinkCapacity:       0,
		ReceivingRate:               73637,
	}

	err := p.MarshalCIF(cif)
	require.NoError(t, err)

	// Marshal full packet
	var buf bytes.Buffer
	err = p.Marshal(&buf)
	require.NoError(t, err)

	// Unmarshal full packet
	p2, err := NewPacketFromData(addr, buf.Bytes())
	require.NoError(t, err)

	// Verify header
	require.True(t, p2.Header().IsControlPacket)
	require.Equal(t, CTRLTYPE_ACK, p2.Header().ControlType)
	require.Equal(t, uint32(12345678), p2.Header().Timestamp)
	require.Equal(t, uint32(0x12345678), p2.Header().DestinationSocketId)
	require.Equal(t, uint32(42), p2.Header().TypeSpecific)

	// Verify CIF
	cif2 := &CIFACK{}
	err = p2.UnmarshalCIF(cif2)
	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

// TestFullNAKPacketRoundTrip tests the full packet round-trip for NAK packets
// This verifies that a packet with header + CIF can be marshalled and unmarshalled correctly
func TestFullNAKPacketRoundTrip(t *testing.T) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	// Create NAK packet as it would be created in sendNAK()
	p := NewPacket(addr)
	p.Header().IsControlPacket = true
	p.Header().ControlType = CTRLTYPE_NAK
	p.Header().Timestamp = 87654321
	p.Header().DestinationSocketId = 0x87654321

	cif := &CIFNAK{
		LostPacketSequenceNumber: []circular.Number{
			circular.New(42, MAX_SEQUENCENUMBER),
			circular.New(42, MAX_SEQUENCENUMBER),
			circular.New(45, MAX_SEQUENCENUMBER),
			circular.New(49, MAX_SEQUENCENUMBER),
		},
	}

	err := p.MarshalCIF(cif)
	require.NoError(t, err)

	// Marshal full packet
	var buf bytes.Buffer
	err = p.Marshal(&buf)
	require.NoError(t, err)

	// Unmarshal full packet
	p2, err := NewPacketFromData(addr, buf.Bytes())
	require.NoError(t, err)

	// Verify header
	require.True(t, p2.Header().IsControlPacket)
	require.Equal(t, CTRLTYPE_NAK, p2.Header().ControlType)
	require.Equal(t, uint32(87654321), p2.Header().Timestamp)
	require.Equal(t, uint32(0x87654321), p2.Header().DestinationSocketId)

	// Verify CIF
	cif2 := &CIFNAK{}
	err = p2.UnmarshalCIF(cif2)
	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

func TestNAK(t *testing.T) {
	cif := &CIFNAK{
		LostPacketSequenceNumber: []circular.Number{
			circular.New(42, MAX_SEQUENCENUMBER),
			circular.New(42, MAX_SEQUENCENUMBER),
			circular.New(45, MAX_SEQUENCENUMBER),
			circular.New(49, MAX_SEQUENCENUMBER),
		},
	}

	var buf bytes.Buffer

	cif.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "0000002a8000002d00000031", data)

	cif2 := &CIFNAK{}

	err := cif2.Unmarshal(buf.Bytes())

	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

func TestNAKString(t *testing.T) {
	cif := &CIFNAK{
		LostPacketSequenceNumber: []circular.Number{
			circular.New(42, MAX_SEQUENCENUMBER),
			circular.New(42, MAX_SEQUENCENUMBER),
			circular.New(45, MAX_SEQUENCENUMBER),
			circular.New(49, MAX_SEQUENCENUMBER),
		},
	}

	require.Greater(t, len(cif.String()), 0)
}

func TestShutdown(t *testing.T) {
	cif := &CIFShutdown{}

	var buf bytes.Buffer

	cif.Marshal(&buf)

	data := hex.EncodeToString(buf.Bytes())

	require.Equal(t, "00000000", data)

	cif2 := &CIFShutdown{}

	err := cif2.Unmarshal(buf.Bytes())

	require.NoError(t, err)
	require.Equal(t, cif, cif2)
}

func TestShutdownString(t *testing.T) {
	cif := &CIFShutdown{}

	require.Greater(t, len(cif.String()), 0)
}

func TestPacketPoolReuse(t *testing.T) {
	// Verify packets are reused from pool
	addr := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1234}

	p1 := NewPacket(addr)
	p1.Header().DestinationSocketId = 12345
	p1.Header().Timestamp = 99999

	// Store pointer to verify reuse
	p1Ptr := p1.(*pkt)

	p1.Decommission() // Resets fields and returns to pool

	p2 := NewPacket(addr)
	// Verify p2 is the same underlying struct (pointer comparison)
	require.Equal(t, p1Ptr, p2.(*pkt), "packet should be reused from pool")
	// Verify fields are properly reset (should be 0/defaults)
	require.Equal(t, uint32(0), p2.Header().DestinationSocketId, "DestinationSocketId should be reset")
	require.Equal(t, uint32(0), p2.Header().Timestamp, "Timestamp should be reset")
	require.Equal(t, addr, p2.Header().Addr, "Addr should be set")
}

func TestPacketPoolResetInDecommission(t *testing.T) {
	// Verify all fields are reset in Decommission() before Put()
	addr := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1234}

	p := NewPacket(addr)
	// Set various fields
	p.Header().IsControlPacket = true
	p.Header().ControlType = CTRLTYPE_ACK
	p.Header().PktTsbpdTime = 123456
	p.Header().DestinationSocketId = 999
	p.Header().Timestamp = 888
	p.Header().TypeSpecific = 777

	p.Decommission() // Should reset all fields

	// Get again from pool
	p2 := NewPacket(addr)
	require.False(t, p2.Header().IsControlPacket, "IsControlPacket should be reset")
	require.Equal(t, CtrlType(0), p2.Header().ControlType, "ControlType should be reset")
	require.Equal(t, uint64(0), p2.Header().PktTsbpdTime, "PktTsbpdTime should be reset")
	require.Equal(t, uint32(0), p2.Header().DestinationSocketId, "DestinationSocketId should be reset")
	require.Equal(t, uint32(0), p2.Header().Timestamp, "Timestamp should be reset")
	require.Equal(t, uint32(0), p2.Header().TypeSpecific, "TypeSpecific should be reset")
	require.Equal(t, addr, p2.Header().Addr, "Addr should be set")
}

func TestPacketPoolWithUnmarshal(t *testing.T) {
	// Verify that Unmarshal() works correctly with pooled packets
	addr := &net.UDPAddr{IP: net.IPv4(1, 2, 3, 4), Port: 1234}
	data, _ := hex.DecodeString("00000000c0000001000000000000000068656c6c6f20776f726c6421")

	// Create and decommission a packet to populate pool
	p1 := NewPacket(addr)
	p1.Decommission()

	// Create new packet from data (should use pooled packet)
	p2, err := NewPacketFromData(addr, data)
	require.NoError(t, err)
	require.Equal(t, "hello world!", string(p2.Data()))
	require.Equal(t, addr, p2.Header().Addr)
}

func BenchmarkNewPacket(b *testing.B) {
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	for i := 0; i < b.N; i++ {
		pkt := NewPacket(addr)

		pkt.Decommission()
	}
}

func BenchmarkNewPacketWithData(b *testing.B) {
	data := make([]byte, 1316)
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	p := NewPacket(addr)
	p.SetData(data)

	var buf bytes.Buffer

	p.Marshal(&buf)

	data = buf.Bytes()

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pkt, _ := NewPacketFromData(addr, data)

		if pkt != nil {
			pkt.Decommission()
		}
	}
}

func BenchmarkNoBufferpool(b *testing.B) {
	data := make([]byte, 1316)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		pdata := make([]byte, len(data)-16)
		copy(pdata, data[16:])
	}
}

func BenchmarkBufferpool(b *testing.B) {
	pool := sync.Pool{
		New: func() interface{} {
			return new(bytes.Buffer)
		},
	}

	data := make([]byte, 1316)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		p := pool.Get().(*bytes.Buffer)

		p.Reset()
		p.Write(data[16:])

		pool.Put(p)
	}
}

// ========== Zero-Copy Tests (Phase 2: Lockless Design) ==========

const (
	// MPEG-TS packet size (ISO/IEC 13818-1 standard)
	MpegTsPacketSize = 188

	// Number of MPEG-TS packets typically packed into one SRT payload
	MpegTsPacketsPerPayload = 7

	// Realistic payload size: 188 * 7 = 1316 bytes
	RealisticPayloadSize = MpegTsPacketSize * MpegTsPacketsPerPayload
)

// createTestDataPacket creates a valid data packet buffer with the given sequence and payload size
func createTestDataPacket(seq uint32, payloadSize int) []byte {
	buf := make([]byte, HeaderSize+payloadSize)
	// Data packet: bit 15 = 0
	buf[0] = byte(seq >> 24)
	buf[1] = byte(seq >> 16)
	buf[2] = byte(seq >> 8)
	buf[3] = byte(seq)
	// Position flag, order flag, encryption, retransmit, message number
	buf[4] = 0xC0 // Single packet, no order, no encryption
	buf[5] = 0x00
	buf[6] = 0x00
	buf[7] = 0x01 // Message number = 1
	// Timestamp
	buf[8] = 0x00
	buf[9] = 0x01
	buf[10] = 0x00
	buf[11] = 0x00
	// Socket ID
	buf[12] = 0xAB
	buf[13] = 0xCD
	buf[14] = 0x12
	buf[15] = 0x34
	// Fill payload with pattern
	for i := 0; i < payloadSize; i++ {
		buf[HeaderSize+i] = byte(i % 256)
	}
	return buf
}

// createTestControlPacket creates a valid control packet buffer
func createTestControlPacket(ctrlType CtrlType) []byte {
	buf := make([]byte, HeaderSize)
	// Control packet: bit 15 = 1
	typeVal := uint16(ctrlType) | 0x8000
	buf[0] = byte(typeVal >> 8)
	buf[1] = byte(typeVal)
	buf[2] = 0x00 // SubType high
	buf[3] = 0x00 // SubType low
	buf[4] = 0x00 // TypeSpecific
	buf[5] = 0x00
	buf[6] = 0x00
	buf[7] = 0x00
	// Timestamp
	buf[8] = 0x00
	buf[9] = 0x02
	buf[10] = 0x00
	buf[11] = 0x00
	// Socket ID
	buf[12] = 0xFE
	buf[13] = 0xDC
	buf[14] = 0xBA
	buf[15] = 0x98
	return buf
}

func TestUnmarshalZeroCopy(t *testing.T) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	t.Run("successful data packet", func(t *testing.T) {
		buf := createTestDataPacket(12345, 100)
		bufPtr := &buf
		n := len(buf)

		p := NewPacket(nil).(*pkt)
		err := p.UnmarshalZeroCopy(bufPtr, n, testAddr)
		require.NoError(t, err)

		// Verify header parsed correctly
		require.False(t, p.Header().IsControlPacket)
		require.Equal(t, uint32(12345), p.Header().PacketSequenceNumber.Val())
		require.Equal(t, uint32(0x00010000), p.Header().Timestamp)
		require.Equal(t, uint32(0xABCD1234), p.Header().DestinationSocketId)

		// Verify buffer tracking
		require.True(t, p.HasRecvBuffer())
		require.Equal(t, bufPtr, p.GetRecvBuffer())

		// Verify payload access via Data()
		payload := p.Data()
		require.Len(t, payload, 100)
		// Check payload pattern
		for i := 0; i < 100; i++ {
			require.Equal(t, byte(i%256), payload[i], "payload byte %d mismatch", i)
		}

		// Verify Len()
		require.Equal(t, uint64(100), p.Len())

		p.Decommission()
	})

	t.Run("successful control packet", func(t *testing.T) {
		buf := createTestControlPacket(CTRLTYPE_ACK)
		bufPtr := &buf

		p := NewPacket(nil).(*pkt)
		err := p.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)
		require.NoError(t, err)

		require.True(t, p.Header().IsControlPacket)
		require.Equal(t, CTRLTYPE_ACK, p.Header().ControlType)
		require.Equal(t, uint32(0x00020000), p.Header().Timestamp)
		require.Equal(t, uint32(0xFEDCBA98), p.Header().DestinationSocketId)

		p.Decommission()
	})

	t.Run("packet too short returns error", func(t *testing.T) {
		buf := make([]byte, HeaderSize-1) // One byte too short
		bufPtr := &buf

		p := NewPacket(nil).(*pkt)
		err := p.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)
		require.Error(t, err)
		require.Contains(t, err.Error(), "too short")

		// CRITICAL: Buffer must still be tracked even on error!
		// This allows DecommissionWithBuffer to clean up properly
		require.True(t, p.HasRecvBuffer())
		require.Equal(t, bufPtr, p.GetRecvBuffer())

		p.Decommission()
	})

	t.Run("n field stored correctly", func(t *testing.T) {
		buf := make([]byte, 1500)
		// Create valid header
		copy(buf, createTestDataPacket(1, 0)[:HeaderSize])
		bufPtr := &buf

		p := NewPacket(nil).(*pkt)
		n := 200 // Only use first 200 bytes
		err := p.UnmarshalZeroCopy(bufPtr, n, testAddr)
		require.NoError(t, err)

		// Data() should only return up to n, not full buffer
		payload := p.Data()
		require.Len(t, payload, n-HeaderSize) // 200-16 = 184

		p.Decommission()
	})
}

func TestDecommissionWithBuffer(t *testing.T) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	t.Run("returns buffer to pool", func(t *testing.T) {
		pool := &sync.Pool{New: func() interface{} { b := make([]byte, 1500); return &b }}

		bufPtr := pool.Get().(*[]byte)
		copy(*bufPtr, createTestDataPacket(1, 100))

		p := NewPacket(nil).(*pkt)
		p.UnmarshalZeroCopy(bufPtr, HeaderSize+100, testAddr)
		require.True(t, p.HasRecvBuffer())

		p.DecommissionWithBuffer(pool)

		// Buffer reference should be cleared (packet is now in pool, can't check directly)
		// But we can verify no panic occurred
	})

	t.Run("handles nil buffer gracefully", func(t *testing.T) {
		pool := &sync.Pool{New: func() interface{} { b := make([]byte, 1500); return &b }}

		p := NewPacket(nil).(*pkt) // No buffer set
		require.False(t, p.HasRecvBuffer())

		// Should not panic
		p.DecommissionWithBuffer(pool)
	})

	t.Run("handles nil pool gracefully", func(t *testing.T) {
		buf := createTestDataPacket(1, 100)
		bufPtr := &buf

		p := NewPacket(nil).(*pkt)
		p.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)

		// Should not panic with nil pool
		p.DecommissionWithBuffer(nil)
	})

	t.Run("buffer length preserved after pool return", func(t *testing.T) {
		// This test catches the bug where DecommissionWithBuffer zeroed the slice length
		// before returning to pool, causing panics in io_uring path when accessing buffer[0].
		//
		// The bug was: *p.recvBuffer = (*p.recvBuffer)[:0]
		//
		// With the bug, subsequent Get() returns a zero-length slice, causing:
		//   panic: runtime error: index out of range [0] with length 0
		// when io_uring tries to set iovec.Base = &buffer[0]

		const bufferSize = 1500
		pool := &sync.Pool{New: func() interface{} { b := make([]byte, bufferSize); return &b }}

		// Get buffer, simulate zero-copy receive, then return to pool
		bufPtr := pool.Get().(*[]byte)
		require.Equal(t, bufferSize, len(*bufPtr), "initial buffer should have full length")

		copy(*bufPtr, createTestDataPacket(1, 100))
		p := NewPacket(nil).(*pkt)
		err := p.UnmarshalZeroCopy(bufPtr, HeaderSize+100, testAddr)
		require.NoError(t, err)

		// Return buffer to pool via DecommissionWithBuffer
		p.DecommissionWithBuffer(pool)

		// Get buffer again from pool (should be the same one we just returned)
		bufPtr2 := pool.Get().(*[]byte)

		// CRITICAL: Buffer must still have full length, not zero!
		// The io_uring path does: iovec.Base = &buffer[0]
		// If buffer has length 0, this panics with "index out of range [0] with length 0"
		require.Equal(t, bufferSize, len(*bufPtr2),
			"buffer returned from pool must have full length (not zeroed); "+
				"io_uring requires buffer[0] access for iovec setup")

		// Also verify we can actually access buffer[0] without panic
		require.NotPanics(t, func() {
			_ = (*bufPtr2)[0]
		}, "must be able to access buffer[0] without panic")
	})
}

func TestDataZeroCopy(t *testing.T) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	t.Run("zero-copy path computes slice correctly", func(t *testing.T) {
		payloadData := []byte("test payload data here")
		buf := make([]byte, HeaderSize+len(payloadData))
		copy(buf[:HeaderSize], createTestDataPacket(1, 0)[:HeaderSize])
		copy(buf[HeaderSize:], payloadData)
		bufPtr := &buf

		p := NewPacket(nil).(*pkt)
		p.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)

		payload := p.Data()
		require.Equal(t, payloadData, payload)

		p.Decommission()
	})

	t.Run("returns nil for header-only packet", func(t *testing.T) {
		buf := createTestDataPacket(1, 0) // Exactly header, no payload
		bufPtr := &buf

		p := NewPacket(nil).(*pkt)
		p.UnmarshalZeroCopy(bufPtr, HeaderSize, testAddr)

		payload := p.Data()
		require.Empty(t, payload)

		p.Decommission()
	})

	t.Run("returns nil when recvBuffer is nil", func(t *testing.T) {
		p := NewPacket(nil).(*pkt)
		// Don't call UnmarshalZeroCopy - legacy path with nil payload
		p.payload = nil
		p.recvBuffer = nil

		require.Nil(t, p.Data())
	})

	t.Run("legacy path still works", func(t *testing.T) {
		testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")
		payloadData := []byte("legacy payload")

		p := NewPacket(testAddr)
		p.SetData(payloadData)

		payload := p.Data()
		require.Equal(t, payloadData, payload)

		p.Decommission()
	})
}

func TestLenZeroCopy(t *testing.T) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	t.Run("returns correct length for zero-copy", func(t *testing.T) {
		buf := createTestDataPacket(1, 184)
		bufPtr := &buf
		n := len(buf) // HeaderSize + 184 = 200

		p := NewPacket(nil).(*pkt)
		p.UnmarshalZeroCopy(bufPtr, n, testAddr)

		require.Equal(t, uint64(184), p.Len())

		p.Decommission()
	})

	t.Run("returns zero for header-only", func(t *testing.T) {
		buf := createTestDataPacket(1, 0)
		bufPtr := &buf

		p := NewPacket(nil).(*pkt)
		p.UnmarshalZeroCopy(bufPtr, HeaderSize, testAddr)

		require.Equal(t, uint64(0), p.Len())

		p.Decommission()
	})
}

// TestUnmarshalZeroCopyRoundTrip verifies that a packet can be marshaled,
// then unmarshaled with UnmarshalZeroCopy, and produce identical header values.
func TestUnmarshalZeroCopyRoundTrip(t *testing.T) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	testCases := []struct {
		name        string
		isControl   bool
		ctrlType    CtrlType
		seq         uint32
		timestamp   uint32
		socketID    uint32
		payloadSize int
	}{
		{"data packet small payload", false, 0, 12345, 1000000, 0xABCD1234, 100},
		{"data packet large payload", false, 0, 99999, 5000000, 0x12345678, 1400},
		{"data packet min payload", false, 0, 1, 0, 0x1, 1},
		{"ACK control packet", true, CTRLTYPE_ACK, 0, 2000000, 0xFACE0000, 0},
		{"NAK control packet", true, CTRLTYPE_NAK, 0, 3000000, 0xBEEF0000, 0},
		{"keepalive packet", true, CTRLTYPE_KEEPALIVE, 0, 4000000, 0xDEAD0000, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create original packet using legacy path
			original := NewPacket(testAddr)
			original.Header().IsControlPacket = tc.isControl
			original.Header().Timestamp = tc.timestamp
			original.Header().DestinationSocketId = tc.socketID

			if tc.isControl {
				original.Header().ControlType = tc.ctrlType
			} else {
				original.Header().PacketSequenceNumber = circular.New(tc.seq, MAX_SEQUENCENUMBER)
			}

			if tc.payloadSize > 0 {
				payload := make([]byte, tc.payloadSize)
				for i := range payload {
					payload[i] = byte(i % 256)
				}
				original.SetData(payload)
			}

			// Marshal to bytes
			var buf bytes.Buffer
			original.Marshal(&buf)
			marshaled := buf.Bytes()

			// Unmarshal with zero-copy
			bufCopy := make([]byte, len(marshaled))
			copy(bufCopy, marshaled)
			bufPtr := &bufCopy

			restored := NewPacket(nil).(*pkt)
			err := restored.UnmarshalZeroCopy(bufPtr, len(bufCopy), testAddr)
			require.NoError(t, err)

			// Verify all header fields match
			require.Equal(t, original.Header().IsControlPacket, restored.Header().IsControlPacket)
			require.Equal(t, original.Header().Timestamp, restored.Header().Timestamp)
			require.Equal(t, original.Header().DestinationSocketId, restored.Header().DestinationSocketId)

			if tc.isControl {
				require.Equal(t, original.Header().ControlType, restored.Header().ControlType)
			} else {
				require.Equal(t, original.Header().PacketSequenceNumber.Val(),
					restored.Header().PacketSequenceNumber.Val())
			}

			// Verify payload matches
			if tc.payloadSize > 0 {
				require.Equal(t, original.Data(), restored.Data())
			}

			original.Decommission()
			restored.Decommission()
		})
	}
}

// TestUnmarshalZeroCopyVsCopyEquivalence verifies that UnmarshalZeroCopy
// produces the same results as the legacy Unmarshal for all packet types.
func TestUnmarshalZeroCopyVsCopyEquivalence(t *testing.T) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	testCases := []struct {
		name        string
		isControl   bool
		payloadSize int
	}{
		{"data packet 100 bytes", false, 100},
		{"data packet 1400 bytes", false, 1400},
		{"control ACK packet", true, 0},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create a test packet buffer
			var buf []byte
			if tc.isControl {
				buf = createTestControlPacket(CTRLTYPE_ACK)
			} else {
				buf = createTestDataPacket(54321, tc.payloadSize)
			}

			// Unmarshal with legacy copy method
			pktCopy := NewPacket(testAddr)
			errCopy := pktCopy.Unmarshal(buf)
			require.NoError(t, errCopy)

			// Unmarshal with zero-copy method
			bufCopy := make([]byte, len(buf))
			copy(bufCopy, buf) // Make a copy to avoid interference
			bufPtr := &bufCopy
			pktZero := NewPacket(nil).(*pkt)
			errZero := pktZero.UnmarshalZeroCopy(bufPtr, len(bufCopy), testAddr)
			require.NoError(t, errZero)

			// Compare all header fields
			require.Equal(t, pktCopy.Header().IsControlPacket, pktZero.Header().IsControlPacket,
				"IsControlPacket mismatch")
			require.Equal(t, pktCopy.Header().Timestamp, pktZero.Header().Timestamp,
				"Timestamp mismatch")
			require.Equal(t, pktCopy.Header().DestinationSocketId, pktZero.Header().DestinationSocketId,
				"DestinationSocketId mismatch")

			if tc.isControl {
				require.Equal(t, pktCopy.Header().ControlType, pktZero.header.ControlType,
					"ControlType mismatch")
				require.Equal(t, pktCopy.Header().SubType, pktZero.header.SubType,
					"SubType mismatch")
				require.Equal(t, pktCopy.Header().TypeSpecific, pktZero.header.TypeSpecific,
					"TypeSpecific mismatch")
			} else {
				require.Equal(t, pktCopy.Header().PacketSequenceNumber.Val(),
					pktZero.header.PacketSequenceNumber.Val(),
					"PacketSequenceNumber mismatch")
				require.Equal(t, pktCopy.Header().PacketPositionFlag, pktZero.header.PacketPositionFlag,
					"PacketPositionFlag mismatch")
				require.Equal(t, pktCopy.Header().OrderFlag, pktZero.header.OrderFlag,
					"OrderFlag mismatch")
				require.Equal(t, pktCopy.Header().KeyBaseEncryptionFlag, pktZero.header.KeyBaseEncryptionFlag,
					"KeyBaseEncryptionFlag mismatch")
				require.Equal(t, pktCopy.Header().RetransmittedPacketFlag, pktZero.header.RetransmittedPacketFlag,
					"RetransmittedPacketFlag mismatch")
				require.Equal(t, pktCopy.Header().MessageNumber, pktZero.header.MessageNumber,
					"MessageNumber mismatch")
			}

			// Compare payloads (content should be identical)
			// Note: Empty payload can be either nil or []byte{}, so use bytes.Equal
			// which treats both as equal
			if len(pktCopy.Data()) == 0 && len(pktZero.Data()) == 0 {
				// Both empty - that's fine (nil vs []byte{} is equivalent)
			} else {
				require.Equal(t, pktCopy.Data(), pktZero.Data(),
					"Payload content mismatch")
			}
			require.Equal(t, pktCopy.Len(), pktZero.Len(),
				"Len mismatch")

			pktCopy.Decommission()
			pktZero.Decommission()
		})
	}
}

// ========== Zero-Copy Benchmarks ==========

// BenchmarkUnmarshalZeroCopy measures zero-copy unmarshal performance.
// Uses realistic 7x MPEG-TS payload (1316 bytes) to demonstrate real-world benefit.
func BenchmarkUnmarshalZeroCopy(b *testing.B) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")
	buf := createTestDataPacket(12345, RealisticPayloadSize)
	bufPtr := &buf

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		p := NewPacket(nil).(*pkt)
		_ = p.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)
		p.ClearRecvBuffer() // Don't return buffer in benchmark
		p.Decommission()
	}
}

// BenchmarkUnmarshalCopy measures legacy copying unmarshal for comparison.
func BenchmarkUnmarshalCopy(b *testing.B) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")
	buf := createTestDataPacket(12345, RealisticPayloadSize)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		p := NewPacket(testAddr)
		_ = p.Unmarshal(buf)
		p.Decommission()
	}
}

// BenchmarkUnmarshalComparison runs both methods with various payload sizes.
func BenchmarkUnmarshalComparison(b *testing.B) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")

	payloadSizes := []struct {
		name string
		size int
	}{
		{"1_MPEGTS", MpegTsPacketSize * 1},         // 188 bytes
		{"4_MPEGTS", MpegTsPacketSize * 4},         // 752 bytes
		{"7_MPEGTS_typical", MpegTsPacketSize * 7}, // 1316 bytes
		{"max_payload", 1400},                      // Near MTU limit
	}

	for _, tc := range payloadSizes {
		buf := createTestDataPacket(12345, tc.size)
		bufCopy := make([]byte, len(buf))
		copy(bufCopy, buf)
		bufPtr := &buf

		b.Run("ZeroCopy/"+tc.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				p := NewPacket(nil).(*pkt)
				_ = p.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)
				p.ClearRecvBuffer()
				p.Decommission()
			}
		})

		b.Run("Copy/"+tc.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				p := NewPacket(testAddr)
				_ = p.Unmarshal(bufCopy)
				p.Decommission()
			}
		})
	}
}

// BenchmarkDataAccess measures payload access overhead.
func BenchmarkDataAccess(b *testing.B) {
	testAddr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:6000")
	buf := createTestDataPacket(12345, RealisticPayloadSize)
	bufPtr := &buf

	p := NewPacket(nil).(*pkt)
	_ = p.UnmarshalZeroCopy(bufPtr, len(buf), testAddr)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		payload := p.Data()
		_ = payload // Prevent optimization
	}
}
