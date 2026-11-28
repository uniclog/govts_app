package voice

import (
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

func voiceDeliveryBytes(deliveries []VoiceStateDelivery) int {
	size := 0
	for _, delivery := range deliveries {
		for _, p := range delivery.Packets {
			size += len(p.Payload) + voiceStatePacketOverhead
		}
	}
	return size
}

func budgetVoiceHub(t *testing.T, recipients int, now time.Time) *Hub {
	t.Helper()
	h := NewHub()
	for id := uint64(1); id <= 1000; id++ {
		h.Add(&Session{ID: id, ChannelID: DefaultChannelID})
	}
	for id := uint64(1); id <= uint64(recipients); id++ {
		if err := h.RequestVoiceState(id, now); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

func TestVoiceStateGlobalBudgetAndFairInitialDelivery(t *testing.T) {
	now := time.Unix(100, 0)
	h := budgetVoiceHub(t, 64, now)
	seen := make(map[uint64]bool)
	total := 0
	for tick := 0; tick <= 5; tick++ {
		deliveries := h.DueVoiceStates(now.Add(time.Duration(tick) * 50 * time.Millisecond))
		total += voiceDeliveryBytes(deliveries)
		maximum := voiceStateGlobalBurstBytes + voiceStateGlobalBytesPerSecond*tick/20
		if total > maximum {
			t.Fatalf("global snapshot budget exceeded: %d > %d", total, maximum)
		}
		for _, delivery := range deliveries {
			if seen[delivery.Session.ID] {
				t.Fatal("retry overtook recipient waiting for first state")
			}
			seen[delivery.Session.ID] = true
			page, err := protocol.DecodeVoiceState(delivery.Packets[0].Payload)
			if err != nil || !h.AckVoiceState(delivery.Session.ID, page.Revision) {
				t.Fatalf("ack: %v", err)
			}
		}
	}
	if len(seen) != 64 {
		t.Fatalf("recipients starved: delivered=%d", len(seen))
	}
}

func TestVoiceStateRecipientBudgetBoundsMuteChurn(t *testing.T) {
	now := time.Unix(100, 0)
	h := budgetVoiceHub(t, 1, now)
	total := 0
	for tick := 0; tick < 100; tick++ {
		if err := h.SetAudioState(1000, tick%2 == 0, false); err != nil {
			t.Fatal(err)
		}
		deliveries := h.DueVoiceStates(now.Add(time.Duration(tick) * 10 * time.Millisecond))
		total += voiceDeliveryBytes(deliveries)
		maximum := voiceStateRecipientBurstBytes + voiceStateRecipientBytesPerSecond*tick/100
		if total > maximum {
			t.Fatalf("recipient snapshot budget exceeded: %d > %d", total, maximum)
		}
	}
	if total == 0 {
		t.Fatal("budget blocked all delivery")
	}
	// The next state carries the latest epoch after delayed/coalesced updates.
	deliveries := h.DueVoiceStates(now.Add(3 * time.Second))
	if len(deliveries) != 1 {
		t.Fatal("state did not resume after budget refill")
	}
	var a protocol.VoiceStateAssembler
	var snapshot *protocol.VoiceStateSnapshot
	for _, packet := range deliveries[0].Packets {
		page, err := protocol.DecodeVoiceState(packet.Payload)
		if err != nil {
			t.Fatal(err)
		}
		ready, _, err := a.Add(page, 0, now.Add(3*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if ready != nil {
			snapshot = ready
		}
	}
	if snapshot == nil {
		t.Fatal("budget split snapshot into incomplete delivery")
	}
	current, _ := h.Get(1000)
	for _, source := range snapshot.Sources {
		if source.SenderID == 1000 && source.SourceEpoch != current.SourceEpoch {
			t.Fatal("budget delay sent obsolete epoch")
		}
	}
}

func TestVoiceStateLargestSnapshotFitsBurstAndWireAccounting(t *testing.T) {
	now := time.Unix(100, 0)
	h := NewHub()
	for id := uint64(1); id <= protocol.MaxVoiceSources; id++ {
		h.Add(&Session{ID: id, ChannelID: DefaultChannelID})
	}
	if err := h.RequestVoiceState(1, now); err != nil {
		t.Fatal(err)
	}
	deliveries := h.DueVoiceStates(now)
	if len(deliveries) != 1 || len(deliveries[0].Packets) != protocol.MaxVoiceStatePages {
		t.Fatal("largest snapshot starved")
	}
	if got, want := voiceDeliveryBytes(deliveries), voiceStateSnapshotWireSize(protocol.MaxVoiceSources); got != want || got > voiceStateRecipientBurstBytes {
		t.Fatalf("wire accounting: got=%d want=%d", got, want)
	}
}

func TestVoiceStateBudgetDoesNotRefillFromBackwardsClock(t *testing.T) {
	now := time.Unix(100, 0)
	var b voiceStateByteBudget
	if !b.canSpend(now, 100, 100, 100) {
		t.Fatal("initial burst")
	}
	b.spend(100)
	if b.canSpend(now.Add(-time.Second), 1, 100, 100) {
		t.Fatal("backwards time refilled budget")
	}
	if !b.canSpend(now.Add(time.Second), 100, 100, 100) {
		t.Fatal("normal refill")
	}
}
