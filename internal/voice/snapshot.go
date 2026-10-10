package voice

import (
	"fmt"
	"math"
	"net"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func HandleStateSnapshotPacket(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "snapshot request ID is required")
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}
	request, err := protocol.DecodeSnapshotRequest(packet.Payload)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, fmt.Sprintf("invalid snapshot request: %v", err))
	}
	snapshot := hub.ClientSnapshot()
	response, err := buildSnapshotResponse(request, snapshot)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, fmt.Sprintf("cannot create snapshot page: %v", err))
	}
	payload, err := protocol.EncodeSnapshotResponse(response)
	if err != nil {
		return fmt.Errorf("encode snapshot response: %w", err)
	}
	ack := protocol.VoicePacket{Type: protocol.PacketStateSnapshotAck, SessionID: packet.SessionID, RequestID: packet.RequestID, Payload: payload}
	// Metadata ACKs carry the server version in the otherwise unused Sequence
	// field, keeping the snapshot payload compatible with existing clients.
	if request.Kind == protocol.SnapshotKindMetadata {
		ack.Sequence = uint32(hub.ServerVersion())
	}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	session, ok := hub.Get(packet.SessionID)
	if !ok {
		return fmt.Errorf("session %d not found", packet.SessionID)
	}
	return SendToSession(conn, session, ack)
}

func buildSnapshotResponse(request protocol.SnapshotRequest, snapshot domain.ServerSnapshot) (protocol.SnapshotResponse, error) {
	response := protocol.SnapshotResponse{Kind: request.Kind, Status: protocol.SnapshotStatusOK, Revision: snapshot.Revision}
	if request.Kind == protocol.SnapshotKindMetadata {
		if uint64(len(snapshot.Channels)) > math.MaxUint32 || uint64(len(snapshot.Participants)) > math.MaxUint32 || uint64(len(snapshot.ScreenStreams)) > math.MaxUint32 {
			return response, fmt.Errorf("snapshot totals exceed uint32")
		}
		response.ServerInfo = snapshot.Info
		response.ChannelCount = uint32(len(snapshot.Channels))
		response.ParticipantCount = uint32(len(snapshot.Participants))
		response.ScreenStreamCount = uint32(len(snapshot.ScreenStreams))
		return response, nil
	}
	if request.ExpectedRevision != snapshot.Revision {
		response.Status = protocol.SnapshotStatusRevisionChanged
		return response, nil
	}
	var total int
	if request.Kind == protocol.SnapshotKindChannels {
		total = len(snapshot.Channels)
	} else if request.Kind == protocol.SnapshotKindParticipants {
		total = len(snapshot.Participants)
	} else {
		total = len(snapshot.ScreenStreams)
	}
	if uint64(request.Offset) > uint64(total) {
		return response, fmt.Errorf("offset %d exceeds total %d", request.Offset, total)
	}
	start := int(request.Offset)
	end := start
	for end < total && end-start < int(request.Limit) {
		var size int
		if request.Kind == protocol.SnapshotKindChannels {
			size = protocol.EncodedChannelSize(snapshot.Channels[end])
		} else if request.Kind == protocol.SnapshotKindParticipants {
			size = protocol.EncodedParticipantSize(snapshot.Participants[end])
		} else {
			size = protocol.EncodedScreenStreamSize(snapshot.ScreenStreams[end])
		}
		current := protocol.SnapshotResponseHeaderSize
		if request.Kind == protocol.SnapshotKindChannels {
			for _, v := range snapshot.Channels[start:end] {
				current += protocol.EncodedChannelSize(v)
			}
		} else if request.Kind == protocol.SnapshotKindParticipants {
			for _, v := range snapshot.Participants[start:end] {
				current += protocol.EncodedParticipantSize(v)
			}
		} else {
			current += (end - start) * protocol.EncodedScreenStreamSize(domain.ScreenStream{})
		}
		if current+size > protocol.MaxPayloadSize {
			if end == start {
				return response, errorsContractItemTooLarge(request.Kind)
			}
			break
		}
		end++
	}
	if request.Kind == protocol.SnapshotKindChannels {
		response.Channels = append([]domain.Channel(nil), snapshot.Channels[start:end]...)
	} else if request.Kind == protocol.SnapshotKindParticipants {
		response.Participants = append([]domain.Participant(nil), snapshot.Participants[start:end]...)
	} else {
		response.ScreenStreams = append([]domain.ScreenStream(nil), snapshot.ScreenStreams[start:end]...)
	}
	if end < total {
		response.HasMore = true
		response.NextOffset = uint32(end)
	}
	return response, nil
}

func errorsContractItemTooLarge(kind protocol.SnapshotKind) error {
	return fmt.Errorf("%v item exceeds snapshot payload budget", kind)
}
