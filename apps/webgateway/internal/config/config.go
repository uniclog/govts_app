package config

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pion/stun/v4"
	"github.com/pion/webrtc/v4"
	"uniclog.io/sonoryx/internal/auth"
	"uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/identity"
)

type Config struct {
	LoginMode             string             `json:"loginMode"`
	Listen                string             `json:"listen"`
	Origin                string             `json:"origin"`
	TLSCertificate        string             `json:"tlsCertificate"`
	TLSKey                string             `json:"tlsKey"`
	WebRoot               string             `json:"webRoot"`
	StateDir              string             `json:"stateDir"`
	Server                string             `json:"server"`
	ServerKey             string             `json:"serverKey"`
	MediaFingerprint      string             `json:"mediaFingerprint"`
	ServerPasswordEnv     string             `json:"serverPasswordEnv"`
	SessionTTLSeconds     int                `json:"sessionTTLSeconds"`
	ReconnectGraceSeconds int                `json:"reconnectGraceSeconds"`
	MaxSessions           int                `json:"maxSessions"`
	ICE                   []webrtc.ICEServer `json:"iceServers"`
	ICEPortMin            uint16             `json:"icePortMin"`
	ICEPortMax            uint16             `json:"icePortMax"`

	Endpoint       string            `json:"-"`
	Password       string            `json:"-"`
	SessionTTL     time.Duration     `json:"-"`
	ReconnectGrace time.Duration     `json:"-"`
	Pin            ed25519.PublicKey `json:"-"`
}

func Load(path string) (Config, error) {
	file, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer file.Close()
	c := Config{LoginMode: "invite", Listen: "127.0.0.1:8443", SessionTTLSeconds: 3600, ReconnectGraceSeconds: 30, MaxSessions: 20}
	if err := decode(file, &c); err != nil {
		return c, err
	}
	if c.LoginMode != "invite" && c.LoginMode != "nickname" {
		return c, fmt.Errorf("loginMode must be invite or nickname")
	}
	origin, err := url.Parse(c.Origin)
	if err != nil || origin.Scheme != "https" || origin.Hostname() == "" || origin.User != nil || c.Origin != "https://"+origin.Host || c.Origin != strings.ToLower(c.Origin) || origin.Port() == "443" {
		return c, fmt.Errorf("origin must be the exact browser HTTPS origin, without a path or default port")
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return c, fmt.Errorf("invalid listen address")
	}
	if c.StateDir == "" || c.WebRoot == "" || c.TLSCertificate == "" || c.TLSKey == "" {
		return c, fmt.Errorf("stateDir, webRoot and both TLS files are required")
	}
	endpoint, err := clientcore.ParseServerEndpoint(c.Server)
	if err != nil {
		return c, fmt.Errorf("resolve configured Sonoryx server: %w", err)
	}
	c.Endpoint = endpoint.String()
	c.Pin, err = base64.StdEncoding.DecodeString(c.ServerKey)
	if err != nil || len(c.Pin) != ed25519.PublicKeySize {
		return c, fmt.Errorf("serverKey must contain a base64 Ed25519 public key")
	}
	if c.MediaFingerprint != "" {
		c.MediaFingerprint = identity.NormalizeTLSFingerprint(c.MediaFingerprint)
		parts := strings.Split(c.MediaFingerprint, ":")
		decoded, decodeErr := hex.DecodeString(strings.Join(parts, ""))
		if len(parts) != 32 || decodeErr != nil || len(decoded) != 32 {
			return c, fmt.Errorf("mediaFingerprint must be a colon-delimited SHA-256 fingerprint")
		}
		for _, part := range parts {
			if len(part) != 2 {
				return c, fmt.Errorf("invalid mediaFingerprint byte")
			}
		}
	}
	if c.ServerPasswordEnv != "" {
		value, found := os.LookupEnv(c.ServerPasswordEnv)
		if !found || value == "" || auth.ValidatePassword(value) != nil {
			return c, fmt.Errorf("invalid or missing Sonoryx password environment variable")
		}
		c.Password = value
	}
	if c.SessionTTLSeconds < 60 || c.SessionTTLSeconds > 86400 || c.ReconnectGraceSeconds < 5 || c.ReconnectGraceSeconds > 120 || c.ReconnectGraceSeconds >= c.SessionTTLSeconds || c.MaxSessions < 1 || c.MaxSessions > 100 {
		return c, fmt.Errorf("invalid session lifetime, reconnect grace or concurrent session limit")
	}
	c.SessionTTL = time.Duration(c.SessionTTLSeconds) * time.Second
	c.ReconnectGrace = time.Duration(c.ReconnectGraceSeconds) * time.Second
	if (c.ICEPortMin == 0) != (c.ICEPortMax == 0) || c.ICEPortMin > c.ICEPortMax {
		return c, fmt.Errorf("set both icePortMin and icePortMax to a valid UDP range, or leave both zero")
	}
	for _, server := range c.ICE {
		if len(server.URLs) == 0 {
			return c, fmt.Errorf("ICE servers require at least one URL")
		}
		for _, raw := range server.URLs {
			u, err := stun.ParseURI(raw)
			if err != nil {
				return c, fmt.Errorf("invalid ICE server URL")
			}
			if u.Scheme == stun.SchemeTypeTURN || u.Scheme == stun.SchemeTypeTURNS {
				credential, ok := server.Credential.(string)
				if server.Username == "" || server.CredentialType != webrtc.ICECredentialTypePassword || !ok || credential == "" {
					return c, fmt.Errorf("TURN requires a username and password")
				}
			}
		}
	}
	return c, nil
}

func decode(file *os.File, value any) error {
	d := json.NewDecoder(file)
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		return fmt.Errorf("decode gateway config: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("gateway config must contain exactly one JSON object")
	}
	return nil
}
