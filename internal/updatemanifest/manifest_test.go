package updatemanifest

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"testing"
)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return public, private
}

func TestMacArchiveManifestRoundTrip(t *testing.T) {
	public, private := testKey(t)
	archive := []byte("PK\x03\x04 Sonoryx.app")
	data, err := Sign("1.2.3", MacFilename, archive, nil, private)
	if err != nil {
		t.Fatal(err)
	}
	m, err := VerifyAs(data, public, MacFilename)
	if err != nil {
		t.Fatalf("VerifyAs: %v", err)
	}
	if m.Version != "1.2.3" || m.Filename != MacFilename || m.Size != int64(len(archive)) || m.Packed != nil {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestMacManifestIsNotAcceptedForTheExecutable(t *testing.T) {
	public, private := testKey(t)
	data, err := Sign("1.2.3", MacFilename, []byte("PK\x03\x04"), nil, private)
	if err != nil {
		t.Fatal(err)
	}
	// A Windows client must not install the macOS archive and vice versa.
	if _, err := Verify(data, public); err == nil {
		t.Fatal("Verify accepted a manifest signed for the macOS archive")
	}
	exe, err := Sign("1.2.3", Filename, []byte("MZ"), nil, private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAs(exe, public, MacFilename); err == nil {
		t.Fatal("VerifyAs(MacFilename) accepted the executable manifest")
	}
}

func TestManifestRejectsForeignKeyAndTamperedPayload(t *testing.T) {
	public, private := testKey(t)
	other, _ := testKey(t)
	data, err := Sign("1.2.3", MacFilename, []byte("PK\x03\x04"), nil, private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyAs(data, other, MacFilename); err == nil {
		t.Fatal("manifest verified with another key")
	}
	var envelope Envelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	envelope.Payload = append([]byte(nil), envelope.Payload...)
	envelope.Payload[len(envelope.Payload)-2] ^= 1
	tampered, _ := json.Marshal(envelope)
	if _, err := VerifyAs(tampered, public, MacFilename); err == nil {
		t.Fatal("tampered manifest verified")
	}
}
