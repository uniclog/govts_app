package voice

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/persist"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func (a *Authenticator) HandleModeration(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "moderation request ID is required")
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}
	actor, ok := hub.Get(packet.SessionID)
	if !ok || actor.UserID <= 0 {
		return cacheAndSendSessionError(conn, cache, packet, addr, ErrPermissionDenied.Error())
	}
	if (packet.Type == protocol.PacketDrag && len(packet.Payload) != 16) || (packet.Type != protocol.PacketDrag && len(packet.Payload) != 8) {
		return cacheAndSendSessionError(conn, cache, packet, addr, "invalid moderation request")
	}
	targetID := binary.BigEndian.Uint64(packet.Payload[:8])
	var required uint8
	switch packet.Type {
	case protocol.PacketKick:
		required = persist.PermissionKick
	case protocol.PacketBan:
		required = persist.PermissionBan
	case protocol.PacketDrag:
		required = persist.PermissionDrag
	default:
		return cacheAndSendSessionError(conn, cache, packet, addr, "unknown moderation action")
	}
	target, err := hub.CheckModeration(actor.ID, targetID, required)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	switch packet.Type {
	case protocol.PacketKick:
		if err := a.store.LogAction(ctx, fmt.Sprint(actor.UserID), "kick", target.UserID, fmt.Sprint(targetID)); err != nil {
			return cacheAndSendSessionError(conn, cache, packet, addr, "moderation storage unavailable")
		}
		if _, err := hub.Kick(actor.ID, targetID); err != nil {
			return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
		}
		a.codec.Remove(targetID)
		cache.RemoveSession(targetID)
	case protocol.PacketBan:
		if err := a.store.SetBan(ctx, fmt.Sprint(actor.UserID), target.UserID, true, "moderator action"); err != nil {
			return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
		}
		for _, id := range hub.RemoveUserSessions(target.UserID) {
			a.codec.Remove(id)
			cache.RemoveSession(id)
		}
	case protocol.PacketDrag:
		channelID := domain.ChannelID(binary.BigEndian.Uint64(packet.Payload[8:16]))
		if _, err := hub.CheckDrag(actor.ID, targetID, channelID); err != nil {
			return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
		}
		if err := a.store.LogAction(ctx, fmt.Sprint(actor.UserID), "drag", target.UserID, fmt.Sprintf("session=%d channel=%d", targetID, channelID)); err != nil {
			return cacheAndSendSessionError(conn, cache, packet, addr, "moderation storage unavailable")
		}
		if _, err := hub.Drag(actor.ID, targetID, channelID); err != nil {
			return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
		}
	}
	ack := protocol.VoicePacket{Type: protocol.PacketModerationAck, SessionID: actor.ID, RequestID: packet.RequestID}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	return conn.WritePacket(actor.ID, addr, ack)
}
