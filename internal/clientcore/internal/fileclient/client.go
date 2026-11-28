package fileclient

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
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
	"uniclog.io/sonoryx/internal/fileapi"
	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/protocol"
)

type Session struct {
	Context  context.Context
	Endpoint netip.Addr
	ID       uint64
	Access   func(context.Context) (protocol.FileAccess, error)
}

type Client struct {
	session      func(string) (Session, error)
	slots        chan struct{}
	mu           sync.Mutex
	transport    *http.Transport
	transportKey string
	closed       bool
}

func New(session func(string) (Session, error)) *Client {
	return &Client{session: session, slots: make(chan struct{}, 2)}
}

func (c *Client) CheckSession(expected string) error {
	_, err := c.session(expected)
	return err
}

func (c *Client) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	if c.transport != nil {
		c.transport.CloseIdleConnections()
	}
}

func (c *Client) connection(ctx context.Context, session Session) (*http.Client, string, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}
	access, err := session.Access(ctx)
	if err != nil {
		return nil, "", "", err
	}
	if err := ctx.Err(); err != nil {
		return nil, "", "", err
	}
	endpoint := netip.AddrPortFrom(session.Endpoint, access.Port).String()
	key := endpoint + "|" + identity.NormalizeTLSFingerprint(access.Fingerprint)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, "", "", context.Canceled
	}
	if c.transport == nil || c.transportKey != key {
		if c.transport != nil {
			c.transport.CloseIdleConnections()
		}
		c.transport = pinnedFileTransport(access.Fingerprint)
		c.transportKey = key
	}
	return &http.Client{Transport: c.transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, "https://" + endpoint, access.Credential, nil
}

func pinnedFileTransport(pin string) *http.Transport {
	return &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 || identity.TLSFingerprint(cs.PeerCertificates[0]) != identity.NormalizeTLSFingerprint(pin) {
			return errors.New("file server TLS identity changed")
		}
		return nil
	}}, ResponseHeaderTimeout: 15 * time.Second, MaxResponseHeaderBytes: 16 << 10, IdleConnTimeout: 30 * time.Second, MaxIdleConnsPerHost: 4, MaxConnsPerHost: 8}
}

var errFileOffsetChanged = errors.New("file upload offset changed; reconcile before retry")

func (c *Client) Request(ctx context.Context, expected, method, path string, body io.Reader, headers map[string]string) (*http.Response, func(), error) {
	session, err := c.session(expected)
	if err != nil {
		return nil, func() {}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.Context, cancel)
	if session.Context.Err() != nil {
		cancel()
	}
	cleanup := func() { stop(); cancel() }
	response, release, err := c.request(ctx, session, method, path, body, headers)
	if err != nil {
		cleanup()
		return response, release, err
	}
	return response, func() { release(); cleanup() }, nil
}

