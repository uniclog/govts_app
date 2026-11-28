package client

import (
	"context"
	"errors"
	"net"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/logging"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

const receivePollInterval = 500 * time.Millisecond

func ReceiveLoop(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	encodedCh chan<- audio.MediaFrame,
	controlCh chan<- protocol.VoicePacket,
	states ...*State,
) error {
	defer close(encodedCh)
	defer close(controlCh)
	rejected := logging.NewFailures("client_udp_decode")
	defer rejected.Close()

	var state *State
	var generation uint64
	if len(states) > 0 {
		state = states[0]
		if state != nil {
			generation, _ = state.SessionIdentity()
		}
	}
	for {
		if err := conn.SetReadDeadline(time.Now().Add(receivePollInterval)); err != nil {
			return err
		}

		packet, err := conn.ReceivePacket()
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				select {
				case <-ctx.Done():
					return ctx.Err()
				default:
					continue
				}
			}
			if errors.Is(err, protocol.ErrRejectedDatagram) {
				if state != nil {
					state.RecordVoiceProtocolDrop(generation)
				}
				rejected.RecordKind(protocol.DatagramFailureReason(err), err, conn.RemoteAddr())
				continue
			}
			return err
		}

		switch packet.Type {
		case protocol.PacketVoice, protocol.PacketVoiceBundle:
			// Reserved v1 IDs are never interpreted as audio.
			continue
		case protocol.PacketVoiceBundleV2:
			bundle, err := protocol.DecodeVoiceBundleV2Packet(packet, false)
			if err != nil {
				if state != nil {
					state.RecordVoiceProtocolDrop(generation)
				}
				rejected.RecordKind("malformed_packet", err, conn.RemoteAddr())
				continue
			}
			if state == nil {
				continue
			}
			current, _ := state.SessionIdentity()
			if current != generation {
				continue
			}
			// One transport stream spans all speakers; frame sequences are independent.
			state.RecordVoiceDatagram(generation, bundle.DatagramSequence, byte(len(bundle.Frames)))
			if !state.VoiceBundleAllowed(generation, packet.SessionID, bundle) {
				state.RequestVoiceResync(generation)
				continue
			}
			identity := audio.VoiceIdentity{Generation: generation, SourceEpoch: bundle.SourceEpoch, ReceiverEpoch: bundle.ReceiverEpoch}
			batch := make([]audio.MediaFrame, 0, len(bundle.Frames))
			arrival := time.Now()
			for i, f := range bundle.Frames {
				batch = append(batch, audio.MediaFrame{DatagramSequence: bundle.DatagramSequence, ArrivedAt: arrival, Redundant: i < len(bundle.Frames)-1, Voice: identity, SenderID: packet.SessionID, Sequence: f.Sequence, Data: f.Payload, Duration: frameDuration})
			}
			frame := batch[0]
			if len(batch) > 1 {
				frame = audio.MediaFrame{Batch: batch}
			}
			if err := sendMediaFrame(ctx, encodedCh, frame); err != nil {
				return err
			}

		default:
			select {
			case controlCh <- packet:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func sendMediaFrame(
	ctx context.Context,
	encodedCh chan<- audio.MediaFrame,
	frame audio.MediaFrame,
) error {
	select {
	case encodedCh <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
