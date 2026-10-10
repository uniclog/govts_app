package voice

import (
	"context"
	"log"
	"net"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

const EventOutboxCapacity = 256

// Called under h.mu. Drop newest on overflow: revision checks recover the gap.
func (h *Hub) emitLocked(e domain.StateEvent) {
	e.Revision = h.revision
	if len(h.outbox) < EventOutboxCapacity {
		h.outbox = append(h.outbox, e)
	}
	select {
	case h.eventReady <- struct{}{}:
	default:
	}
}

func (h *Hub) takeEvents() []domain.StateEvent {
	h.mu.Lock()
	defer h.mu.Unlock()
	events := h.outbox
	h.outbox = nil
	return events
}

type EventWriter interface {
	WritePacket(uint64, *net.UDPAddr, protocol.VoicePacket) error
}

// One dispatcher per server. Socket writes have bounded deadlines in transport.
func DispatchEvents(ctx context.Context, hub *Hub, writer EventWriter) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-hub.eventReady:
			for _, e := range hub.takeEvents() {
				payload, err := protocol.EncodeStateEvent(e)
				if err != nil {
					return err
				}
				for _, session := range hub.Inspect().Sessions {
					if err := ctx.Err(); err != nil {
						return err
					}
					p := protocol.VoicePacket{Type: protocol.PacketStateEvent, SessionID: session.ID, Payload: payload}
					if err := writer.WritePacket(session.ID, session.Addr, p); err != nil {
						log.Printf("state event delivery: %v", err)
					}
				}
			}
		}
	}
}
