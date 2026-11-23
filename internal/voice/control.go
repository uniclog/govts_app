package voice

import (
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func HandleHeartbeatPacket(
	conn *udp.ServerPacketConn,
	hub *Hub,
	cache *RequestCache,
	packet protocol.VoicePacket,
	addr *net.UDPAddr,
) error {
	if addr == nil {
		return fmt.Errorf("client UDP address is required")
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "heartbeat request ID is required")
	}
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrInvalidSessionAddr) {
			reason := "session_not_found"
			var expectedAddr *net.UDPAddr
			if errors.Is(err, ErrInvalidSessionAddr) {
				reason = "address_mismatch"
				if session, ok := hub.Get(packet.SessionID); ok {
					expectedAddr = session.Addr
				}
			}
			log.Printf("heartbeat rejected: session_id=%d request_id=%d reason=%s actual_addr=%s expected_addr=%v", packet.SessionID, packet.RequestID, reason, addr, expectedAddr)
			response := protocol.VoicePacket{
				Type:      protocol.PacketSessionInvalid,
				SessionID: packet.SessionID,
				RequestID: packet.RequestID,
			}
			return conn.WritePacket(packet.SessionID, addr, response)
		}
		return err
	}
	if err := protocol.ValidateEmptyLifecyclePayload(protocol.PacketHeartbeat, packet.Payload); err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}
	if err := hub.Touch(packet.SessionID); err != nil {
		return err
	}
	ack := protocol.VoicePacket{
		Type:      protocol.PacketHeartbeatAck,
		SessionID: packet.SessionID,
		RequestID: packet.RequestID,
	}
	// Match the client's 30-second sequence range with packets received here.
	sent := packet.Sequence & 0x3fff
	end := uint16(packet.Sequence >> 14)
	received := hub.VoiceReceivedWindow(packet.SessionID, time.Now(), end, uint16(sent))
	if received > sent {
		received = sent
	}
	if sent > 0 {
		ack.Sequence = uint32(uint64(sent-received) * 10000 / uint64(sent))
	}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	return conn.WritePacket(packet.SessionID, addr, ack)
}

func HandleDisconnectPacket(
	hub *Hub,
	cache *RequestCache,
	packet protocol.VoicePacket,
	addr *net.UDPAddr,
) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		// The client repeats Disconnect in case one is lost; a copy that
		// arrives after the removal finds nothing and is not an error.
		if errors.Is(err, ErrSessionNotFound) {
			return nil
		}
		return err
	}
	session, ok := hub.Get(packet.SessionID)
	if !ok {
		return nil
	}
	removed, ok := hub.Remove(session.ID)
	if !ok {
		return nil
	}
	cache.RemoveSession(session.ID)
	hub.ForgetArrival(removed.UserID, time.Now())
	log.Printf("client disconnected: id=%d, name=%s", packet.SessionID, removed.Name)
	return nil
}

func HandleJoinChannelPacket(
	conn *udp.ServerPacketConn,
	hub *Hub,
	cache *RequestCache,
	packet protocol.VoicePacket,
	addr *net.UDPAddr,
) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "join request ID is required")
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}

	channelID, expected, err := protocol.DecodeJoinChannelRequest(packet.Payload)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, fmt.Sprintf("invalid join request: %v", err))
	}
	result, err := hub.JoinChannelAtVoiceRevision(packet.SessionID, channelID, expected)
	if err != nil && !errors.Is(err, ErrStaleVoiceControl) {
		userID := int64(0)
		if session, ok := hub.Get(packet.SessionID); ok {
			userID = session.UserID
		}
		log.Printf("channel join rejected: user_id=%d session_id=%d channel_id=%d reason=%s", userID, packet.SessionID, channelID, joinRejectionReason(err))
		return cacheAndSendSessionError(conn, cache, packet, addr, fmt.Sprintf("cannot join channel: %v", err))
	}
	status := protocol.VoiceControlOK
	if errors.Is(err, ErrStaleVoiceControl) {
		status = protocol.VoiceControlStale
	}
	payload, err := protocol.EncodeVoiceControlAck(voiceCommandAck(result, status))
	if err != nil {
		return fmt.Errorf("encode join acknowledgement: %w", err)
	}
	ack := protocol.VoicePacket{
		Type:      protocol.PacketJoinChannelAck,
		SessionID: packet.SessionID,
		RequestID: packet.RequestID,
		Payload:   payload,
	}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	return SendToSession(conn, result.Session, ack)
}

func joinRejectionReason(err error) string {
	switch {
	case errors.Is(err, ErrChannelForbidden):
		return "permission_denied"
	case errors.Is(err, ErrChannelFull):
		return "channel_full"
	case errors.Is(err, ErrChannelNotFound):
		return "channel_not_found"
	default:
		return "join_failed"
	}
}

