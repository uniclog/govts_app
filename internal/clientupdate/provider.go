package clientupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/updatemanifest"
)

const repository = "uniclog/govts_app"

type provider struct {
	key    ed25519.PublicKey
	client *http.Client
	busy   func() bool
	etag   string
	cached *updater.Release
}

func (p *provider) Name() string { return "sonoryx-github-signed" }

func (p *provider) get(ctx context.Context, address string) (*http.Response, error) {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || (u.Host != "api.github.com" && u.Host != "github.com") {
		return nil, errors.New("неверный адрес обновления")
	}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("User-Agent", "Sonoryx-updater")
	r.Header.Set("Accept", "application/vnd.github+json")
	if strings.Contains(address, "/releases/latest") && p.etag != "" {
		r.Header.Set("If-None-Match", p.etag)
	}
	return p.client.Do(r)
}

func (p *provider) Check(ctx context.Context, req updater.CheckRequest) (*updater.Release, error) {
	if req.Platform != "windows" || req.Arch != "amd64" {
		return nil, errors.New("автообновление доступно для Windows amd64")
	}
	response, err := p.get(ctx, "https://api.github.com/repos/"+repository+"/releases/latest")
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusNotModified {
		return p.cached, nil
	}
	if response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub: HTTP %d; повторите проверку позже", response.StatusCode)
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Body       string `json:"body"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return nil, err
	}
	version := strings.TrimPrefix(release.Tag, "v")
	newVersion, err := appversion.Parse(version)
	if err != nil {
		return nil, err
	}
	current, err := appversion.Parse(req.CurrentVersion)
	if err != nil {
		return nil, err
	}
	if release.Draft || release.Prerelease || newVersion <= current {
		p.cached = nil
		p.etag = response.Header.Get("ETag")
		return nil, nil
	}
	var manifestURL, binaryURL, packedURL string
	var size, packedSize int64
	prefix := "https://github.com/" + repository + "/releases/download/" + release.Tag + "/"
	for _, asset := range release.Assets {
		if !strings.HasPrefix(asset.URL, prefix) {
			continue
		}
		switch asset.Name {
		case updatemanifest.Filename:
			binaryURL, size = asset.URL, asset.Size
		case updatemanifest.PackedFilename:
			packedURL, packedSize = asset.URL, asset.Size
		case updatemanifest.AssetName:
			manifestURL = asset.URL
		}
	}
	if manifestURL == "" || binaryURL == "" {
		return nil, errors.New("релиз пока не содержит подписанного обновления")
	}
	manifestResponse, err := p.get(ctx, manifestURL)
	if err != nil {
		return nil, err
	}
	defer func() { _ = manifestResponse.Body.Close() }()
	if manifestResponse.StatusCode != http.StatusOK {
		return nil, errors.New("не удалось получить подпись обновления")
	}
	data, err := io.ReadAll(io.LimitReader(manifestResponse.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return nil, errors.New("неверный размер манифеста")
	}
	m, err := updatemanifest.Verify(data, p.key)
	if err != nil {
		return nil, err
	}
	if m.Version != version || m.Size != size {
		return nil, errors.New("подписанные данные не совпадают с релизом")
	}
	r := &updater.Release{Version: version, Name: "Sonoryx " + version, Notes: release.Body,
		Artifact:     updater.Artifact{Filename: m.Filename, Filetype: "exe", Size: m.Size, Platform: "windows", Arch: "amd64"},
		Verification: &updater.Verification{DigestAlgo: "sha256", Digest: m.Digest, SignatureAlgo: "ed25519", Signature: m.Signature},
		Metadata:     map[string]any{"url": binaryURL, "minServerVersion": m.MinServerVersion}}
	if m.Packed != nil && packedURL != "" && packedSize == m.Packed.Size {
		r.Metadata["packed"] = packedAsset{url: packedURL, size: m.Packed.Size, digest: m.Packed.Digest}
	}
	p.cached, p.etag = r, response.Header.Get("ETag")
	return r, nil
}

// packedAsset is the signed compressed executable selected by Check.
type packedAsset struct {
	url    string
	size   int64
	digest []byte
}

func (p *provider) Download(ctx context.Context, r *updater.Release, dst io.Writer, progress func(int64, int64)) error {
	if packed, ok := r.Metadata["packed"].(packedAsset); ok {
		return p.downloadPacked(ctx, r, packed, dst, progress)
	}
	address, _ := r.Metadata["url"].(string)
	body, err := p.open(ctx, address, r.Artifact.Size)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	return copyArtifact(dst, &throttledReader{ctx: ctx, r: body, busy: p.busy}, r.Artifact.Size, progress)
}

// downloadPacked streams the compressed executable through the zstd decoder.
// The updater hashes what reaches dst, so the executable signature is still
// checked; the archive digest additionally rejects a substituted archive.
func (p *provider) downloadPacked(ctx context.Context, r *updater.Release, packed packedAsset, dst io.Writer, progress func(int64, int64)) error {
	body, err := p.open(ctx, packed.url, packed.size)
	if err != nil {
		return err
	}
	defer func() { _ = body.Close() }()
	hash := sha256.New()
	limited := &io.LimitedReader{R: io.TeeReader(&throttledReader{ctx: ctx, r: body, busy: p.busy}, hash), N: packed.size + 1}
	decoder, err := zstd.NewReader(limited, zstd.WithDecoderMaxWindow(updatemanifest.PackedWindow), zstd.WithDecoderConcurrency(1))
	if err != nil {
		return err
	}
	err = copyArtifact(dst, decoder, r.Artifact.Size, progress)
	// Close stops the decoder's reader before the rest of the archive is
	// drained into the digest.
	decoder.Close()
	if err != nil {
		return err
	}
	if _, err := io.Copy(io.Discard, limited); err != nil {
		return err
	}
	if packed.size+1-limited.N != packed.size || !bytes.Equal(hash.Sum(nil), packed.digest) {
		return errors.New("сжатое обновление не совпадает с подписью")
	}
	return nil
}

// open requests address and checks the response against the signed size.
func (p *provider) open(ctx context.Context, address string, size int64) (io.ReadCloser, error) {
	response, err := p.get(ctx, address)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		return nil, fmt.Errorf("загрузка: HTTP %d", response.StatusCode)
	}
	if response.ContentLength >= 0 && response.ContentLength != size {
		_ = response.Body.Close()
		return nil, errors.New("размер загрузки не совпадает с подписью")
	}
	return response.Body, nil
}

// copyArtifact writes exactly size bytes of the executable to dst.
func copyArtifact(dst io.Writer, src io.Reader, size int64, progress func(int64, int64)) error {
	var written int64
	buffer := make([]byte, 64<<10)
	for {
		n, readErr := src.Read(buffer)
		if written+int64(n) > size {
			return errors.New("обновление превышает объявленный размер")
		}
		if n > 0 {
			count, err := dst.Write(buffer[:n])
			written += int64(count)
			if err != nil {
				return err
			}
			if count != n {
				return io.ErrShortWrite
			}
			progress(written, size)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	if written != size {
		return errors.New("обновление загружено не полностью")
	}
	return nil
}

// throttledReader limits the download to 512 KiB/s while busy reports an
// active voice session, so the update does not compete with voice traffic.
type throttledReader struct {
	ctx  context.Context
	r    io.Reader
	busy func() bool
}

func (t *throttledReader) Read(b []byte) (int, error) {
	started := time.Now()
	n, err := t.r.Read(b)
	if n > 0 && t.busy() {
		delay := time.Duration(n)*time.Second/(512<<10) - time.Since(started)
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-t.ctx.Done():
				timer.Stop()
				return n, t.ctx.Err()
			case <-timer.C:
			}
		}
	}
	return n, err
}
