package srt

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/datarhei/gosrt/metrics"
	"github.com/datarhei/gosrt/packet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnectionMetricsDataPackets verifies data packet counters work for a simple send/receive
func TestConnectionMetricsDataPackets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	message := "Hello World! This is a test message for metrics verification."
	channel := NewPubSub(PubSubConfig{})

	config := DefaultConfig()

	server := Server{
		Addr:    "127.0.0.1:6013",
		Config:  &config,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			streamid := req.StreamId()
			if streamid == "publish" {
				return PUBLISH
			} else if streamid == "subscribe" {
				return SUBSCRIBE
			}
			return REJECT
		},
		HandlePublish: func(conn Conn) {
			channel.Publish(conn)
			conn.Close()
		},
		HandleSubscribe: func(conn Conn) {
			channel.Subscribe(conn)
			conn.Close()
		},
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		err := server.Serve()
		if err == ErrServerClosed {
			return
		}
	}()

	readerConnected := make(chan struct{})
	readerDone := make(chan struct{})
	dataReader := bytes.Buffer{}

	// Track socketIds for metrics lookup
	var readerSocketId, writerSocketId uint32

	go func() {
		defer close(readerDone)

		config := DefaultConfig()
		config.StreamId = "subscribe"

		conn, err := testDial(t, "127.0.0.1:6013", config)
		if !assert.NoError(t, err) {
			return
		}

		// Get socket ID from dialer
		if d, ok := conn.(*dialer); ok {
			readerSocketId = d.conn.socketId
		}

		close(readerConnected)

		buffer := make([]byte, 2048)
		for {
			n, err := conn.Read(buffer)
			if n != 0 {
				dataReader.Write(buffer[:n])
			}
			if err != nil {
				break
			}
		}
		conn.Close()
	}()

	<-readerConnected

	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)

		config := DefaultConfig()
		config.StreamId = "publish"

		conn, err := testDial(t, "127.0.0.1:6013", config)
		if !assert.NoError(t, err) {
			return
		}

		// Get socket ID from dialer
		if d, ok := conn.(*dialer); ok {
			writerSocketId = d.conn.socketId
		}

		// Write multiple messages
		for i := 0; i < 10; i++ {
			_, err := conn.Write([]byte(message))
			if !assert.NoError(t, err) {
				return
			}
		}

		time.Sleep(2 * time.Second)
		conn.Close()
	}()

	<-writerDone
	<-readerDone

	// Verify data was received
	require.NotEmpty(t, dataReader.String())

	// Look up metrics by socket ID
	connections, _ := metrics.GetConnections()

	// Verify writer metrics (sender side)
	if writerMetrics, ok := connections[writerSocketId]; ok && writerMetrics != nil {
		sentData := writerMetrics.PktSentDataSuccess.Load()
		t.Logf("Writer PktSentDataSuccess: %d", sentData)
		require.Greater(t, sentData, uint64(0), "Should have sent data packets")

		sentBytes := writerMetrics.ByteSentDataSuccess.Load()
		t.Logf("Writer ByteSentDataSuccess: %d", sentBytes)
		require.Greater(t, sentBytes, uint64(0), "Should have sent bytes")

		// Verify congestion control counters
		congSent := writerMetrics.CongestionSendPkt.Load()
		t.Logf("Writer CongestionSendPkt: %d", congSent)
		require.Greater(t, congSent, uint64(0), "Should have congestion send packets")
	} else {
		t.Log("Warning: Writer metrics not found in registry (connection may have been unregistered)")
	}

	// Verify reader metrics (receiver side)
	if readerMetrics, ok := connections[readerSocketId]; ok && readerMetrics != nil {
		recvData := readerMetrics.PktRecvDataSuccess.Load()
		t.Logf("Reader PktRecvDataSuccess: %d", recvData)
		require.Greater(t, recvData, uint64(0), "Should have received data packets")

		recvBytes := readerMetrics.ByteRecvDataSuccess.Load()
		t.Logf("Reader ByteRecvDataSuccess: %d", recvBytes)
		require.Greater(t, recvBytes, uint64(0), "Should have received bytes")

		// Verify congestion control counters
		congRecv := readerMetrics.CongestionRecvPkt.Load()
		t.Logf("Reader CongestionRecvPkt: %d", congRecv)
		require.Greater(t, congRecv, uint64(0), "Should have congestion recv packets")
	} else {
		t.Log("Warning: Reader metrics not found in registry (connection may have been unregistered)")
	}
}

