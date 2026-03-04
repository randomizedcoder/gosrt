package srt

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/randomizedcoder/gosrt/packet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEncryption(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	message := "Hello World!"
	passphrase := "foobarfoobar"
	channel := NewPubSub(PubSubConfig{})

	config := DefaultConfig()
	config.EnforcedEncryption = true

	server := Server{
		Addr:    "127.0.0.1:6003",
		Config:  &config,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			if req.IsEncrypted() {
				if err := req.SetPassphrase(passphrase); err != nil {
					return REJECT
				}
			}

			streamid := req.StreamId()

			switch streamid {
			case "publish":
				return PUBLISH
			case "subscribe":
				return SUBSCRIBE
			}

			return REJECT
		},
		HandlePublish: func(conn Conn) {
			if err := channel.Publish(conn); err != nil {
				t.Logf("HandlePublish: Publish error (expected during shutdown): %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Logf("HandlePublish: Close error (expected during shutdown): %v", err)
			}
		},
		HandleSubscribe: func(conn Conn) {
			if err := channel.Subscribe(conn); err != nil {
				t.Logf("HandleSubscribe: Subscribe error (expected during shutdown): %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Logf("HandleSubscribe: Close error (expected during shutdown): %v", err)
			}
		},
	}

	listenErr := server.Listen()
	require.NoError(t, listenErr)

	defer server.Shutdown()

	go func() {
		serveErr := server.Serve()
		if serveErr == ErrServerClosed {
			return
		}
	}()

	{
		// Reject connection if wrong password is set
		wrongPassConfig := DefaultConfig()
		wrongPassConfig.StreamId = "subscribe"
		wrongPassConfig.Passphrase = "barfoobarfoo"

		_, dialErr := testDial(t, "127.0.0.1:6003", wrongPassConfig)
		require.Error(t, dialErr)
	}
	// Test transmitting an encrypted message

	readerConnected := make(chan struct{})
	readerDone := make(chan struct{})

	dataReader1 := bytes.Buffer{}

	go func() {
		defer close(readerDone)

		readerConfig := DefaultConfig()
		readerConfig.StreamId = "subscribe"
		readerConfig.Passphrase = "foobarfoobar"

		conn, dialErr := testDial(t, "127.0.0.1:6003", readerConfig)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		close(readerConnected)

		buffer := make([]byte, 2048)

		for {
			n, readErr := conn.Read(buffer)
			if n != 0 {
				dataReader1.Write(buffer[:n])
			}

			if readErr != nil {
				break
			}
		}

		_ = conn.Close()
	}()

	<-readerConnected

	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)

		writerConfig := DefaultConfig()
		writerConfig.StreamId = "publish"
		writerConfig.Passphrase = "foobarfoobar"

		conn, dialErr := testDial(t, "127.0.0.1:6003", writerConfig)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		n, writeErr := conn.Write([]byte(message))
		if !assert.NoError(t, writeErr) {
			panic(writeErr.Error())
		}
		assert.Equal(t, 12, n)

		time.Sleep(3 * time.Second)

		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	<-writerDone
	<-readerDone

	reader1 := dataReader1.String()

	require.Equal(t, message, reader1)
}

