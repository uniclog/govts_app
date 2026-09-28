package voice

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"time"

	"uniclog.io/govts/internal/appversion"
	"uniclog.io/govts/internal/auth"
	"uniclog.io/govts/internal/persist"
	"uniclog.io/govts/internal/protocol"
	"uniclog.io/govts/internal/transport/udp"
)

type pendingAuth struct {
	init      auth.Init
	challenge auth.Challenge
	ephemeral *ecdh.PrivateKey
	version   uint32
	created   time.Time
	ack       *protocol.VoicePacket
}

type authRequestKey struct {
	endpoint netip.AddrPort
	request  uint32
}

type authRateWindow struct {
	started time.Time
	count   uint16
}

const maxNewHandshakesPerIPPerMinute = 60
const minimumSecureClientVersion appversion.Number = 2<<16 | 5 // 0.2.5

// Authenticator handles the signed, ephemeral-key handshake before any
// ordinary session packet reaches the existing voice packet handlers.
type Authenticator struct {
	mu         sync.Mutex
	signer     ed25519.PrivateKey
	store      *persist.Store
	codec      *protocol.SecureDatagramCodec
	pending    map[authRequestKey]*pendingAuth
	rate       map[netip.Addr]authRateWindow
	policyGate *sync.Mutex
}

// SetPolicyGate serializes UDP authorization with local account/channel changes.
// Configure it before starting ServeUDP.
func (a *Authenticator) SetPolicyGate(gate *sync.Mutex) { a.policyGate = gate }

func NewAuthenticator(signer ed25519.PrivateKey, store *persist.Store, codec *protocol.SecureDatagramCodec) (*Authenticator, error) {
	if len(signer) != ed25519.PrivateKeySize || store == nil || codec == nil {
		return nil, errors.New("authentication dependencies are required")
	}
	return &Authenticator{signer: signer, store: store, codec: codec, pending: make(map[authRequestKey]*pendingAuth), rate: make(map[netip.Addr]authRateWindow)}, nil
}

func (a *Authenticator) Handle(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if addr == nil || packet.RequestID == 0 || packet.SessionID != 0 {
		return errors.New("invalid authentication packet header")
	}
	var err error
	switch packet.Type {
	case protocol.PacketAuthInit:
		err = a.handleInit(conn, hub, packet, addr)
	case protocol.PacketAuthFinish:
		err = a.handleFinish(conn, hub, cache, packet, addr)
	default:
		err = errors.New("unexpected authentication packet")
	}
	if err != nil {
		if sendErr := conn.WritePacket(0, addr, protocol.NewErrorPacket(0, packet.RequestID, err.Error())); sendErr != nil {
			return errors.Join(err, sendErr)
		}
	}
	return err
}

func (a *Authenticator) handleInit(conn *udp.ServerPacketConn, hub *Hub, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if appversion.Number(packet.Sequence) < minimumSecureClientVersion {
		return fmt.Errorf("client upgrade required: minimum version %s", minimumSecureClientVersion)
	}
	if uint32(hub.ServerVersion()) < packet.Sequence {
		return conn.WritePacket(0, addr, protocol.VoicePacket{Type: protocol.PacketServerVersionTooOld, RequestID: packet.RequestID, Sequence: uint32(hub.ServerVersion())})
	}
	init, err := auth.DecodeInit(packet.Payload)
	if err != nil {
		return err
	}
	if err := validateParticipantName(init.Name); err != nil {
		return err
	}
	key := authRequestKey{endpoint: addr.AddrPort(), request: packet.RequestID}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	if previous := a.pending[key]; previous != nil {
		if previous.init != init || previous.version != packet.Sequence {
			return errors.New("authentication request ID reused with different identity")
		}
		return conn.WritePacket(0, addr, protocol.VoicePacket{Type: protocol.PacketAuthChallenge, RequestID: packet.RequestID, Sequence: packet.Sequence, Payload: previous.challenge.Encode()})
	}
	if len(a.pending) >= 512 {
		return errors.New("too many pending authentication handshakes")
	}
	ip := addr.AddrPort().Addr().Unmap()
	now := time.Now()
	window := a.rate[ip]
	if now.Sub(window.started) >= time.Minute {
		window = authRateWindow{started: now}
	}
	if window.count >= maxNewHandshakesPerIPPerMinute {
		return errors.New("too many authentication attempts; retry later")
	}
	challenge, ephemeral, err := auth.NewChallenge(init, a.signer, packet.RequestID, packet.Sequence)
	if err != nil {
		return err
	}
	window.count++
	a.rate[ip] = window
	a.pending[key] = &pendingAuth{init: init, challenge: challenge, ephemeral: ephemeral, version: packet.Sequence, created: time.Now()}
	return conn.WritePacket(0, addr, protocol.VoicePacket{Type: protocol.PacketAuthChallenge, RequestID: packet.RequestID, Sequence: packet.Sequence, Payload: challenge.Encode()})
}

func (a *Authenticator) handleFinish(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	key := authRequestKey{endpoint: addr.AddrPort(), request: packet.RequestID}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.pruneLocked()
	pending := a.pending[key]
	if pending == nil {
		return errors.New("authentication challenge expired")
	}
	digest := auth.Transcript(pending.init, pending.challenge, packet.RequestID, pending.version)
	if err := auth.VerifyFinish(pending.init.PublicKey, digest, packet.Payload); err != nil {
		return err
	}
	if pending.ack != nil {
		return conn.WritePacket(0, addr, *pending.ack)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	account, err := a.store.FindOrCreateAccount(ctx, pending.init.PublicKey, pending.init.Name)
	if err != nil {
		return fmt.Errorf("resolve account: %w", err)
	}
	if account.Banned {
		return errors.New("account is banned")
	}
	c2s, s2c, ackKey, err := auth.DeriveKeys(pending.ephemeral, pending.init.Ephemeral, digest)
	if err != nil {
		return err
	}
	session, replaced, err := hub.CreateAuthenticatedSession(pending.init.Name, addr, account.ID, account.JoinLevel, account.Permissions, account.Owner)
	if err != nil {
		return err
	}
	for _, oldID := range replaced {
		a.codec.Remove(oldID)
		cache.RemoveSession(oldID)
	}
	if err := a.codec.Install(session.ID, c2s, s2c); err != nil {
		hub.Remove(session.ID)
		return err
	}
	mac := auth.AckMAC(ackKey, digest, session.ID, account.JoinLevel, account.Permissions)
	payload := append([]byte(nil), mac[:]...)
	payload = binary.BigEndian.AppendUint16(payload, account.JoinLevel)
	payload = append(payload, account.Permissions)
	ack := protocol.VoicePacket{Type: protocol.PacketAuthAck, SessionID: session.ID, RequestID: packet.RequestID, Sequence: pending.version, Payload: payload}
	pending.ack = &ack
	return conn.WritePacket(0, addr, ack)
}

func (a *Authenticator) pruneLocked() {
	now := time.Now()
	cutoff := now.Add(-30 * time.Second)
	for key, pending := range a.pending {
		if pending.created.Before(cutoff) {
			delete(a.pending, key)
		}
	}
	for ip, window := range a.rate {
		if now.Sub(window.started) >= time.Minute {
			delete(a.rate, ip)
		}
	}
}
