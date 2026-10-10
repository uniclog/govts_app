package client

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

const RevisionCheckInterval = 5 * time.Second

type StateSyncer struct {
	Conn  *udp.ClientPacketConn
	State *State
}

// Initial/post-join sync retries a changing topology without replacing a healthy
// session. Network failures still return to the session supervisor.
func (syncer *StateSyncer) Load(ctx context.Context) (domain.ServerSnapshot, error) {
	attempt := 0
	for {
		attempt++
		snapshot, err := LoadServerSnapshot(ctx, syncer.Conn, syncer.State)
		if !errors.Is(err, errSnapshotRevisionChanged) {
			return snapshot, err
		}
		log.Printf("snapshot changed during load: session_id=%d attempt=%d error=%v", syncer.State.SessionID(), attempt, err)
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return domain.ServerSnapshot{}, ctx.Err()
		case <-timer.C:
		}
	}
}

// All callers, including initial load and post-join refresh, use this gate.
// A waiter reuses a successful concurrent refresh instead of loading it again.
func LoadServerSnapshot(ctx context.Context, conn *udp.ClientPacketConn, state *State) (domain.ServerSnapshot, error) {
	state.mu.RLock()
	serial, generation := state.syncSerial, state.generation
	state.mu.RUnlock()
	state.syncMu.Lock()
	defer state.syncMu.Unlock()
	if err := ctx.Err(); err != nil {
		return domain.ServerSnapshot{}, err
	}
	state.mu.RLock()
	changed := generation != state.generation
	reuse := state.syncSerial != serial && state.snapshotFresh
	state.mu.RUnlock()
	if changed {
		return domain.ServerSnapshot{}, errors.New("session changed while waiting for sync")
	}
	if reuse {
		return state.Snapshot(), nil
	}
	_, err := loadAndPublishSnapshot(ctx, conn, state)
	if err != nil {
		return domain.ServerSnapshot{}, err
	}
	state.mu.Lock()
	state.syncSerial++
	state.mu.Unlock()
	return state.Snapshot(), nil
}

func (syncer *StateSyncer) Run(ctx context.Context) error {
	return syncer.run(ctx, RevisionCheckInterval)
}

func (syncer *StateSyncer) run(ctx context.Context, interval time.Duration) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-syncer.State.resync:
		case <-ticker.C:
		}
		if err := syncer.Check(ctx); err != nil {
			if errors.Is(err, errSnapshotRevisionChanged) {
				log.Printf("snapshot changed during revision check: session_id=%d error=%v", syncer.State.SessionID(), err)
				// A busy topology is not a lost connection. Retry on next tick.
				timer := time.NewTimer(time.Second)
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				continue
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%w: state sync: %w", ErrConnectionLost, err)
		}
	}
}

func (syncer *StateSyncer) Check(ctx context.Context) error {
	state := syncer.State
	// Metadata requests also share the serialization gate.
	state.syncMu.Lock()
	metadata, err := requestSnapshot(ctx, syncer.Conn, state, protocol.SnapshotRequest{Kind: protocol.SnapshotKindMetadata})
	state.syncMu.Unlock()
	if err != nil {
		return err
	}
	if metadata.Kind != protocol.SnapshotKindMetadata || metadata.Status != protocol.SnapshotStatusOK {
		return errors.New("invalid revision check response")
	}
	state.mu.RLock()
	needed := !state.snapshotFresh || metadata.Revision > state.snapshot.Revision
	previousRevision, fresh := state.snapshot.Revision, state.snapshotFresh
	state.mu.RUnlock()
	if !needed {
		return nil
	} // A delayed metadata response must not roll back newer live events.
	started := time.Now()
	log.Printf("state resync starting: session_id=%d previous_revision=%d server_revision=%d snapshot_fresh=%t", state.SessionID(), previousRevision, metadata.Revision, fresh)
	snapshot, err := LoadServerSnapshot(ctx, syncer.Conn, state)
	log.Printf("state resync completed: session_id=%d revision=%d duration=%s error=%v", state.SessionID(), snapshot.Revision, time.Since(started), err)
	if err == nil {
		state.mu.Lock()
		state.noticeLocked(fmt.Sprintf("state resync (revision check/gap): revision=%d", snapshot.Revision))
		state.mu.Unlock()
	}
	return err
}
