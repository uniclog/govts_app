package updatemanifest

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"

	"uniclog.io/sonoryx/internal/appversion"
)

const Filename = "SRX64.exe"
const AssetName = "SRX64.exe.signature"
const MaxSize int64 = 512 << 20

// LegacyFilename and LegacyAssetName are the assets that clients released
// before the Sonoryx rename look for. Releases keep publishing the same
// executable under these names so those clients still update in place.
const LegacyFilename = "GTS64.exe"
const LegacyAssetName = "signature"

// PackedFilename is the zstd-compressed copy of Filename. The plain
// executable stays in the release for clients that predate compression.
const PackedFilename = Filename + ".zst"

// PackedWindow is the zstd window used to pack the executable; clients
// refuse archives that need more decoder memory than this.
const PackedWindow = 32 << 20

type Manifest struct {
	Version          string  `json:"version"`
	Filename         string  `json:"filename"`
	Size             int64   `json:"size"`
	Digest           []byte  `json:"sha256"`
	Signature        []byte  `json:"signature"`
	MinServerVersion string  `json:"minServerVersion,omitempty"`
	Packed           *Packed `json:"packed,omitempty"`
}

// Packed describes PackedFilename; its content must decompress to the
// executable described by Manifest.Size and Manifest.Digest.
type Packed struct {
	Size   int64  `json:"size"`
	Digest []byte `json:"sha256"`
}

type Envelope struct {
	Payload   []byte `json:"payload"`
	Signature []byte `json:"signature"`
}

// Sign signs binary published as filename and, when packed is not nil, its
// compressed copy.
func Sign(version, filename string, binary, packed []byte, key ed25519.PrivateKey) ([]byte, error) {
	digest := sha256.Sum256(binary)
	m := Manifest{
		Version:          version,
		Filename:         filename,
		Size:             int64(len(binary)),
		Digest:           digest[:],
		Signature:        ed25519.Sign(key, digest[:]),
		MinServerVersion: appversion.MinimumServerVersion,
	}
	if packed != nil {
		packedDigest := sha256.Sum256(packed)
		m.Packed = &Packed{Size: int64(len(packed)), Digest: packedDigest[:]}
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{payload, ed25519.Sign(key, payload)})
}

func Verify(data []byte, key ed25519.PublicKey) (Manifest, error) {
	return VerifyAs(data, key, Filename)
}

// VerifyAs checks a manifest signed for the executable published as filename.
func VerifyAs(data []byte, key ed25519.PublicKey, filename string) (Manifest, error) {
	var e Envelope
	if err := json.Unmarshal(data, &e); err != nil {
		return Manifest{}, err
	}
	if len(key) != ed25519.PublicKeySize || !ed25519.Verify(key, e.Payload, e.Signature) {
		return Manifest{}, errors.New("подпись манифеста обновления недействительна")
	}
	var m Manifest
	if err := json.Unmarshal(e.Payload, &m); err != nil {
		return m, err
	}
	if m.Filename != filename || m.Size <= 0 || m.Size > MaxSize || len(m.Digest) != sha256.Size || !ed25519.Verify(key, m.Digest, m.Signature) {
		return m, errors.New("неверные параметры подписанного обновления")
	}
	if m.Packed != nil && (m.Packed.Size <= 0 || m.Packed.Size > MaxSize || len(m.Packed.Digest) != sha256.Size) {
		return m, errors.New("неверные параметры сжатого обновления")
	}
	if m.MinServerVersion != "" {
		if _, err := appversion.Parse(m.MinServerVersion); err != nil {
			return m, errors.New("неверная минимальная версия сервера в подписи обновления")
		}
	}
	return m, nil
}