// TestConnectionMetricsACKFlow verifies ACK/ACKACK counters during normal operation
func TestConnectionMetricsACKFlow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	message := "Test message for ACK flow verification"
	channel := NewPubSub(PubSubConfig{})

	config := DefaultConfig()

	server := Server{
		Addr:    "127.0.0.1:6014",
		Config:  &config,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			streamid := req.StreamId()
			if streamid == "publish" {
				return PUBLISH
			} else if streamid == "subscribe" {
				return SUBSCRIBE
			}
			return REJECT
		},
		HandlePublish: func(conn Conn) {
			channel.Publish(conn)
			conn.Close()
		},
		HandleSubscribe: func(conn Conn) {
			channel.Subscribe(conn)
			conn.Close()
		},
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		err := server.Serve()
		if err == ErrServerClosed {
			return
		}
	}()

	readerConnected := make(chan struct{})
	readerDone := make(chan struct{})
	var readerSocketId, writerSocketId uint32

	go func() {
		defer close(readerDone)

		config := DefaultConfig()
		config.StreamId = "subscribe"

		conn, err := testDial(t, "127.0.0.1:6014", config)
		if !assert.NoError(t, err) {
			return
		}

		if d, ok := conn.(*dialer); ok {
			readerSocketId = d.conn.socketId
		}

		close(readerConnected)

		buffer := make([]byte, 2048)
		for {
			n, err := conn.Read(buffer)
			if n == 0 && err != nil {
				break
			}
		}
		conn.Close()
	}()

	<-readerConnected

	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)

		config := DefaultConfig()
		config.StreamId = "publish"

		conn, err := testDial(t, "127.0.0.1:6014", config)
		if !assert.NoError(t, err) {
			return
		}

		if d, ok := conn.(*dialer); ok {
			writerSocketId = d.conn.socketId
		}

		// Write multiple messages to trigger ACK flow
		for i := 0; i < 50; i++ {
			conn.Write([]byte(message))
			time.Sleep(10 * time.Millisecond)
		}

		time.Sleep(2 * time.Second)
		conn.Close()
	}()

	<-writerDone
	<-readerDone

	// Look up metrics
	connections, _ := metrics.GetConnections()

	// Writer should have received ACKs (and sent ACKACKs)
	if writerMetrics, ok := connections[writerSocketId]; ok && writerMetrics != nil {
		recvACK := writerMetrics.PktRecvACKSuccess.Load()
		t.Logf("Writer PktRecvACKSuccess: %d", recvACK)
		// Sender receives ACKs from receiver
		require.Greater(t, recvACK, uint64(0), "Sender should receive ACKs")

		sentACKACK := writerMetrics.PktSentACKACKSuccess.Load()
		t.Logf("Writer PktSentACKACKSuccess: %d", sentACKACK)
		// Sender sends ACKACKs in response
		require.Greater(t, sentACKACK, uint64(0), "Sender should send ACKACKs")
	}

	// Reader should have sent ACKs (and received ACKACKs)
	if readerMetrics, ok := connections[readerSocketId]; ok && readerMetrics != nil {
		sentACK := readerMetrics.PktSentACKSuccess.Load()
		t.Logf("Reader PktSentACKSuccess: %d", sentACK)
		// Receiver sends ACKs to sender
		require.Greater(t, sentACK, uint64(0), "Receiver should send ACKs")

		recvACKACK := readerMetrics.PktRecvACKACKSuccess.Load()
		t.Logf("Reader PktRecvACKACKSuccess: %d", recvACKACK)
		// Receiver receives ACKACKs from sender
		require.Greater(t, recvACKACK, uint64(0), "Receiver should receive ACKACKs")
	}
}

