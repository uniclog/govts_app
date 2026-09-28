package voice

import (
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"uniclog.io/govts/internal/domain"
	"uniclog.io/govts/internal/protocol"
	"uniclog.io/govts/internal/transport/udp"
)

func HandleHelloPacket(
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
		return SendError(conn, addr, 0, 0, "handshake request ID is required")
	}

	endpoint := addr.AddrPort()
	if response, ok := cache.GetHandshake(endpoint, packet.RequestID); ok {
		return conn.WritePacket(response.SessionID, addr, response)
	}
	serverVersion := hub.ServerVersion()
	if uint32(serverVersion) < packet.Sequence {
		response := protocol.VoicePacket{
			Type:      protocol.PacketServerVersionTooOld,
			RequestID: packet.RequestID,
			Sequence:  uint32(serverVersion),
		}
		cache.PutHandshake(endpoint, packet.RequestID, response)
		return conn.WritePacket(0, addr, response)
	}

	if len(packet.Payload) == 0 {
		return cacheAndSendHandshakeError(
			conn,
			cache,
			packet.RequestID,
			addr,
			"client name is required",
		)
	}
	if len(packet.Payload) > domain.MaxParticipantNameBytes {
		return cacheAndSendHandshakeError(
			conn,
			cache,
			packet.RequestID,
			addr,
			fmt.Sprintf("client name too long: %d bytes", len(packet.Payload)),
		)
	}

	name := string(packet.Payload)
	if err := validateParticipantName(name); err != nil {
		return cacheAndSendHandshakeError(conn, cache, packet.RequestID, addr, err.Error())
	}
	session, replaced, err := hub.CreateSessionReplacingEndpoint(name, addr)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	for _, replacedID := range replaced {
		cache.RemoveSession(replacedID)
	}
	if len(replaced) > 0 {
		cache.RemoveHandshakeEndpoint(endpoint)
	}
	ack := protocol.VoicePacket{
		Type:      protocol.PacketHelloAck,
		SessionID: session.ID,
		RequestID: packet.RequestID,
		Sequence:  uint32(serverVersion),
	}
	cache.PutHandshake(endpoint, packet.RequestID, ack)
	if err := SendToSession(conn, session, ack); err != nil {
		return err
	}

	log.Printf(
		"client connected: id=%d name=%q addr=%s",
		session.ID,
		session.Name,
		session.Addr,
	)
	return nil
}

func cacheAndSendHandshakeError(
	conn *udp.ServerPacketConn,
	cache *RequestCache,
	requestID uint32,
	addr *net.UDPAddr,
	message string,
) error {
	response := protocol.NewErrorPacket(0, requestID, message)
	cache.PutHandshake(addr.AddrPort(), requestID, response)
	return conn.WritePacket(0, addr, response)
}

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
		return err
	}
	session, ok := hub.Get(packet.SessionID)
	if !ok {
		return fmt.Errorf("session %d not found", packet.SessionID)
	}
	removed, ok := hub.Remove(session.ID)
	if !ok {
		return fmt.Errorf("session %d not found", packet.SessionID)
	}
	cache.RemoveSession(session.ID)
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

	channelID, err := protocol.DecodeJoinChannelRequest(packet.Payload)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, fmt.Sprintf("invalid join request: %v", err))
	}
	revision, err := hub.JoinChannelWithRevision(packet.SessionID, channelID)
	if err != nil {
		userID := int64(0)
		if session, ok := hub.Get(packet.SessionID); ok {
			userID = session.UserID
		}
		log.Printf("channel join rejected: user_id=%d session_id=%d channel_id=%d reason=%s", userID, packet.SessionID, channelID, joinRejectionReason(err))
		return cacheAndSendSessionError(conn, cache, packet, addr, fmt.Sprintf("cannot join channel: %v", err))
	}
	session, ok := hub.Get(packet.SessionID)
	if !ok {
		return fmt.Errorf("session %d not found", packet.SessionID)
	}
	payload, err := protocol.EncodeJoinChannelAck(channelID, revision)
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
	return SendToSession(conn, session, ack)
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
	muted, deafened, err := protocol.DecodeAudioState(packet.Payload)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, fmt.Sprintf("invalid audio state: %v", err))
	}
	if err := hub.SetAudioState(packet.SessionID, muted, deafened); err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	ack := protocol.VoicePacket{
		Type:      protocol.PacketAudioStateAck,
		SessionID: packet.SessionID,
		RequestID: packet.RequestID,
		Payload:   protocol.EncodeAudioState(muted, deafened),
	}
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