func (c *Client) request(ctx context.Context, session Session, method, path string, body io.Reader, headers map[string]string) (*http.Response, func(), error) {
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
		response, close, err := c.fileRequestAuthenticated(ctx, session, method, path, body, headers)
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

func (c *Client) fileRequestAuthenticated(ctx context.Context, session Session, method, path string, body io.Reader, headers map[string]string) (*http.Response, func(), error) {
	var start int64
	seeker, replayable := body.(io.Seeker)
	if replayable {
		var err error
		start, err = seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, func() {}, err
		}
	}
	response, close, err := c.fileRequestOnce(ctx, session, method, path, body, headers)
	var httpErr *fileHTTPError
	if !errors.As(err, &httpErr) || httpErr.status != 401 || ctx.Err() != nil {
		return response, close, err
	}
	if body != nil && !replayable {
		return response, close, err
	}
	if method == "PATCH" {
		var upload fileapi.FileUpload
		status, release, e := c.request(ctx, session, "GET", strings.TrimSuffix(path, "/content"), nil, nil)
		if e != nil {
			return nil, func() {}, e
		}
		e = json.NewDecoder(io.LimitReader(status.Body, 64<<10)).Decode(&upload)
		release()
		if e != nil {
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
	return c.fileRequestOnce(ctx, session, method, path, body, headers)
}

func (c *Client) fileRequestOnce(ctx context.Context, session Session, method, path string, body io.Reader, headers map[string]string) (*http.Response, func(), error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	// Metadata, previews and transfers share a budget below the server's per-account limit.
	select {
	case c.slots <- struct{}{}:
	case <-ctx.Done():
		cancel()
		return nil, func() {}, ctx.Err()
	}
	client, base, credential, err := c.connection(ctx, session)
	if err != nil {
		<-c.slots
		cancel()
		return nil, func() {}, err
	}
	var release sync.Once
	cleanup := func() { release.Do(func() { cancel(); <-c.slots }) }
	request, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	request.Header.Set("X-Govts-Session", strconv.FormatUint(session.ID, 10))
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
func (c *Client) Retryable(err error) bool {
	var n net.Error
	var h *fileHTTPError
	return errors.Is(err, errFileOffsetChanged) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &n) || errors.As(err, &h) && (h.status == 429 || h.status >= 500)
}
func (c *Client) fileJSON(ctx context.Context, expected, method, path string, input, output any) (int, error) {
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	response, close, err := c.Request(ctx, expected, method, path, body, map[string]string{"Content-Type": "application/json"})
	if err != nil {
		return 0, err
	}
	defer close()
	if output != nil {
		err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(output)
	}
	return response.StatusCode, err
}
func (c *Client) FileMetadata(ctx context.Context, expected string, ids []string) ([]fileapi.FileAttachment, error) {
	var list []fileapi.FileAttachment
	_, err := c.fileJSON(ctx, expected, "POST", "/files/v1/metadata", ids, &list)
	return list, err
}
func (c *Client) FileCapabilities(ctx context.Context, expected string) (map[string]any, error) {
	var caps map[string]any
	_, err := c.fileJSON(ctx, expected, "GET", "/files/v1/capabilities", nil, &caps)
	return caps, err
}
func (c *Client) FilePreview(ctx context.Context, expected, id string) ([]byte, error) {
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		return nil, err
	}
	response, close, err := c.Request(ctx, expected, "GET", "/files/v1/attachments/"+id+"/preview", nil, nil)
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
func (c *Client) FileOriginalImage(ctx context.Context, expected, id string) ([]byte, string, error) {
	parsed, err := strconv.ParseInt(id, 10, 64)
	if err != nil || parsed <= 0 {
		return nil, "", errors.New("invalid attachment ID")
	}
	list, err := c.FileMetadata(ctx, expected, []string{id})
	if err != nil {
		return nil, "", err
	}
	const maxImageBytes = 32 << 20
	if len(list) != 1 || list[0].Deleted || list[0].Size <= 0 || list[0].Size > maxImageBytes {
		return nil, "", errors.New("image unavailable or exceeds 32 MiB")
	}
	response, close, err := c.Request(ctx, expected, "GET", "/files/v1/attachments/"+id+"/content", nil, nil)
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

func (c *Client) FileAccountRights(ctx context.Context, expected, accountID string) (map[string]bool, error) {
	id, err := strconv.ParseInt(accountID, 10, 64)
	if err != nil || id <= 0 {
		return nil, errors.New("invalid account ID")
	}
	var out map[string]bool
	_, err = c.fileJSON(ctx, expected, "GET", "/files/v1/admin/rights?id="+strconv.FormatInt(id, 10), nil, &out)
	return out, err
}

func (c *Client) FileAdmin(ctx context.Context, expected, method, cursor string, input any) (map[string]any, error) {
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
	_, err := c.fileJSON(ctx, expected, method, path, input, output)
	return out, err
}

func (c *Client) UploadStatus(ctx context.Context, expected, id string) (fileapi.FileUpload, error) {
	var upload fileapi.FileUpload
	_, err := c.fileJSON(ctx, expected, "GET", "/files/v1/uploads/"+id, nil, &upload)
	return upload, err
}

func (c *Client) StartUpload(ctx context.Context, expected string, input fileapi.FileUpload) (fileapi.FileUpload, error) {
	var upload fileapi.FileUpload
	_, err := c.fileJSON(ctx, expected, "POST", "/files/v1/uploads", input, &upload)
	return upload, err
}

func (c *Client) WriteChunk(ctx context.Context, expected, id string, offset int64, chunk []byte) (fileapi.FileUpload, error) {
	sum := sha256.Sum256(chunk)
	headers := map[string]string{"Upload-Offset": strconv.FormatInt(offset, 10), "Upload-Checksum": hex.EncodeToString(sum[:])}
	response, release, err := c.Request(ctx, expected, "PATCH", "/files/v1/uploads/"+id+"/content", bytes.NewReader(chunk), headers)
	var upload fileapi.FileUpload
	if err != nil {
		return upload, err
	}
	defer release()
	err = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&upload)
	return upload, err
}

func (c *Client) FinishUpload(ctx context.Context, expected, id string) (fileapi.FileUpload, error) {
	var upload fileapi.FileUpload
	_, err := c.fileJSON(ctx, expected, "POST", "/files/v1/uploads/"+id+"/complete", nil, &upload)
	return upload, err
}

func (c *Client) CancelUpload(ctx context.Context, expected, id string) error {
	_, err := c.fileJSON(ctx, expected, "DELETE", "/files/v1/uploads/"+id, nil, nil)
	return err
}

func (c *Client) ReadRange(ctx context.Context, expected string, id, offset, end, size int64, hash string, dst io.Writer) (int64, error) {
	tag := "\"" + hash + "\""
	headers := map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", offset, end), "If-Range": tag}
	response, release, err := c.Request(ctx, expected, "GET", "/files/v1/attachments/"+strconv.FormatInt(id, 10)+"/content", nil, headers)
	if err != nil {
		return 0, err
	}
	defer release()
	if response.StatusCode != http.StatusPartialContent || response.Header.Get("ETag") != tag || response.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, end, size) {
		return 0, errors.New("invalid download range")
	}
	n, err := io.CopyN(dst, response.Body, end-offset+1)
	if err == nil {
		err = ctx.Err()
	}
	return n, err
}
