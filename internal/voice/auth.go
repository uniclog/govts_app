package voice

import (
	"context"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"
	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/auth"
	"uniclog.io/sonoryx/internal/persist"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

type pendingAuth struct {
	init      auth.Init
	name      string // init.Name in NFKC form
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

// Handshake limits count per network, a /24 for IPv4 and a /64 for IPv6, so a
// host cannot multiply its budget by cycling addresses inside its own subnet.
const (
	maxPendingHandshakes               = 512
	maxPendingHandshakesPerNetwork     = 32
	maxNewHandshakesPerNetworkPerMin   = 60
	maxFinishesPerNetworkPerMin        = 180 // three Finish attempts per handshake
	maxNewAccountsPerNetworkPerHour    = 10
	pendingHandshakeLifetime           = 30 * time.Second
	newAccountWindow                   = time.Hour
	handshakeWindow                    = time.Minute
	authenticationServerErrorForClient = "authentication failed: server error"
)

// AuthInit.Sequence carries the requested protocol/server compatibility level,
// not the desktop executable's independently incremented release version.
const minimumSecureClientVersion appversion.Number = 4<<16 | 5 // 0.4.5: chat v4 with reactions

// Authenticator handles the signed, ephemeral-key handshake before any
// ordinary session packet reaches the existing voice packet handlers.
type Authenticator struct {
	mu          sync.Mutex
	signer      ed25519.PrivateKey
	store       *persist.Store
	codec       *protocol.SecureDatagramCodec
	pending     map[authRequestKey]*pendingAuth
	initRate    map[netip.Prefix]authRateWindow
	finishRate  map[netip.Prefix]authRateWindow
	accountRate map[netip.Prefix]authRateWindow
	adminRate   map[uint64]authRateWindow
	policyGate  *sync.Mutex
	admin       *Administration
}

// SetPolicyGate serializes UDP authorization with local account/channel changes.
// Configure it before starting ServeUDP.
func (a *Authenticator) SetPolicyGate(gate *sync.Mutex) { a.policyGate = gate }

func NewAuthenticator(signer ed25519.PrivateKey, store *persist.Store, codec *protocol.SecureDatagramCodec) (*Authenticator, error) {
	if len(signer) != ed25519.PrivateKeySize || store == nil || codec == nil {
		return nil, errors.New("authentication dependencies are required")
	}
	return &Authenticator{
		signer:      signer,
		store:       store,
		codec:       codec,
		pending:     make(map[authRequestKey]*pendingAuth),
		initRate:    make(map[netip.Prefix]authRateWindow),
		finishRate:  make(map[netip.Prefix]authRateWindow),
		accountRate: make(map[netip.Prefix]authRateWindow),
	}, nil
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
	// Compatibility forms (fullwidth letters, ligatures) fold to one spelling,
	// so they cannot imitate another participant's name.
	name := norm.NFKC.String(init.Name)
	if err := validateParticipantName(name); err != nil {
		return err
	}
	key := authRequestKey{endpoint: addr.AddrPort(), request: packet.RequestID}
	network := addressNetwork(key.endpoint.Addr())
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.pruneLocked(now)
	if previous := a.pending[key]; previous != nil {
		if previous.init != init || previous.version != packet.Sequence {
			return errors.New("authentication request ID reused with different identity")
		}
		return conn.WritePacket(0, addr, protocol.VoicePacket{Type: protocol.PacketAuthChallenge, RequestID: packet.RequestID, Sequence: packet.Sequence, Payload: previous.challenge.Encode()})
	}
	if a.pendingInNetworkLocked(network) >= maxPendingHandshakesPerNetwork {
		return errors.New("too many pending authentication handshakes")
	}
	if !allowRateLocked(a.initRate, network, now, handshakeWindow, maxNewHandshakesPerNetworkPerMin) {
		return errors.New("too many authentication attempts; retry later")
	}
	// A full table drops its oldest entry: forged requests from many networks
	// cannot keep a new, genuine handshake out.
	if len(a.pending) >= maxPendingHandshakes {
		a.evictOldestPendingLocked()
	}
	challenge, ephemeral, err := auth.NewChallenge(init, a.signer, packet.RequestID, packet.Sequence)
	if err != nil {
		return err
	}
	a.pending[key] = &pendingAuth{init: init, name: name, challenge: challenge, ephemeral: ephemeral, version: packet.Sequence, created: now}
	return conn.WritePacket(0, addr, protocol.VoicePacket{Type: protocol.PacketAuthChallenge, RequestID: packet.RequestID, Sequence: packet.Sequence, Payload: challenge.Encode()})
}

func (a *Authenticator) handleFinish(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	key := authRequestKey{endpoint: addr.AddrPort(), request: packet.RequestID}
	network := addressNetwork(key.endpoint.Addr())
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	a.pruneLocked(now)
	if !allowRateLocked(a.finishRate, network, now, handshakeWindow, maxFinishesPerNetworkPerMin) {
		return errors.New("too many authentication attempts; retry later")
	}
	pending := a.pending[key]
	if pending == nil {
		return errors.New("authentication challenge expired")
	}
	digest := auth.Transcript(pending.init, pending.challenge, packet.RequestID, pending.version)
	if err := auth.VerifyFinish(pending.init.PublicKey, digest, packet.Payload); err != nil {
		// One signature check per challenge: a repeated bad Finish finds no
		// challenge and costs no Ed25519 verification.
		delete(a.pending, key)
		return err
	}
	if pending.ack != nil {
		return conn.WritePacket(0, addr, *pending.ack)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	known, err := a.store.AccountExists(ctx, pending.init.PublicKey)
	if err != nil {
		log.Printf("authentication account lookup failed: addr=%s request_id=%d error=%v", addr, packet.RequestID, err)
		return errors.New(authenticationServerErrorForClient)
	}
	if !known && !allowRateLocked(a.accountRate, network, now, newAccountWindow, maxNewAccountsPerNetworkPerHour) {
		return errors.New("too many new accounts from this network; retry later")
	}
	account, err := a.store.FindOrCreateAccount(ctx, pending.init.PublicKey, pending.name)
	if err != nil {
		log.Printf("authentication account resolve failed: addr=%s request_id=%d error=%v", addr, packet.RequestID, err)
		return errors.New(authenticationServerErrorForClient)
	}
	if account.Banned {
		return errors.New("account is banned")
	}
	c2s, s2c, ackKey, err := auth.DeriveKeys(pending.ephemeral, pending.init.Ephemeral, digest)
	if err != nil {
		return err
	}
	session, replaced, err := hub.CreateAuthenticatedSession(pending.name, addr, account.ID, account.JoinLevel, account.Permissions, account.Owner)
	if err != nil {
		return err
	}
	for _, oldID := range replaced {
		a.codec.Remove(oldID)
		cache.RemoveSession(oldID)
	}
	if err := a.codec.Install(session.ID, c2s, s2c); err != nil {
		hub.Remove(session.ID)
		log.Printf("authentication key install failed: session_id=%d error=%v", session.ID, err)
		return errors.New(authenticationServerErrorForClient)
	}
	hub.SetSessionVoiceBundles(session.ID, appversion.Number(pending.version) >= appversion.VoiceBundlesMinimum)
	privileges := account.Privileges()
	mac := auth.AckMAC(ackKey, digest, session.ID, account.JoinLevel, privileges)
	payload := append([]byte(nil), mac[:]...)
	payload = binary.BigEndian.AppendUint16(payload, account.JoinLevel)
	payload = append(payload, privileges)
	ack := protocol.VoicePacket{Type: protocol.PacketAuthAck, SessionID: session.ID, RequestID: packet.RequestID, Sequence: pending.version, Payload: payload}
	pending.ack = &ack
	log.Printf("client authenticated: session_id=%d user_id=%d name=%q addr=%s request_id=%d replaced_sessions=%v", session.ID, account.ID, session.Name, addr, packet.RequestID, replaced)
	return conn.WritePacket(0, addr, ack)
}

func (a *Authenticator) pruneLocked(now time.Time) {
	cutoff := now.Add(-pendingHandshakeLifetime)
	for key, pending := range a.pending {
		if pending.created.Before(cutoff) {
			delete(a.pending, key)
		}
	}
	pruneRateLocked(a.initRate, now, handshakeWindow)
	pruneRateLocked(a.finishRate, now, handshakeWindow)
	pruneRateLocked(a.accountRate, now, newAccountWindow)
}

func (a *Authenticator) pendingInNetworkLocked(network netip.Prefix) int {
	count := 0
	for key := range a.pending {
		if network.Contains(key.endpoint.Addr().Unmap()) {
			count++
		}
	}
	return count
}

func (a *Authenticator) evictOldestPendingLocked() {
	var oldestKey authRequestKey
	var oldest *pendingAuth
	for key, pending := range a.pending {
		if oldest == nil || pending.created.Before(oldest.created) {
			oldestKey, oldest = key, pending
		}
	}
	if oldest != nil {
		delete(a.pending, oldestKey)
	}
}

// allowRateLocked counts one event for network and reports whether it fits
// within limit events per window.
func allowRateLocked(windows map[netip.Prefix]authRateWindow, network netip.Prefix, now time.Time, window time.Duration, limit uint16) bool {
	current := windows[network]
	if now.Sub(current.started) >= window {
		current = authRateWindow{started: now}
	}
	if current.count >= limit {
		return false
	}
	current.count++
	windows[network] = current
	return true
}

func pruneRateLocked(windows map[netip.Prefix]authRateWindow, now time.Time, window time.Duration) {
	for network, current := range windows {
		if now.Sub(current.started) >= window {
			delete(windows, network)
		}
	}
}

// addressNetwork returns the /24 (IPv4) or /64 (IPv6) network holding addr.
func addressNetwork(addr netip.Addr) netip.Prefix {
	addr = addr.Unmap()
	bits := 24
	if addr.Is6() {
		bits = 64
	}
	network, err := addr.Prefix(bits)
	if err != nil {
		return netip.PrefixFrom(addr, addr.BitLen())
	}
	return network
}