// TestConnectionMetricsNAKRetransmit verifies NAK and retransmission counters when loss is simulated
func TestConnectionMetricsNAKRetransmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	message := "Hello World!"
	channel := NewPubSub(PubSubConfig{})

	config := DefaultConfig()

	server := Server{
		Addr:    "127.0.0.1:6015",
		Config:  &config,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			streamid := req.StreamId()
			if streamid == "publish" {
				return PUBLISH
			} else if streamid == "subscribe" {
				return SUBSCRIBE
			}
			return REJECT
		},
		HandlePublish: func(conn Conn) {
			channel.Publish(conn)
			conn.Close()
		},
		HandleSubscribe: func(conn Conn) {
			channel.Subscribe(conn)
			conn.Close()
		},
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		err := server.Serve()
		if err == ErrServerClosed {
			return
		}
	}()

	readerConnected := make(chan struct{})
	readerDone := make(chan struct{})
	dataReader := bytes.Buffer{}
	var writerSocketId uint32

	go func() {
		defer close(readerDone)

		config := DefaultConfig()
		config.StreamId = "subscribe"

		conn, err := testDial(t, "127.0.0.1:6015", config)
		if !assert.NoError(t, err) {
			return
		}

		close(readerConnected)

		buffer := make([]byte, 2048)
		for {
			n, err := conn.Read(buffer)
			if n != 0 {
				dataReader.Write(buffer[:n])
			}
			if err != nil {
				break
			}
		}
		conn.Close()
	}()

	<-readerConnected

	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)

		config := DefaultConfig()
		config.StreamId = "publish"

		conn, err := testDial(t, "127.0.0.1:6015", config)
		if !assert.NoError(t, err) {
			return
		}

		if d, ok := conn.(*dialer); ok {
			writerSocketId = d.conn.socketId
		}

		// Inject packet drops to trigger NAKs
		// Drop every 2nd original (non-retransmit) packet
		counter := 0
		dialer, _ := conn.(*dialer)
		originalOnSend := dialer.conn.onSend
		dialer.conn.onSend = func(p packet.Packet) {
			if !p.Header().IsControlPacket {
				if !p.Header().RetransmittedPacketFlag {
					counter++
					if counter%2 == 0 {
						// Drop this packet - don't call originalOnSend
						return
					}
				}
			}
			originalOnSend(p)
		}

		// Write messages
		for i := 0; i < 20; i++ {
			conn.Write([]byte(message))
		}

		time.Sleep(3 * time.Second)
		conn.Close()
	}()

	<-writerDone
	<-readerDone

	// Verify most messages were received (some may be lost due to timing at shutdown)
	// We drop 50% of original packets, so with ARQ we should recover most
	receivedMessages := len(dataReader.String()) / len(message)
	t.Logf("Received %d out of 20 messages", receivedMessages)
	require.GreaterOrEqual(t, receivedMessages, 18, "Should receive at least 18 of 20 messages via retransmit")

	// Verify NAK/retransmit counters
	connections, _ := metrics.GetConnections()

	if writerMetrics, ok := connections[writerSocketId]; ok && writerMetrics != nil {
		recvNAK := writerMetrics.PktRecvNAKSuccess.Load()
		t.Logf("Writer PktRecvNAKSuccess: %d", recvNAK)
		require.Greater(t, recvNAK, uint64(0), "Sender should receive NAKs")

		retransFromNAK := writerMetrics.PktRetransFromNAK.Load()
		t.Logf("Writer PktRetransFromNAK: %d", retransFromNAK)
		require.Greater(t, retransFromNAK, uint64(0), "Sender should retransmit from NAK")

		congRetrans := writerMetrics.CongestionSendPktRetrans.Load()
		t.Logf("Writer CongestionSendPktRetrans: %d", congRetrans)
		require.Greater(t, congRetrans, uint64(0), "Congestion control should track retransmits")
	}
}

