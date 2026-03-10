package srt

import (
	"fmt"
	"os"
	"sync"
)

// RunLoggerOutput starts a goroutine that forwards Logger messages to stderr.
// The goroutine exits when the logger is closed. Caller must call logger.Close()
// to unblock the goroutine during shutdown.
func RunLoggerOutput(logger Logger, wg *sync.WaitGroup) {
	if logger == nil {
		return
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for m := range logger.Listen() {
			fmt.Fprintf(os.Stderr, "%#08x %s (in %s:%d)\n%s \n",
				m.SocketId, m.Topic, m.File, m.Line, m.Message)
		}
	}()
}
