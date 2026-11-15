package clientapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/filesignal"
	"uniclog.io/sonoryx/internal/protocol"
)

func (a *App) FileAccountRights(ctx context.Context, expected, accountID string) (map[string]bool, error) {
	id, err := strconv.ParseInt(accountID, 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("invalid account ID")
	}
	var out map[string]bool
	_, err = a.fileJSON(ctx, expected, "GET", "/files/v1/admin/rights?id="+strconv.FormatInt(id, 10), nil, &out)
	return out, err
}

func (a *App) FileAdmin(ctx context.Context, expected, method, cursor string, input any) (map[string]any, error) {
	var out map[string]any
	path := "/files/v1/admin"
	if cursor != "" {
		if _, err := strconv.ParseInt(cursor, 10, 64); err != nil {
			return nil, err
		}
		path += "?cursor=" + cursor
	}
	var output any
	if method == "GET" {
		output = &out
	}
	_, err := a.fileJSON(ctx, expected, method, path, input, output)
	return out, err
}

type fileConnection struct {
	connContext context.Context
	endpoint    netip.AddrPort
	session     uint64
}

func (a *App) fileConnection(expected string) (fileConnection, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.currentConn == nil || a.chatSession == nil || a.chatSession.ctx.Err() != nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return fileConnection{}, ErrNotConnected
	}
	if expected != a.chatContextLocked()+"|"+strconv.FormatInt(a.state.SnapshotView().UserID, 10) {
		return fileConnection{}, errors.New("file server or account changed")
	}
	return fileConnection{a.chatSession.ctx, a.serverEndpoint, a.state.SessionID()}, nil
}

func (a *App) fileClient(ctx context.Context, expected string) (*http.Client, string, string, func(), error) {
	c, err := a.fileConnection(expected)
	if err != nil {
		return nil, "", "", func() {}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(c.connContext, cancel)
	cleanup := func() { stop(); cancel() }
	a.mu.Lock()
	conn := a.currentConn
	a.mu.Unlock()
	response, err := voiceclient.DoRequest(ctx, conn, a.state, protocol.VoicePacket{Type: protocol.PacketFileAccessRequest}, time.Second)
	if err != nil {
		cleanup()
		return nil, "", "", func() {}, err
	}
	var access protocol.FileAccess
	if response.Type != protocol.PacketFileAccessAck || json.Unmarshal(response.Payload, &access) != nil || access.Port == 0 || access.Fingerprint == "" {
		cleanup()
		return nil, "", "", func() {}, errors.New("file transfer is disabled on this server")
	}
	key := netip.AddrPortFrom(c.endpoint.Addr(), access.Port).String() + "|" + normalizeFingerprint(access.Fingerprint)
	a.fileTransportMu.Lock()
	if a.fileTransport == nil || a.fileTransportKey != key {
		if a.fileTransport != nil {
			a.fileTransport.CloseIdleConnections()
		}
		a.fileTransport = pinnedFileTransport(access.Fingerprint)
		a.fileTransportKey = key
	}
	transport := a.fileTransport
	a.fileTransportMu.Unlock()
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, "https://" + netip.AddrPortFrom(c.endpoint.Addr(), access.Port).String(), access.Credential, cleanup, nil
}

func pinnedFileTransport(pin string) *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 || certificateFingerprint(cs.PeerCertificates[0]) != normalizeFingerprint(pin) {
			return errors.New("file server TLS identity changed")
		}
		return nil
	}}, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 16 << 10, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 8}
}

var errFileOffsetChanged = errors.New("file upload offset changed; reconcile before retry")

func (a *App) fileRequest(ctx context.Context, expected, method, path string, body io.Reader, headers map[string]string) (*http.Response, func(), error) {
	var start int64
	seeker, replayable := body.(io.Seeker)
	if replayable {
		var err error
		start, err = seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, func() {}, err
		}
	}
	for attempt := 0; ; attempt++ {
		response, close, err := a.fileRequestAuthenticated(ctx, expected, method, path, body, headers)
		var status *fileHTTPError
		if !errors.As(err, &status) || status.status != http.StatusTooManyRequests || attempt >= 4 || (body != nil && !replayable) {
			return response, close, err
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 300 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, func() {}, ctx.Err()
		case <-timer.C:
		}
		if replayable {
			if _, err = seeker.Seek(start, io.SeekStart); err != nil {
				return nil, func() {}, err
			}
		}
	}
}

func (a *App) fileRequestAuthenticated(ctx context.Context, expected, method, path string, body io.Reader, headers map[string]string) (*http.Response, func(), error) {
	var start int64
	seeker, replayable := body.(io.Seeker)
	if replayable {
		var err error
		start, err = seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, func() {}, err
		}
	}
	response, close, err := a.fileRequestOnce(ctx, expected, method, path, body, headers)
	var httpErr *fileHTTPError
	if !errors.As(err, &httpErr) || httpErr.status != 401 || ctx.Err() != nil {
		return response, close, err
	}
	if body != nil && !replayable {
		return response, close, err
	}
	if method == "PATCH" {
		var upload filesignal.FileUpload
		if _, e := a.fileJSON(ctx, expected, "GET", strings.TrimSuffix(path, "/content"), nil, &upload); e != nil {
			return nil, func() {}, e
		}
		offset, e := strconv.ParseInt(headers["Upload-Offset"], 10, 64)
		if e != nil || upload.Offset != offset {
			return nil, func() {}, errFileOffsetChanged
		}
	}
	if replayable {
		if _, e := seeker.Seek(start, io.SeekStart); e != nil {
			return nil, func() {}, e
		}
	}
	return a.fileRequestOnce(ctx, expected, method, path, body, headers)
}

