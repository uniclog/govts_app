package client

import (
	"uniclog.io/sonoryx/internal/domain"
	"testing"
	"time"
)

func TestConnectionMeasurementsVoiceLossAndLateArrival(t *testing.T) {
	var m connectionMeasurements
	now := time.Now()
	if m.snapshot(now).IncomingKnown {
		t.Fatal("idle stream reported zero loss")
	}
	m.recordVoice(10, 1, now)
	m.recordVoice(10, 3, now.Add(time.Millisecond))
	if got := m.snapshot(now.Add(time.Second)); !got.IncomingKnown || got.IncomingLoss != 100.0/3.0 {
		t.Fatalf("gap stats = %+v", got)
	}
	m.recordVoice(10, 2, now.Add(2*time.Millisecond))
	m.recordVoice(10, 2, now.Add(3*time.Millisecond))
	if got := m.snapshot(now.Add(time.Second)); got.IncomingLoss != 0 {
		t.Fatalf("late and duplicate stats = %+v", got)
	}
	m.recordVoice(20, 100, now.Add(4*time.Millisecond))
	if got := m.snapshot(now.Add(statsWindow + time.Second)); got.IncomingKnown {
		t.Fatalf("stale stats = %+v", got)
	}
}

func TestConnectionMeasurementsPingAndReset(t *testing.T) {
	var m connectionMeasurements
	now := time.Now()
	m.recordPing(12*time.Millisecond, now)
	if got := m.snapshot(now); !got.PingAvailable || got.PingVariationAvailable || got.PingMS != 12 {
		t.Fatalf("first ping = %+v", got)
	}
	m.recordPing(16*time.Millisecond, now.Add(time.Second))
	if got := m.snapshot(now.Add(time.Second)); !got.PingVariationAvailable || got.PingVariationMS != 4 {
		t.Fatalf("ping variation = %+v", got)
	}
	if got := m.snapshot(now.Add(2*HeartbeatInterval + 2*time.Second)); got.PingAvailable {
		t.Fatalf("stale ping = %+v", got)
	}
	m.reset()
	if got := m.snapshot(now); got.PingAvailable || got.IncomingKnown {
		t.Fatalf("reset = %+v", got)
	}
}

func TestVoiceSentWindowExpiresOldPackets(t *testing.T) {
	var m connectionMeasurements
	now := time.Now()
	m.recordVoiceSent(1, now.Add(-31*time.Second))
	m.recordVoiceSent(2, now.Add(-29*time.Second))
	m.recordVoiceSent(3, now)
	if count, end := m.sentInWindow(now); count != 2 || end != 3 {
		t.Fatalf("sent in window = %d ending at %d, want 2 ending at 3", count, end)
	}
	if count, _ := m.sentInWindow(now.Add(31 * time.Second)); count != 0 {
		t.Fatalf("expired window = %d", count)
	}
}

func TestIncomingSequenceBaselineResetsOnChannelChange(t *testing.T) {
	s := NewState(1, "alice")
	s.SetConnectionStatus(ConnectionConnected)
	s.SetChannelID(1)
	s.RecordVoiceArrival(2, 1)
	s.SetChannelID(2)
	s.RecordVoiceArrival(2, 100)
	if got := s.ConnectionStats(); !got.IncomingKnown || got.IncomingLoss != 0 {
		t.Fatalf("channel change counted unobserved packets as loss: %+v", got)
	}
}

func TestIncomingSequenceBaselineResetsOnSenderMove(t *testing.T) {
	s := liveTestState()
	s.SetConnectionStatus(ConnectionConnected)
	s.RecordVoiceArrival(2, 1)
	if !s.ApplyEvent(s.Generation(), domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 2, ChannelID: 2}) {
		t.Fatal("sender move was not applied")
	}
	if !s.ApplyEvent(s.Generation(), domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 12, SessionID: 2, ChannelID: 1}) {
		t.Fatal("sender return was not applied")
	}
	s.RecordVoiceArrival(2, 100)
	if got := s.ConnectionStats(); !got.IncomingKnown || got.IncomingLoss != 0 {
		t.Fatalf("sender move counted unobserved packets as loss: %+v", got)
	}
}
