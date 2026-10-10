package clientapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/mediasignal"
)

const maxMediaResponse = 256 * 1024

type MediaPublishResult struct {
	StreamID string                         `json:"streamId"`
	Answer   mediasignal.SessionDescription `json:"answer"`
}
type MediaSubscribeResult struct {
	Answer mediasignal.SessionDescription `json:"answer"`
}

func (a *App) MediaServerFingerprint(ctx context.Context) (string, error) {
	endpoint, err := a.mediaEndpoint()
	if err != nil {
		return "", err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", endpoint.String(), &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}) // verified interactively by fingerprint
	if err != nil {
		return "", mediaEndpointError(endpoint, err)
	}
	defer conn.Close()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	certificates := conn.ConnectionState().PeerCertificates
	if len(certificates) == 0 {
		return "", errors.New("media server provided no certificate")
	}
	return certificateFingerprint(certificates[0]), nil
}

func (a *App) PublishScreen(ctx context.Context, offer mediasignal.SessionDescription, pinnedFingerprint string) (MediaPublishResult, error) {
	var result MediaPublishResult
	err := a.mediaRequest(ctx, "/media/publish", mediasignal.PublishRequest{Offer: offer}, pinnedFingerprint, &result)
	return result, err
}

func (a *App) SubscribeScreen(ctx context.Context, streamID, subscriberID string, offer mediasignal.SessionDescription, pinnedFingerprint string) (MediaSubscribeResult, error) {
	var result MediaSubscribeResult
	err := a.mediaRequest(ctx, "/media/subscribe", mediasignal.SubscribeRequest{StreamID: streamID, SubscriberID: subscriberID, Offer: offer}, pinnedFingerprint, &result)
	return result, err
}

func (a *App) StopScreen(ctx context.Context, streamID, pinnedFingerprint string) error {
	return a.mediaRequest(ctx, "/media/stop", mediasignal.StreamRequest{StreamID: streamID}, pinnedFingerprint, nil)
}

func (a *App) UnsubscribeScreen(ctx context.Context, streamID, subscriberID, pinnedFingerprint string) error {
	return a.mediaRequest(ctx, "/media/unsubscribe", mediasignal.StreamRequest{StreamID: streamID, SubscriberID: subscriberID}, pinnedFingerprint, nil)
}

func (a *App) mediaRequest(ctx context.Context, path string, requestBody any, pin string, responseBody any) (requestErr error) {
	started := time.Now()
	sessionID := a.state.SessionID()
	defer func() {
		a.logger.Printf("media request completed: operation=%s session_id=%d duration=%s error=%v", path, sessionID, time.Since(started), requestErr)
	}()
	a.mu.Lock()
	conn := a.currentConn
	a.mu.Unlock()
	if conn == nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return ErrNotConnected
	}
	credential, err := voiceclient.RequestMediaCredential(ctx, conn, a.state)
	if err != nil {
		return fmt.Errorf("request media credential: %w", err)
	}
	endpoint, err := a.mediaEndpoint()
	if err != nil {
		return err
	}
	pin = normalizeFingerprint(pin)
	a.logger.Printf("media request starting: operation=%s session_id=%d server=%s", path, sessionID, endpoint)
	if pin == "" {
		return errors.New("media server fingerprint is not trusted")
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13, VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("media server provided no certificate")
		}
		cert, parseErr := x509.ParseCertificate(rawCerts[0])
		if parseErr != nil {
			return parseErr
		}
		if normalizeFingerprint(certificateFingerprint(cert)) != pin {
			return errors.New("media server identity changed")
		}
		return nil
	}}}
	defer transport.CloseIdleConnections()
	data, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+endpoint.String()+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Sonoryx-Session", strconv.FormatUint(a.state.SessionID(), 10))
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(credential[:]))
	response, err := (&http.Client{Transport: transport, Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		return mediaEndpointError(endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("media signaling: %s: %s", response.Status, strings.TrimSpace(string(body)))
	}
	if responseBody == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxMediaResponse))
	if err := decoder.Decode(responseBody); err != nil {
		return fmt.Errorf("decode media response: %w", err)
	}
	return nil
}

func mediaEndpointError(endpoint netip.AddrPort, err error) error {
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return fmt.Errorf("неверная конфигурация сервера: media signaling недоступен по адресу %s; проверьте TCP-порт, firewall и проброс порта", endpoint)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("неверная конфигурация сервера: не удалось подключиться к media signaling %s; проверьте TCP-порт и firewall: %w", endpoint, err)
	}
	return fmt.Errorf("подключение к media signaling %s: %w", endpoint, err)
}

func (a *App) mediaEndpoint() (netip.AddrPort, error) {
	a.mu.Lock()
	endpoint := a.serverEndpoint
	active := a.active
	a.mu.Unlock()
	if !active || !endpoint.IsValid() {
		return netip.AddrPort{}, ErrNotConnected
	}
	mediaPort := a.state.Snapshot().Info.MediaPort
	if mediaPort == 0 {
		return netip.AddrPort{}, errors.New("screen sharing is disabled on this server")
	}
	return netip.AddrPortFrom(endpoint.Addr(), mediaPort), nil
}

func certificateFingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	encoded := strings.ToUpper(hex.EncodeToString(sum[:]))
	parts := make([]string, 0, len(encoded)/2)
	for i := 0; i < len(encoded); i += 2 {
		parts = append(parts, encoded[i:i+2])
	}
	return strings.Join(parts, ":")
}
func normalizeFingerprint(value string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(value), " ", ""))
}
