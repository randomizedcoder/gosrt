package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	srt "github.com/randomizedcoder/gosrt"
	"github.com/randomizedcoder/gosrt/contrib/common"
	"github.com/pkg/profile"
)

const (
	KM_PRE_ANNOUNCE = 200
	KM_REFRESH_RATE = 10000
)

// server is an implementation of the Server framework
type server struct {
	// Configuration parameter taken from the Config
	addr       string
	app        string
	token      string
	passphrase string
	logtopics  string
	profile    string

	server *srt.Server

	// Map of publishing channels and a lock to serialize
	// access to the map.
	channels map[string]srt.PubSub
	lock     sync.RWMutex
}

func (s *server) ListenAndServe() error {
	if len(s.app) == 0 {
		s.app = "/"
	}

	return s.server.ListenAndServe()
}

func (s *server) Shutdown() {
	s.server.Shutdown()
}

var (
	// Server-specific flags
	addr        = flag.String("addr", "", "address to listen on")
	app         = flag.String("app", "", "path prefix for streamid")
	token       = flag.String("token", "", "token query param for streamid")
	passphrase  = flag.String("passphrase", "", "passphrase for de- and enrcypting the data")
	logtopics   = flag.String("logtopics", "", "topics for the log output")
	profileFlag = flag.String("profile", "", "enable profiling (cpu, mem, allocs, heap, rate, mutex, block, thread, trace)")
	profilePath = flag.String("profilepath", ".", "directory to write profile files to")
	testflags   = flag.Bool("testflags", false, "Test mode: parse flags, apply to config, print config as JSON, and exit")
	printConfig = flag.Bool("printconfig", false, "Print config")
)