// Test for https://github.com/randomizedcoder/gosrt/pull/94
func TestEncryptionRetransmit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	message := "Hello World!"
	passphrase := "foobarfoobar"
	channel := NewPubSub(PubSubConfig{})

	config := DefaultConfig()
	config.EnforcedEncryption = true

	server := Server{
		Addr:    "127.0.0.1:6003",
		Config:  &config,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			if req.IsEncrypted() {
				if err := req.SetPassphrase(passphrase); err != nil {
					return REJECT
				}
			}

			streamid := req.StreamId()

			switch streamid {
			case "publish":
				return PUBLISH
			case "subscribe":
				return SUBSCRIBE
			}

			return REJECT
		},
		HandlePublish: func(conn Conn) {
			if err := channel.Publish(conn); err != nil {
				t.Logf("HandlePublish: Publish error (expected during shutdown): %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Logf("HandlePublish: Close error (expected during shutdown): %v", err)
			}
		},
		HandleSubscribe: func(conn Conn) {
			if err := channel.Subscribe(conn); err != nil {
				t.Logf("HandleSubscribe: Subscribe error (expected during shutdown): %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Logf("HandleSubscribe: Close error (expected during shutdown): %v", err)
			}
		},
	}

	listenErr := server.Listen()
	require.NoError(t, listenErr)

	defer server.Shutdown()

	go func() {
		serveErr := server.Serve()
		if serveErr == ErrServerClosed {
			return
		}
	}()

	{
		// Reject connection if wrong password is set
		wrongPassConfig := DefaultConfig()
		wrongPassConfig.StreamId = "subscribe"
		wrongPassConfig.Passphrase = "barfoobarfoo"

		_, dialErr := testDial(t, "127.0.0.1:6003", wrongPassConfig)
		require.Error(t, dialErr)
	}

	// Test transmitting an encrypted message

	readerConnected := make(chan struct{})
	readerDone := make(chan struct{})

	dataReader1 := bytes.Buffer{}

	go func() {
		defer close(readerDone)

		readerConfig := DefaultConfig()
		readerConfig.StreamId = "subscribe"
		readerConfig.Passphrase = "foobarfoobar"

		conn, dialErr := testDial(t, "127.0.0.1:6003", readerConfig)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		close(readerConnected)

		buffer := make([]byte, 2048)

		for {
			n, readErr := conn.Read(buffer)
			if n != 0 {
				dataReader1.Write(buffer[:n])
			}

			if readErr != nil {
				break
			}
		}

		_ = conn.Close()
	}()

	<-readerConnected

	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)

		// Set up packet drop filter BEFORE dialing (to avoid race)
		counter := 0
		writerConfig := DefaultConfig()
		writerConfig.StreamId = "publish"
		writerConfig.Passphrase = "foobarfoobar"
		writerConfig.SendFilter = func(p packet.Packet) bool {
			if !p.Header().IsControlPacket {
				// Drop every 2nd original packet
				if !p.Header().RetransmittedPacketFlag {
					counter++
					if counter%2 == 0 {
						return false // Drop this packet
					}
				}
			}
			return true // Send the packet
		}

		conn, dialErr := testDial(t, "127.0.0.1:6003", writerConfig)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		for i := 0; i < 5; i++ {
			n, writeErr := conn.Write([]byte(message))
			if !assert.NoError(t, writeErr) {
				panic(writeErr.Error())
			}
			assert.Equal(t, 12, n)
		}

		time.Sleep(3 * time.Second)

		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	<-writerDone
	<-readerDone

	reader1 := dataReader1.String()

	require.Equal(t, message+message+message+message+message, reader1)
}

func TestEncryptionKeySwap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	message := "Hello World!"
	passphrase := "foobarfoobar"
	channel := NewPubSub(PubSubConfig{})

	config := DefaultConfig()
	config.EnforcedEncryption = true

	server := Server{
		Addr:    "127.0.0.1:6003",
		Config:  &config,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			if req.IsEncrypted() {
				if err := req.SetPassphrase(passphrase); err != nil {
					return REJECT
				}
			}

			streamid := req.StreamId()

			switch streamid {
			case "publish":
				return PUBLISH
			case "subscribe":
				return SUBSCRIBE
			}

			return REJECT
		},
		HandlePublish: func(conn Conn) {
			if err := channel.Publish(conn); err != nil {
				t.Logf("HandlePublish: Publish error (expected during shutdown): %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Logf("HandlePublish: Close error (expected during shutdown): %v", err)
			}
		},
		HandleSubscribe: func(conn Conn) {
			if err := channel.Subscribe(conn); err != nil {
				t.Logf("HandleSubscribe: Subscribe error (expected during shutdown): %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Logf("HandleSubscribe: Close error (expected during shutdown): %v", err)
			}
		},
	}

	listenErr := server.Listen()
	require.NoError(t, listenErr)

	defer server.Shutdown()

	go func() {
		serveErr := server.Serve()
		if serveErr == ErrServerClosed {
			return
		}
	}()

	// Test transmitting encrypted messages with key swap in between

	dataReader1 := bytes.Buffer{}

	readerConnected := make(chan struct{})
	readerDone := make(chan struct{})

	go func() {
		defer close(readerDone)

		readerConfig := DefaultConfig()
		readerConfig.StreamId = "subscribe"
		readerConfig.Passphrase = "foobarfoobar"

		conn, dialErr := testDial(t, "127.0.0.1:6003", readerConfig)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		buffer := make([]byte, 2048)

		close(readerConnected)

		for {
			n, readErr := conn.Read(buffer)
			if n != 0 {
				dataReader1.Write(buffer[:n])
			}

			if readErr != nil {
				break
			}
		}

		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	<-readerConnected

	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)

		writerConfig := DefaultConfig()
		writerConfig.StreamId = "publish"
		writerConfig.Passphrase = "foobarfoobar"
		// Swap encryption key after 50 sent messages
		writerConfig.KMPreAnnounce = 10
		writerConfig.KMRefreshRate = 30

		conn, dialErr := testDial(t, "127.0.0.1:6003", writerConfig)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		// Send 150 messages
		for i := 0; i < 150; i++ {
			n, writeErr := conn.Write([]byte(message))
			if !assert.NoError(t, writeErr) {
				panic(writeErr.Error())
			}
			assert.Equal(t, 12, n)
		}

		time.Sleep(3 * time.Second)

		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	<-writerDone
	<-readerDone

	reader1 := dataReader1.String()

	require.Equal(t, strings.Repeat(message, 150), reader1)
}

