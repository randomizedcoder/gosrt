package srt

import (
	"runtime"
	"strings"
	"sync/atomic"
	"time"
)

// Logger is for logging debug messages.
type Logger interface {
	// HasTopic returns whether this Logger is logging messages of that topic.
	HasTopic(topic string) bool

	// Print adds a new message to the message queue. The message itself is
	// a function that returns the string to be logges. It will only be
	// executed if HasTopic returns true on the given topic.
	Print(topic string, socketId uint32, skip int, message func() string)

	// Listen returns a read channel for Log messages.
	Listen() <-chan Log

	// Close closes the logger. No more messages will be logged.
	Close()
}

// logger implements a Logger
type logger struct {
	logQueue chan Log
	topics   map[string]bool
	closed   atomic.Uint32 // 0 = open, 1 = closed
}

// NewLogger returns a Logger that only listens on the given list of topics.
func NewLogger(topics []string) Logger {
	l := &logger{
		logQueue: make(chan Log, 1024),
		topics:   make(map[string]bool),
	}

	for _, topic := range topics {
		l.topics[topic] = true
	}

	return l
}

func (l *logger) HasTopic(topic string) bool {
	if len(l.topics) == 0 {
		return false
	}

	if ok := l.topics[topic]; ok {
		return true
	}

	topicLen := len(topic)

	for {
		i := strings.LastIndexByte(topic[:topicLen], ':')
		if i == -1 {
			break
		}

		topicLen = i

		if ok := l.topics[topic[:topicLen]]; !ok {
			continue
		}

		return true
	}

	return false
}

func (l *logger) Print(topic string, socketId uint32, skip int, message func() string) {
	// Check if logger is closed - safe to call after Close()
	if l.closed.Load() == 1 {
		return
	}

	if !l.HasTopic(topic) {
		return
	}

	_, file, line, _ := runtime.Caller(skip)

	msg := Log{
		Time:     time.Now(),
		SocketId: socketId,
		Topic:    topic,
		Message:  message(),
		File:     file,
		Line:     line,
	}

	// Write to log queue, but don't block if it's full
	// Use recover to safely handle closed channel (race condition protection)
	func() {
		defer func() {
			// Silently recover from panic if channel is closed
			// The recover() result is intentionally unused - we just want to catch any panic
			if r := recover(); r != nil {
				// Panic caught and silently ignored - this is intentional
				// to prevent crashes when the channel is closed during shutdown
			}
		}()
		select {
		case l.logQueue <- msg:
		default:
			// Channel full - silently drop message
		}
	}()
}

func (l *logger) Listen() <-chan Log {
	return l.logQueue
}

func (l *logger) Close() {
	// Mark as closed first (atomic, prevents new messages from being queued)
	l.closed.Store(1)
	// Then close the channel (allows readers to detect closure)
	close(l.logQueue)
}

// Log represents a log message
type Log struct {
	Time     time.Time // Time of when this message has been logged
	SocketId uint32    // The socketid if connection related, 0 otherwise
	Topic    string    // The topic of this message
	Message  string    // The message itself
	File     string    // The file in which this message has been dispatched
	Line     int       // The line number in the file in which this message has been dispatched
}
