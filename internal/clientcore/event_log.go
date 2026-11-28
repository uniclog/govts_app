package clientcore

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"
)

const EventLogCapacity = 200

type ClientEvent struct {
	Sequence uint64
	Time     time.Time
	Kind     string
	Message  string
	Revision uint64
}

type EventLog struct {
	mu          sync.Mutex
	next        uint64
	entries     []ClientEvent
	subscribers map[chan struct{}]struct{}
	logger      *log.Logger
}

func newEventLog() *EventLog {
	return &EventLog{subscribers: make(map[chan struct{}]struct{})}
}

func (l *EventLog) append(kind, message string, revision uint64) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	if l.logger != nil {
		l.logger.Printf("client event: kind=%s revision=%d message=%q", kind, revision, message)
	}
	l.mu.Lock()
	l.next++
	event := ClientEvent{Sequence: l.next, Time: time.Now(), Kind: kind, Message: message, Revision: revision}
	if len(l.entries) == EventLogCapacity {
		copy(l.entries, l.entries[1:])
		l.entries[len(l.entries)-1] = event
	} else {
		l.entries = append(l.entries, event)
	}
	for subscriber := range l.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
	l.mu.Unlock()
}

func (l *EventLog) clear() {
	l.mu.Lock()
	l.entries = nil
	l.mu.Unlock()
}

func (l *EventLog) after(sequence uint64) []ClientEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	result := make([]ClientEvent, 0, len(l.entries))
	for _, event := range l.entries {
		if event.Sequence > sequence {
			result = append(result, event)
		}
	}
	return result
}

func (l *EventLog) subscribe(ctx context.Context) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	l.mu.Lock()
	l.subscribers[ch] = struct{}{}
	ch <- struct{}{}
	l.mu.Unlock()
	unsubscribe := func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if _, ok := l.subscribers[ch]; ok {
			delete(l.subscribers, ch)
			close(ch)
		}
	}
	stop := context.AfterFunc(ctx, unsubscribe)
	return ch, func() { stop(); unsubscribe() }
}

type eventWriter struct {
	log *EventLog
}

func (w eventWriter) Write(data []byte) (int, error) {
	for line := range strings.Lines(string(data)) {
		w.log.append(noticeKind(line), line, 0)
	}
	return len(data), nil
}

func noticeKind(line string) string {
	line = strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(line, "→ ") && strings.Contains(line, " moved "):
		return "channel"
	case strings.HasPrefix(line, "+ "), strings.HasPrefix(line, "- "):
		return "participant"
	default:
		return "server"
	}
}
