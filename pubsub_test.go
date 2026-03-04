package srt

import (
	"bytes"
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPubSub(t *testing.T) {
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

	go func() {
		serveErr := server.Serve()
		if serveErr == ErrServerClosed {
			return
		}
	}()

	readerReadyWg := sync.WaitGroup{}
	readerReadyWg.Add(2)

	readerDoneWg := sync.WaitGroup{}
	readerDoneWg.Add(2)

	dataReader1 := bytes.Buffer{}
	dataReader2 := bytes.Buffer{}

	go func() {
		reader1Config := DefaultConfig()
		reader1Config.StreamId = "subscribe"

		conn, dialErr := testDial(t, "127.0.0.1:6003", reader1Config)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		buffer := make([]byte, 2048)

		readerReadyWg.Done()

		for {
			n, readErr := conn.Read(buffer)
			if n != 0 {
				dataReader1.Write(buffer[:n])
			}

			if readErr != nil {
				break
			}
		}

		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("Reader1: conn.Close error (expected during shutdown): %v", closeErr)
		}

		readerDoneWg.Done()
	}()

	go func() {
		reader2Config := DefaultConfig()
		reader2Config.StreamId = "subscribe"

		conn, dialErr := testDial(t, "127.0.0.1:6003", reader2Config)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		buffer := make([]byte, 2048)

		readerReadyWg.Done()

		for {
			n, readErr := conn.Read(buffer)
			if n != 0 {
				dataReader2.Write(buffer[:n])
			}

			if readErr != nil {
				break
			}
		}

		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("Reader2: conn.Close error (expected during shutdown): %v", closeErr)
		}

		readerDoneWg.Done()
	}()

	readerReadyWg.Wait()

	writerWg := sync.WaitGroup{}
	writerWg.Add(1)

	go func() {
		writerConfig := DefaultConfig()
		writerConfig.StreamId = "publish"

		conn, dialErr := testDial(t, "127.0.0.1:6003", writerConfig)
		if !assert.NoError(t, dialErr) {
			panic(dialErr.Error())
		}

		n, writeErr := conn.Write([]byte(message))
		if writeErr != nil {
			t.Logf("Writer: Write error: %v", writeErr)
		}
		require.Equal(t, 12, n)

		time.Sleep(3 * time.Second)

		if closeErr := conn.Close(); closeErr != nil {
			t.Logf("Writer: conn.Close error (expected during shutdown): %v", closeErr)
		}

		writerWg.Done()
	}()

	writerWg.Wait()
	readerDoneWg.Wait()

	server.Shutdown()

	reader1 := dataReader1.String()
	reader2 := dataReader2.String()

	require.Equal(t, message, reader1)
	require.Equal(t, message, reader2)
}
