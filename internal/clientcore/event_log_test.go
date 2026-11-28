package clientcore

import (
	"context"
	"fmt"
	"testing"
)

func TestEventLogIsBoundedAndReturnsTail(t *testing.T) {
	log := newEventLog()
	for index := 0; index < EventLogCapacity+5; index++ {
		log.append("server", fmt.Sprintf("event-%d", index), 0)
	}
	events := log.after(0)
	if len(events) != EventLogCapacity {
		t.Fatalf("event count = %d, want %d", len(events), EventLogCapacity)
	}
	if events[0].Message != "event-5" {
		t.Fatalf("oldest event = %q, want event-5", events[0].Message)
	}
	tail := log.after(events[len(events)-2].Sequence)
	if len(tail) != 1 || tail[0].Message != events[len(events)-1].Message {
		t.Fatalf("tail = %#v", tail)
	}
}

func TestEventLogSubscriptionCoalesces(t *testing.T) {
	log := newEventLog()
	ctx, cancel := context.WithCancel(context.Background())
	changes, unsubscribe := log.subscribe(ctx)
	defer unsubscribe()
	<-changes
	for index := 0; index < 10; index++ {
		log.append("server", "changed", 0)
	}
	if len(changes) != 1 {
		t.Fatalf("pending changes = %d, want 1", len(changes))
	}
	cancel()
}

func TestEventLogClearRemovesEntriesAndPreservesSequence(t *testing.T) {
	log := newEventLog()
	log.append("server", "old event", 0)
	oldSequence := log.after(0)[0].Sequence

	log.clear()
	if events := log.after(0); len(events) != 0 {
		t.Fatalf("events after clear = %#v, want none", events)
	}

	log.append("connection", "new session", 0)
	events := log.after(0)
	if len(events) != 1 || events[0].Message != "new session" {
		t.Fatalf("events after new append = %#v", events)
	}
	if events[0].Sequence <= oldSequence {
		t.Fatalf("sequence after clear = %d, want greater than %d", events[0].Sequence, oldSequence)
	}
}

func TestNoticeKind(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"→ Err moved Private I → Private II", "channel"},
		{"+ Alice connected (Lobby)", "participant"},
		{"- Alice left", "participant"},
		{"snapshot refreshed", "server"},
	}
	for _, test := range tests {
		if got := noticeKind(test.line); got != test.want {
			t.Errorf("noticeKind(%q) = %q, want %q", test.line, got, test.want)
		}
	}
}
