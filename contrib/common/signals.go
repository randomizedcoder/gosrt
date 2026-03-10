package common

import (
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
)

// SetupPauseHandler installs a SIGUSR1 handler that sets the paused flag
// to true when received. The label is printed to stderr when the signal
// is received (e.g., "stopping data generation", "stopping UDP receive").
func SetupPauseHandler(paused *atomic.Bool, label string) {
	pauseChan := make(chan os.Signal, 1)
	signal.Notify(pauseChan, syscall.SIGUSR1)
	go func() {
		<-pauseChan
		fmt.Fprintf(os.Stderr, "\nPAUSE signal received - %s\n", label)
		paused.Store(true)
	}()
}
