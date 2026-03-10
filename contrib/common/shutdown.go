package common

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// WaitForShutdown waits for all goroutines tracked by wg to complete,
// with a timeout. Prints elapsed time on completion or timeout message.
func WaitForShutdown(wg *sync.WaitGroup, shutdownStart time.Time, delay time.Duration) {
	if delay <= 0 {
		elapsedMs := time.Since(shutdownStart).Milliseconds()
		fmt.Fprintf(os.Stderr, "Shutdown timed out after %s (elapsed: %dms)\n", delay, elapsedMs)
		return
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		elapsedMs := time.Since(shutdownStart).Milliseconds()
		fmt.Fprintf(os.Stderr, "Graceful shutdown complete after %dms\n", elapsedMs)
	case <-time.After(delay):
		elapsedMs := time.Since(shutdownStart).Milliseconds()
		fmt.Fprintf(os.Stderr, "Shutdown timed out after %s (elapsed: %dms)\n", delay, elapsedMs)
	}
}
