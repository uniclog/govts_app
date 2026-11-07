package clientupdate

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/updater"
	"uniclog.io/sonoryx/internal/updatemanifest"
)

func TestReleaseTargetFor(t *testing.T) {
	tests := []struct {
		platform, arch string
		filename       string
		packed         string
		filetype       string
		ok             bool
	}{
		{"windows", "amd64", updatemanifest.Filename, updatemanifest.PackedFilename, "exe", true},
		{"darwin", "arm64", updatemanifest.MacFilename, "", "zip", true},
		{"darwin", "amd64", updatemanifest.MacFilename, "", "zip", true},
		{"windows", "arm64", "", "", "", false},
		{"linux", "amd64", "", "", "", false},
	}
	for _, test := range tests {
		target, ok := releaseTargetFor(test.platform, test.arch)
		if ok != test.ok || target.filename != test.filename || target.packed != test.packed || target.filetype != test.filetype {
			t.Errorf("%s/%s: got %+v ok=%t", test.platform, test.arch, target, ok)
		}
		if ok && target.manifest != target.filename+".signature" {
			t.Errorf("%s/%s: manifest %q does not follow the asset name", test.platform, test.arch, target.manifest)
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// fakeRelease serves a GitHub release v9.0.0 with both platforms' assets.
func fakeRelease(t *testing.T) (*provider, ed25519.PublicKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	exe, archive := []byte("MZ executable"), []byte("PK\x03\x04 Sonoryx.app")
	exeManifest, err := updatemanifest.Sign("9.0.0", updatemanifest.Filename, exe, nil, private)
	if err != nil {
		t.Fatal(err)
	}
	macManifest, err := updatemanifest.Sign("9.0.0", updatemanifest.MacFilename, archive, nil, private)
	if err != nil {
		t.Fatal(err)
	}
	base := "https://github.com/" + repository + "/releases/download/v9.0.0/"
	files := map[string][]byte{
		base + updatemanifest.Filename:     exe,
		base + updatemanifest.AssetName:    exeManifest,
		base + updatemanifest.MacFilename:  archive,
		base + updatemanifest.MacAssetName: macManifest,
	}
	type asset struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	}
	var assets []asset
	for _, name := range []string{updatemanifest.Filename, updatemanifest.AssetName, updatemanifest.MacFilename, updatemanifest.MacAssetName} {
		assets = append(assets, asset{name, base + name, int64(len(files[base+name]))})
	}
	release, _ := json.Marshal(map[string]any{"tag_name": "v9.0.0", "assets": assets})
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, ok := files[r.URL.String()]
		if r.URL.String() == "https://api.github.com/repos/"+repository+"/releases/latest" {
			body, ok = release, true
		}
		if !ok {
			return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(bytes.NewReader(nil)), Request: r}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Header: http.Header{}, Request: r}, nil
	})}
	return &provider{key: public, client: client, busy: func() bool { return false }}, public
}

func TestCheckPicksMacArchiveOnDarwin(t *testing.T) {
	p, _ := fakeRelease(t)
	for _, arch := range []string{"arm64", "amd64"} {
		p.etag, p.cached = "", nil
		r, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "1.0.0", Platform: "darwin", Arch: arch})
		if err != nil {
			t.Fatalf("%s: Check: %v", arch, err)
		}
		if r == nil || r.Version != "9.0.0" {
			t.Fatalf("%s: release = %+v", arch, r)
		}
		if r.Artifact.Filename != updatemanifest.MacFilename || r.Artifact.Filetype != "zip" || r.Artifact.Platform != "darwin" || r.Artifact.Arch != arch {
			t.Fatalf("%s: artifact = %+v", arch, r.Artifact)
		}
		if _, packed := r.Metadata["packed"]; packed {
			t.Fatalf("%s: macOS release must not use a packed copy", arch)
		}
		var out bytes.Buffer
		if err := p.Download(context.Background(), r, &out, func(int64, int64) {}); err != nil {
			t.Fatalf("%s: Download: %v", arch, err)
		}
		if !bytes.HasPrefix(out.Bytes(), []byte("PK\x03\x04")) {
			t.Fatalf("%s: downloaded %q, want the zip", arch, out.Bytes())
		}
	}
}

func TestCheckStillPicksExecutableOnWindows(t *testing.T) {
	p, _ := fakeRelease(t)
	r, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "1.0.0", Platform: "windows", Arch: "amd64"})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if r == nil || r.Artifact.Filename != updatemanifest.Filename || r.Artifact.Filetype != "exe" {
		t.Fatalf("release = %+v", r)
	}
}

func TestCheckRejectsUnsupportedPlatform(t *testing.T) {
	p, _ := fakeRelease(t)
	if _, err := p.Check(context.Background(), updater.CheckRequest{CurrentVersion: "1.0.0", Platform: "linux", Arch: "amd64"}); err == nil {
		t.Fatal("Check accepted linux")
	}
}

func TestTargetDirFor(t *testing.T) {
	sep := string(filepath.Separator)
	join := func(parts ...string) string { return sep + filepath.Join(parts...) }
	tests := map[string]string{
		join("Applications", "Sonoryx.app", "Contents", "MacOS", "Sonoryx"):                    join("Applications"),
		join("Users", "a", "Apps", "Sonoryx.app", "Contents", "MacOS", "Sonoryx"):              join("Users", "a", "Apps"),
		join("Applications", "Outer.app", "Contents", "Helpers", "Inner.app", "Contents", "x"): join("Applications"),
		join("opt", "sonoryx", "Sonoryx"):                                                      join("opt", "sonoryx"),
	}
	for executable, want := range tests {
		if got := targetDirFor(executable); got != want {
			t.Errorf("targetDirFor(%q) = %q, want %q", executable, got, want)
		}
	}
}