func main() {
	s := server{
		channels: make(map[string]srt.PubSub),
	}

	// Parse all flags (shared + server-specific)
	common.ParseFlags()

	// Validate flag dependencies and auto-enable required flags
	if warnings := common.ValidateFlagDependencies(); len(warnings) > 0 {
		for _, w := range warnings {
			fmt.Fprintf(os.Stderr, "⚠ %s\n", w)
		}
	}

	// Test mode: print config and exit
	if *testflags {
		config := srt.DefaultConfig()
		common.ApplyFlagsToConfig(&config)
		// Handle server-specific passphrase flag (overrides shared passphrase-flag if set)
		if common.FlagSet["passphrase"] {
			config.Passphrase = *passphrase
		}
		// Print config as JSON
		data, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
		os.Exit(0)
	}

	// Set server fields from flags
	s.addr = *addr
	s.app = *app
	s.token = *token
	s.passphrase = *passphrase
	s.logtopics = *logtopics
	s.profile = *profileFlag

	if len(s.addr) == 0 {
		fmt.Fprintf(os.Stderr, "Provide a listen address with -addr\n")
		os.Exit(1)
	}

	var p func(*profile.Profile)
	switch *profileFlag {
	case "cpu":
		p = profile.CPUProfile
	case "mem":
		p = profile.MemProfile
	case "allocs":
		p = profile.MemProfileAllocs
	case "heap":
		p = profile.MemProfileHeap
	case "rate":
		p = profile.MemProfileRate(2048)
	case "mutex":
		p = profile.MutexProfile
	case "block":
		p = profile.BlockProfile
	case "thread":
		p = profile.ThreadcreationProfile
	case "trace":
		p = profile.TraceProfile
	default:
	}

	if p != nil {
		defer profile.Start(profile.ProfilePath(*profilePath), profile.NoShutdownHook, p).Stop()
	}

	config := srt.DefaultConfig()

	// Apply CLI flags (shared flags)
	common.ApplyFlagsToConfig(&config)

	// Handle server-specific passphrase flag (overrides shared passphrase-flag if set)
	if common.FlagSet["passphrase"] {
		config.Passphrase = *passphrase
	}

	if len(*logtopics) != 0 {
		config.Logger = srt.NewLogger(strings.Split(*logtopics, ","))
	}

	config.KMPreAnnounce = KM_PRE_ANNOUNCE
	config.KMRefreshRate = KM_REFRESH_RATE

	if *printConfig {
		data, err := json.MarshalIndent(config, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error marshaling config: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(data))
	}

	// ============================================================
	// Create context that cancels on signal (replaces setupSignalHandler)
	// ============================================================
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Single waitgroup for all goroutines
	var wg sync.WaitGroup

	// ============================================================
	// Start Prometheus Metrics Server(s) (if configured)
	// ============================================================
	if err := common.StartMetricsServers(ctx, &wg, *common.PromHTTPAddr, *common.PromUDSPath); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start metrics server: %v\n", err)
		os.Exit(1)
	}

	// ============================================================
	// Start Logger Goroutine (if enabled)
	// ============================================================
	if config.Logger != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for m := range config.Logger.Listen() {
				fmt.Fprintf(os.Stderr, "%#08x %s (in %s:%d)\n%s \n",
					m.SocketId, m.Topic, m.File, m.Line, m.Message)
			}
		}()
	}

	// ============================================================
	// Start Statistics Ticker (if enabled)
	// ============================================================
	if config.StatisticsPrintInterval > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(config.StatisticsPrintInterval)
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					connections := s.server.GetConnections()
					common.PrintConnectionStatistics(connections,
						config.StatisticsPrintInterval.String(), nil)
				}
			}
		}()
	}

	// ============================================================
	// Setup and Start SRT Server
	// ============================================================
	s.server = srt.NewServer(ctx, &wg, srt.ServerConfig{
		Addr:            s.addr,
		Config:          &config,
		HandleConnect:   s.handleConnect,
		HandlePublish:   s.handlePublish,
		HandleSubscribe: s.handleSubscribe,
	})

	fmt.Fprintf(os.Stderr, "Listening on %s\n", s.addr)

	// Run SRT server
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := s.ListenAndServe(); err != nil && err != srt.ErrServerClosed {
			fmt.Fprintf(os.Stderr, "SRT Server: %s\n", err)
			os.Exit(2)
		}
	}()

	// ============================================================
	// Wait for Shutdown Signal
	// ============================================================
	<-ctx.Done()
	shutdownStart := time.Now()
	fmt.Fprintf(os.Stderr, "\nShutdown signal received\n")

	// ============================================================
	// Cleanup
	// ============================================================
	// Close logger so its goroutine can exit (channel will close)
	if config.Logger != nil {
		config.Logger.Close()
	}

	// ============================================================
	// Wait for All Goroutines with Timeout
	// ============================================================
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		elapsedMs := time.Since(shutdownStart).Milliseconds()
		fmt.Fprintf(os.Stderr, "Graceful shutdown complete after %dms\n", elapsedMs)
	case <-time.After(config.ShutdownDelay):
		elapsedMs := time.Since(shutdownStart).Milliseconds()
		fmt.Fprintf(os.Stderr, "Shutdown timed out after %s (elapsed: %dms)\n", config.ShutdownDelay, elapsedMs)
	}
}

func (s *server) log(who, action, path, message string, client net.Addr) {
	fmt.Fprintf(os.Stderr, "%-10s %10s %s (%s) %s\n", who, action, path, client, message)
}