func (a *App) fileRequestOnce(ctx context.Context, expected, method, path string, body io.Reader, headers map[string]string) (*http.Response, func(), error) {
	connection, err := a.fileConnection(expected)
	if err != nil {
		return nil, func() {}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	stop := context.AfterFunc(connection.connContext, cancel)
	// Metadata, previews and transfers share a budget below the server's per-account limit.
	a.fileTransportMu.Lock()
	if a.fileRequestSlots == nil {
		a.fileRequestSlots = make(chan struct{}, 2)
	}
	slots := a.fileRequestSlots
	a.fileTransportMu.Unlock()
	select {
	case slots <- struct{}{}:
	case <-ctx.Done():
		stop()
		cancel()
		return nil, func() {}, ctx.Err()
	}
	client, base, credential, closeClient, err := a.fileClient(ctx, expected)
	if err != nil {
		<-slots
		stop()
		cancel()
		return nil, func() {}, err
	}
	var release sync.Once
	cleanup := func() { release.Do(func() { closeClient(); stop(); cancel(); <-slots }) }
	request, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	request.Header.Set("X-Govts-Session", strconv.FormatUint(connection.session, 10))
	request.Header.Set("Authorization", "Bearer "+credential)
	for k, v := range headers {
		if k == "Content-Length" {
			request.ContentLength, _ = strconv.ParseInt(v, 10, 64)
		} else {
			request.Header.Set(k, v)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		response.Body.Close()
		cleanup()
		return nil, func() {}, &fileHTTPError{response.StatusCode, strings.TrimSpace(string(b))}
	}
	return response, func() { response.Body.Close(); cleanup() }, nil
}

type fileHTTPError struct {
	status  int
	message string
}

func (e *fileHTTPError) Error() string { return fmt.Sprintf("files HTTP %d: %s", e.status, e.message) }

func retryableFileError(err error) bool {
	var n net.Error
	var h *fileHTTPError
	return errors.Is(err, errFileOffsetChanged) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &n) || errors.As(err, &h) && (h.status == 429 || h.status >= 500)
}

func (a *App) fileJSON(ctx context.Context, expected, method, path string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	response, close, err := a.fileRequest(ctx, expected, method, path, body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return 0, err
	}
	defer close()
	if output != nil {
		err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(output)
	}
	return response.StatusCode, err
}

func (a *App) FileMetadata(ctx context.Context, expected string, ids []string) ([]filesignal.FileAttachment, error) {
	var list []filesignal.FileAttachment
	_, err := a.fileJSON(ctx, expected, "POST", "/files/v1/metadata", ids, &list)
	return list, err
}

func (a *App) FileCapabilities(ctx context.Context, expected string) (map[string]any, error) {
	var caps map[string]any
	_, err := a.fileJSON(ctx, expected, "GET", "/files/v1/capabilities", nil, &caps)
	return caps, err
}

func (a *App) FilePreview(ctx context.Context, expected, id string) ([]byte, error) {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return nil, err
	}
	response, close, err := a.fileRequest(ctx, expected, "GET", "/files/v1/attachments/"+id+"/preview", nil, nil)
	if err != nil {
		return nil, err
	}
	defer close()
	b, err := io.ReadAll(io.LimitReader(response.Body, (512<<10)+1))
	if len(b) > 512<<10 {
		return nil, errors.New("preview too large")
	}
	return b, err
}

// FileOriginalImage returns the original bytes, never a rescaled preview.
func (a *App) FileOriginalImage(ctx context.Context, expected, id string) ([]byte, string, error) {
	parsed, err := strconv.ParseInt(id, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, "", errors.New("invalid attachment ID")
	}
	list, err := a.FileMetadata(ctx, expected, []string{id})
	if err != nil {
		return nil, "", err
	}
	const maxImageBytes = 32 << 20
	if len(list) != 1 || list[0].Deleted || list[0].Size <= 0 || list[0].Size > maxImageBytes {
		return nil, "", errors.New("image unavailable or exceeds 32 MiB")
	}
	response, close, err := a.fileRequest(ctx, expected, "GET", "/files/v1/attachments/"+id+"/content", nil, nil)
	if err != nil {
		return nil, "", err
	}
	defer close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) != list[0].Size || len(data) > maxImageBytes {
		return nil, "", errors.New("invalid original image size")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != list[0].SHA256 {
		return nil, "", errors.New("original image checksum mismatch")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width <= 0 || config.Height <= 0 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return nil, "", errors.New("invalid image or exceeds 40 megapixels")
	}
	mime := map[string]string{"png": "image/png", "jpeg": "image/jpeg", "webp": "image/webp"}[format]
	if mime == "" {
		return nil, "", errors.New("unsupported image format")
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	return data, mime, nil
}
