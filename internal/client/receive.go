package client

import (
	"context"
	"errors"
	"fmt"
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
				rejected.RecordKind(protocol.DatagramFailureReason(err), err, conn.RemoteAddr())
				continue
			}
			return err
		}

		switch packet.Type {
		case protocol.PacketVoice:
			if len(states) > 0 && states[0] != nil {
				states[0].RecordVoiceArrival(packet.SessionID, packet.Sequence)
			}
			frame := audio.MediaFrame{
				SenderID: packet.SessionID,
				Sequence: packet.Sequence,
				Data:     packet.Payload,
				Duration: frameDuration,
			}
			if err := sendMediaFrame(ctx, encodedCh, frame); err != nil {
				return err
			}
		case protocol.PacketVoiceBundle:
			frames, err := protocol.DecodeVoiceBundle(packet.Payload)
			if err == nil && frames[len(frames)-1].Sequence != packet.Sequence {
				err = fmt.Errorf("%w: current frame %d does not match header %d", protocol.ErrInvalidVoiceBundle, frames[len(frames)-1].Sequence, packet.Sequence)
			}
			if err != nil {
				rejected.RecordKind("malformed_packet", err, conn.RemoteAddr())
				continue
			}
			if len(states) > 0 && states[0] != nil {
				// Raw loss counts datagrams, so only the current frame is an
				// arrival; earlier frames are redundant copies.
				states[0].RecordVoiceArrival(packet.SessionID, packet.Sequence)
				for _, redundant := range frames[:len(frames)-1] {
					states[0].RecordVoiceRedundant(packet.SessionID, redundant.Sequence)
				}
			}
			for _, bundled := range frames {
				frame := audio.MediaFrame{
					SenderID: packet.SessionID,
					Sequence: bundled.Sequence,
					Data:     bundled.Payload,
					Duration: frameDuration,
				}
				if err := sendMediaFrame(ctx, encodedCh, frame); err != nil {
					return err
				}
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