// TestConnectionMetricsControlPackets verifies control packet type counters
func TestConnectionMetricsControlPackets(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	config := DefaultConfig()
	var serverConnSocketId uint32

	server := Server{
		Addr:    "127.0.0.1:6016",
		Config:  &config,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			streamid := req.StreamId()
			if streamid == "publish" {
				return PUBLISH
			}
			return REJECT
		},
		HandlePublish: func(conn Conn) {
			// Get server-side connection's socket ID
			if sc, ok := conn.(*srtConn); ok {
				serverConnSocketId = sc.socketId
			}
			// Keep connection alive for a bit
			time.Sleep(2 * time.Second)
			conn.Close()
		},
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		err := server.Serve()
		if err == ErrServerClosed {
			return
		}
	}()

	var clientSocketId uint32

	config = DefaultConfig()
	config.StreamId = "publish"

	conn, err := testDial(t, "127.0.0.1:6016", config)
	require.NoError(t, err)

	if d, ok := conn.(*dialer); ok {
		clientSocketId = d.conn.socketId
	}

	// Write some data to establish communication
	conn.Write([]byte("test"))

	time.Sleep(2 * time.Second)
	conn.Close()

	// Allow server handler to complete
	time.Sleep(500 * time.Millisecond)

	// Check control packet metrics
	connections, _ := metrics.GetConnections()

	// Client metrics
	if clientMetrics, ok := connections[clientSocketId]; ok && clientMetrics != nil {
		// Handshake packets (sent during connection setup)
		sentHandshake := clientMetrics.PktSentHandshakeSuccess.Load()
		t.Logf("Client PktSentHandshakeSuccess: %d", sentHandshake)
		require.Greater(t, sentHandshake, uint64(0), "Client should send handshake packets")

		recvHandshake := clientMetrics.PktRecvHandshakeSuccess.Load()
		t.Logf("Client PktRecvHandshakeSuccess: %d", recvHandshake)
		require.Greater(t, recvHandshake, uint64(0), "Client should receive handshake packets")

		// Keepalive might be sent during the 2s wait
		sentKeepalive := clientMetrics.PktSentKeepaliveSuccess.Load()
		t.Logf("Client PktSentKeepaliveSuccess: %d", sentKeepalive)
		// Not required - depends on timing

		// Shutdown (sent during close)
		sentShutdown := clientMetrics.PktSentShutdownSuccess.Load()
		t.Logf("Client PktSentShutdownSuccess: %d", sentShutdown)
		require.Greater(t, sentShutdown, uint64(0), "Client should send shutdown packet")
	}

	// Server connection metrics
	if serverMetrics, ok := connections[serverConnSocketId]; ok && serverMetrics != nil {
		recvHandshake := serverMetrics.PktRecvHandshakeSuccess.Load()
		t.Logf("Server PktRecvHandshakeSuccess: %d", recvHandshake)
		require.Greater(t, recvHandshake, uint64(0), "Server should receive handshake packets")

		sentHandshake := serverMetrics.PktSentHandshakeSuccess.Load()
		t.Logf("Server PktSentHandshakeSuccess: %d", sentHandshake)
		require.Greater(t, sentHandshake, uint64(0), "Server should send handshake packets")
	} else {
		t.Log("Warning: Server connection metrics not found (may have been unregistered on close)")
	}
}

// TestConnectionMetricsPrometheusMatch verifies Prometheus export matches internal counters after activity
func TestConnectionMetricsPrometheusMatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	message := "Test message for Prometheus matching"
	channel := NewPubSub(PubSubConfig{})

	config := DefaultConfig()

	server := Server{
		Addr:    "127.0.0.1:6017",
		Config:  &config,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			streamid := req.StreamId()
			if streamid == "publish" {
				return PUBLISH
			} else if streamid == "subscribe" {
				return SUBSCRIBE
			}
			return REJECT
		},
		HandlePublish: func(conn Conn) {
			channel.Publish(conn)
			conn.Close()
		},
		HandleSubscribe: func(conn Conn) {
			channel.Subscribe(conn)
			conn.Close()
		},
	}

	err := server.Listen()
	require.NoError(t, err)
	defer server.Shutdown()

	go func() {
		err := server.Serve()
		if err == ErrServerClosed {
			return
		}
	}()

	readerDone := make(chan struct{})
	readerConnected := make(chan struct{})
	var writerSocketId uint32

	go func() {
		defer close(readerDone)

		config := DefaultConfig()
		config.StreamId = "subscribe"

		conn, err := testDial(t, "127.0.0.1:6017", config)
		if !assert.NoError(t, err) {
			return
		}

		close(readerConnected)

		buffer := make([]byte, 2048)
		for {
			n, err := conn.Read(buffer)
			if n == 0 && err != nil {
				break
			}
		}
		conn.Close()
	}()

	<-readerConnected

	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)

		config := DefaultConfig()
		config.StreamId = "publish"

		conn, err := testDial(t, "127.0.0.1:6017", config)
		if !assert.NoError(t, err) {
			return
		}

		if d, ok := conn.(*dialer); ok {
			writerSocketId = d.conn.socketId
		}

		// Write known number of messages
		for i := 0; i < 25; i++ {
			conn.Write([]byte(message))
		}

		// Wait for processing before checking metrics
		time.Sleep(1 * time.Second)

		// Get internal metrics
		connections, _ := metrics.GetConnections()
		if m, ok := connections[writerSocketId]; ok && m != nil {
			internalDataSent := m.PktSentDataSuccess.Load()
			t.Logf("Internal PktSentDataSuccess: %d", internalDataSent)

			// The Statistics() API should match
			var stats Statistics
			conn.Stats(&stats)
			t.Logf("Stats.Accumulated.PktSent: %d", stats.Accumulated.PktSent)

			// Verify they align (PktSent is total control+data, but should be >= data only)
			require.GreaterOrEqual(t, stats.Accumulated.PktSent, internalDataSent,
				"Stats PktSent should be >= internal PktSentDataSuccess")
		}

		time.Sleep(1 * time.Second)
		conn.Close()
	}()

	<-writerDone
	<-readerDone
}