func HandleAudioStatePacket(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "audio state request ID is required")
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}
	muted, deafened, expected, err := protocol.DecodeAudioState(packet.Payload)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, fmt.Sprintf("invalid audio state: %v", err))
	}
	result, err := hub.SetAudioStateAtVoiceRevision(packet.SessionID, muted, deafened, expected)
	if err != nil && !errors.Is(err, ErrStaleVoiceControl) {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	status := protocol.VoiceControlOK
	if errors.Is(err, ErrStaleVoiceControl) {
		status = protocol.VoiceControlStale
	}
	payload, err := protocol.EncodeVoiceControlAck(voiceCommandAck(result, status))
	if err != nil {
		return err
	}
	ack := protocol.VoicePacket{
		Type:      protocol.PacketAudioStateAck,
		SessionID: packet.SessionID,
		RequestID: packet.RequestID,
		Payload:   payload,
	}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	return SendToSession(conn, result.Session, ack)
}

func voiceCommandAck(result VoiceControlResult, status byte) protocol.VoiceControlAck {
	s := result.Session
	return protocol.VoiceControlAck{Status: status, OwnControlRevision: s.OwnControlRevision, VoiceRevision: result.VoiceRevision, DomainRevision: result.DomainRevision, ChannelID: s.ChannelID, SourceEpoch: s.SourceEpoch, ReceiverEpoch: s.ReceiverEpoch, Muted: s.Muted, Deafened: s.Deafened}
}

func HandlePresencePacket(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "presence request ID is required")
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}
	away, err := protocol.DecodePresence(packet.Payload)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, fmt.Sprintf("invalid presence: %v", err))
	}
	if err := hub.SetPresence(packet.SessionID, away); err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	ack := protocol.VoicePacket{
		Type:      protocol.PacketPresenceAck,
		SessionID: packet.SessionID,
		RequestID: packet.RequestID,
		Payload:   protocol.EncodePresence(away),
	}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	session, ok := hub.Get(packet.SessionID)
	if !ok {
		return ErrSessionNotFound
	}
	return SendToSession(conn, session, ack)
}

func HandleAttentionPacket(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "attention request ID is required")
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}
	if len(packet.Payload) != 8 {
		return cacheAndSendSessionError(conn, cache, packet, addr, "invalid attention payload")
	}
	targetID := binary.BigEndian.Uint64(packet.Payload)
	now := time.Now()
	target, err := hub.AcceptAttention(packet.SessionID, targetID, now)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	if err := SendToSession(conn, target, protocol.VoicePacket{Type: protocol.PacketAttention, SessionID: target.ID}); err != nil {
		hub.ReleaseAttention(packet.SessionID)
		return err
	}
	ack := protocol.VoicePacket{Type: protocol.PacketAttentionAck, SessionID: packet.SessionID, RequestID: packet.RequestID}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	session, ok := hub.Get(packet.SessionID)
	if !ok {
		return ErrSessionNotFound
	}
	return SendToSession(conn, session, ack)
}

func HandleMediaCredentialPacket(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "media credential request ID is required")
	}
	if len(packet.Payload) != 0 {
		return cacheAndSendSessionError(conn, cache, packet, addr, "media credential request payload must be empty")
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}
	credential, err := hub.MediaCredential(packet.SessionID)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	ack := protocol.VoicePacket{Type: protocol.PacketMediaCredentialAck, SessionID: packet.SessionID, RequestID: packet.RequestID, Payload: credential[:]}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	session, ok := hub.Get(packet.SessionID)
	if !ok {
		return ErrSessionNotFound
	}
	return SendToSession(conn, session, ack)
}

func cacheAndSendSessionError(conn *udp.ServerPacketConn, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr, message string) error {
	log.Printf("session request rejected: type=%d session_id=%d request_id=%d addr=%v reason=%q", packet.Type, packet.SessionID, packet.RequestID, addr, message)
	response := protocol.NewErrorPacket(packet.SessionID, packet.RequestID, message)
	cache.Put(packet.SessionID, packet.RequestID, response)
	return conn.WritePacket(packet.SessionID, addr, response)
}

func SendError(
	conn *udp.ServerPacketConn,
	addr *net.UDPAddr,
	sessionID uint64,
	requestID uint32,
	message string,
) error {
	packet := protocol.VoicePacket{
		Type:      protocol.PacketError,
		SessionID: sessionID,
		RequestID: requestID,
		Payload:   []byte(message),
	}
	return conn.WritePacket(sessionID, addr, packet)
}