func TestStats(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	message := "Hello World!"
	channel := NewPubSub(PubSubConfig{})

	config := DefaultConfig()

	server := Server{
		Addr:    "127.0.0.1:6003",
		Config:  &config,
		Context: ctx,
		HandleConnect: func(req ConnRequest) ConnType {
			streamid := req.StreamId()

			switch streamid {
			case "publish":
				return PUBLISH
			case "subscribe":
				return SUBSCRIBE
			}

			return REJECT
		},
		HandlePublish: func(conn Conn) {
			if err := channel.Publish(conn); err != nil {
				t.Logf("HandlePublish: Publish error (expected during shutdown): %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Logf("HandlePublish: Close error (expected during shutdown): %v", err)
			}
		},
		HandleSubscribe: func(conn Conn) {
			if err := channel.Subscribe(conn); err != nil {
				t.Logf("HandleSubscribe: Subscribe error (expected during shutdown): %v", err)
			}
			if err := conn.Close(); err != nil {
				t.Logf("HandleSubscribe: Close error (expected during shutdown): %v", err)
			}
		},
	}

	listenErr := server.Listen()
	require.NoError(t, listenErr)

	defer server.Shutdown()

	go func() {
		serveErr := server.Serve()
		if serveErr == ErrServerClosed {
			return
		}
	}()

	statsReader := Statistics{}
	statsWriter := Statistics{}

	readerConnected := make(chan struct{})
	readerDone := make(chan struct{})

	dataReader1 := bytes.Buffer{}

	go func() {
		defer close(readerDone)

		readerConfig := DefaultConfig()
		readerConfig.StreamId = "subscribe"

		conn, dialErr := testDial(t, "127.0.0.1:6003", readerConfig)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		close(readerConnected)

		buffer := make([]byte, 2048)

		for {
			n, readErr := conn.Read(buffer)
			if n != 0 {
				dataReader1.Write(buffer[:n])
			}

			if readErr != nil {
				break
			}
		}

		conn.Stats(&statsReader)

		_ = conn.Close()
	}()

	<-readerConnected

	writerDone := make(chan struct{})

	go func() {
		defer close(writerDone)

		writerConfig := DefaultConfig()
		writerConfig.StreamId = "publish"

		conn, dialErr := testDial(t, "127.0.0.1:6003", writerConfig)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		n, writeErr := conn.Write([]byte(message))
		if !assert.NoError(t, writeErr) {
			panic(writeErr.Error())
		}
		assert.Equal(t, 12, n)

		time.Sleep(3 * time.Second)

		conn.Stats(&statsWriter)

		closeErr := conn.Close()
		assert.NoError(t, closeErr)
	}()

	<-writerDone
	<-readerDone

	reader1 := dataReader1.String()

	require.Equal(t, message, reader1)

	require.Equal(t, uint64(len(message)+44), statsReader.Accumulated.ByteRecv)
	require.Equal(t, uint64(1), statsReader.Accumulated.PktRecv)

	require.Equal(t, uint64(len(message)+44), statsWriter.Accumulated.ByteSent)
	require.Equal(t, uint64(1), statsWriter.Accumulated.PktSent)
}
