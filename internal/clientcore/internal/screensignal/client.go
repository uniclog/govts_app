package screensignal

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/mediasignal"
)

type Client struct {
	ctx        context.Context
	endpoint   netip.AddrPort
	sessionID  uint64
	credential func(context.Context) ([32]byte, error)
	logger     *log.Logger
}

func New(ctx context.Context, endpoint netip.AddrPort, sessionID uint64, credential func(context.Context) ([32]byte, error), logger *log.Logger) *Client {
	return &Client{ctx: ctx, endpoint: endpoint, sessionID: sessionID, credential: credential, logger: logger}
}

func (c *Client) bind(ctx context.Context) (context.Context, func()) {
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.ctx, cancel)
	if c.ctx.Err() != nil {
		cancel()
	}
	return ctx, func() { stop(); cancel() }
}

const maxMediaResponse = 256 * 1024

type mediaHTTPError struct {
	statusCode int
	status     string
	body       string
}

func (e *mediaHTTPError) Error() string {
	return fmt.Sprintf("media signaling: %s: %s", e.status, e.body)
}

func legacyMediaServer(err error) bool {
	var response *mediaHTTPError
	return errors.As(err, &response) && response.statusCode == http.StatusNotFound
}

type MediaPublishResult struct {
	StreamID string                         `json:"streamId"`
	Answer   mediasignal.SessionDescription `json:"answer"`
}
type MediaSubscribeResult struct {
	Answer mediasignal.SessionDescription `json:"answer"`
}

func (c *Client) Fingerprint(ctx context.Context) (string, error) {
	ctx, release := c.bind(ctx)
	defer release()
	endpoint := c.endpoint
	dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}}
	rawConn, err := dialer.DialContext(ctx, "tcp", endpoint.String()) // verified interactively by fingerprint
	if err != nil {
		return "", mediaEndpointError(endpoint, err)
	}
	defer rawConn.Close()
	conn := rawConn.(*tls.Conn)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}
	certificates := conn.ConnectionState().PeerCertificates
	if len(certificates) == 0 {
		return "", errors.New("media server provided no certificate")
	}
	return identity.TLSFingerprint(certificates[0]), nil
}

// Only a missing endpoint means an old VP8-only server. Transport and trust
// failures must remain errors rather than silently masking them as legacy mode.
func (c *Client) Capabilities(ctx context.Context, pin string) (json.RawMessage, error) {
	var result json.RawMessage
	err := c.request(ctx, "/media/capabilities", struct{}{}, pin, &result)
	if legacyMediaServer(err) {
		return json.RawMessage("{\"legacy\":true}"), nil
	}
	return result, err
}

func (c *Client) Publish(ctx context.Context, offer mediasignal.SessionDescription, pinnedFingerprint string) (MediaPublishResult, error) {
	var result MediaPublishResult
	err := c.request(ctx, "/media/publish", mediasignal.PublishRequest{Offer: offer}, pinnedFingerprint, &result)
	return result, err
}

func (c *Client) Subscribe(ctx context.Context, streamID, subscriberID string, offer mediasignal.SessionDescription, pinnedFingerprint string) (MediaSubscribeResult, error) {
	var result MediaSubscribeResult
	err := c.request(ctx, "/media/subscribe", mediasignal.SubscribeRequest{StreamID: streamID, SubscriberID: subscriberID, Offer: offer}, pinnedFingerprint, &result)
	return result, err
}

func (c *Client) Stop(ctx context.Context, streamID, pinnedFingerprint string) error {
	return c.request(ctx, "/media/stop", mediasignal.StreamRequest{StreamID: streamID}, pinnedFingerprint, nil)
}

func (c *Client) Unsubscribe(ctx context.Context, streamID, subscriberID, pinnedFingerprint string) error {
	return c.request(ctx, "/media/unsubscribe", mediasignal.StreamRequest{StreamID: streamID, SubscriberID: subscriberID}, pinnedFingerprint, nil)
}

func (c *Client) request(ctx context.Context, path string, requestBody any, pin string, responseBody any) (requestErr error) {
	started := time.Now()
	sessionID := c.sessionID
	defer func() {
		c.logger.Printf("media request completed: operation=%s session_id=%d duration=%s error=%v", path, sessionID, time.Since(started), requestErr)
	}()
	ctx, release := c.bind(ctx)
	defer release()
	credential, err := c.credential(ctx)
	if err != nil {
		return fmt.Errorf("request media credential: %w", err)
	}
	endpoint := c.endpoint
	pin = identity.NormalizeTLSFingerprint(pin)
	c.logger.Printf("media request starting: operation=%s session_id=%d server=%s", path, sessionID, endpoint)
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
		if identity.NormalizeTLSFingerprint(identity.TLSFingerprint(cert)) != pin {
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
	request.Header.Set("X-Govts-Session", strconv.FormatUint(sessionID, 10))
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(credential[:]))
	response, err := (&http.Client{Transport: transport, Timeout: 20 * time.Second}).Do(request)
	if err != nil {
		return mediaEndpointError(endpoint, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &mediaHTTPError{statusCode: response.StatusCode, status: response.Status, body: strings.TrimSpace(string(body))}
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
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("неверная конфигурация сервера: media signaling недоступен по адресу %s; проверьте TCP-порт, firewall и проброс порта", endpoint)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("неверная конфигурация сервера: не удалось подключиться к media signaling %s; проверьте TCP-порт и firewall: %w", endpoint, err)
	}
	return fmt.Errorf("подключение к media signaling %s: %w", endpoint, err)
}