func (s *server) handleConnect(req srt.ConnRequest) srt.ConnType {
	var mode srt.ConnType = srt.SUBSCRIBE
	client := req.RemoteAddr()

	channel := ""

	if req.Version() == 4 {
		mode = srt.PUBLISH
		channel = "/" + client.String()

		req.SetPassphrase(s.passphrase)
	} else if req.Version() == 5 {
		streamId := req.StreamId()
		path := streamId

		if strings.HasPrefix(streamId, "publish:") {
			mode = srt.PUBLISH
			path = strings.TrimPrefix(streamId, "publish:")
		} else if strings.HasPrefix(streamId, "subscribe:") {
			path = strings.TrimPrefix(streamId, "subscribe:")
		}

		u, err := url.Parse(path)
		if err != nil {
			return srt.REJECT
		}

		if req.IsEncrypted() {
			if err := req.SetPassphrase(s.passphrase); err != nil {
				s.log("CONNECT", "FORBIDDEN", u.Path, err.Error(), client)
				return srt.REJECT
			}
		}

		// Check the token
		token := u.Query().Get("token")
		if len(s.token) != 0 && s.token != token {
			s.log("CONNECT", "FORBIDDEN", u.Path, "invalid token ("+token+")", client)
			return srt.REJECT
		}

		// Check the app patch
		if !strings.HasPrefix(u.Path, s.app) {
			s.log("CONNECT", "FORBIDDEN", u.Path, "invalid app", client)
			return srt.REJECT
		}

		if len(strings.TrimPrefix(u.Path, s.app)) == 0 {
			s.log("CONNECT", "INVALID", u.Path, "stream name not provided", client)
			return srt.REJECT
		}

		channel = u.Path
	} else {
		return srt.REJECT
	}

	s.lock.RLock()
	pubsub := s.channels[channel]
	s.lock.RUnlock()

	if mode == srt.PUBLISH && pubsub != nil {
		s.log("CONNECT", "CONFLICT", channel, "already publishing", client)
		return srt.REJECT
	}

	if mode == srt.SUBSCRIBE && pubsub == nil {
		s.log("CONNECT", "NOTFOUND", channel, "not publishing", client)
		return srt.REJECT
	}

	return mode
}

func (s *server) handlePublish(conn srt.Conn) {
	channel := ""
	client := conn.RemoteAddr()
	if client == nil {
		conn.Close()
		return
	}

	if conn.Version() == 4 {
		channel = "/" + client.String()
	} else if conn.Version() == 5 {
		streamId := conn.StreamId()
		path := strings.TrimPrefix(streamId, "publish:")

		channel = path
	} else {
		s.log("PUBLISH", "INVALID", channel, "unknown connection version", client)
		conn.Close()
		return
	}

	// Look for the stream
	s.lock.Lock()
	pubsub := s.channels[channel]
	if pubsub == nil {
		pubsub = srt.NewPubSub(srt.PubSubConfig{
			Logger: s.server.Config.Logger,
		})
		s.channels[channel] = pubsub
	} else {
		pubsub = nil
	}
	s.lock.Unlock()

	if pubsub == nil {
		s.log("PUBLISH", "CONFLICT", channel, "already publishing", client)
		conn.Close()
		return
	}

	s.log("PUBLISH", "START", channel, "publishing", client)

	pubsub.Publish(conn)

	s.lock.Lock()
	delete(s.channels, channel)
	s.lock.Unlock()

	s.log("PUBLISH", "STOP", channel, "", client)

	stats := &srt.Statistics{}
	conn.Stats(stats)

	fmt.Fprintf(os.Stderr, "%+v\n", stats)

	conn.Close()
}

func (s *server) handleSubscribe(conn srt.Conn) {
	channel := ""
	client := conn.RemoteAddr()
	if client == nil {
		conn.Close()
		return
	}

	if conn.Version() == 4 {
		channel = client.String()
	} else if conn.Version() == 5 {
		streamId := conn.StreamId()
		path := strings.TrimPrefix(streamId, "subscribe:")

		channel = path
	} else {
		s.log("SUBSCRIBE", "INVALID", channel, "unknown connection version", client)
		conn.Close()
		return
	}

	s.log("SUBSCRIBE", "START", channel, "", client)

	// Look for the stream
	s.lock.RLock()
	pubsub := s.channels[channel]
	s.lock.RUnlock()

	if pubsub == nil {
		s.log("SUBSCRIBE", "NOTFOUND", channel, "not publishing", client)
		conn.Close()
		return
	}

	pubsub.Subscribe(conn)

	s.log("SUBSCRIBE", "STOP", channel, "", client)

	stats := &srt.Statistics{}
	conn.Stats(stats)

	fmt.Fprintf(os.Stderr, "%+v\n", stats)

	conn.Close()
}
