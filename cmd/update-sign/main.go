package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/updatemanifest"
)

func main() {
	generate := flag.String("generate-key", "", "write a NEW private seed to this file (outside the repository)")
	public := flag.String("public-key", "", "public key file")
	binary := flag.String("binary", "", "desktop executable to sign")
	version := flag.String("version", appversion.ClientVersion, "desktop version (defaults to version/release.json)")
	out := flag.String("out", "", "signed manifest destination")
	packed := flag.String("packed", "", "optional zstd-compressed executable destination, signed in the manifest")
	flag.Parse()
	if err := run(*generate, *public, *binary, *version, *out, *packed); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(generate, public, binary, version, out, packedOut string) error {
	if generate != "" {
		if public == "" {
			return fmt.Errorf("public-key is required")
		}
		pub, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(generate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, err = f.WriteString(base64.StdEncoding.EncodeToString(private.Seed()))
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		return os.WriteFile(public, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0644)
	}
	if _, err := appversion.Parse(version); err != nil {
		return err
	}
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("UPDATE_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		return fmt.Errorf("UPDATE_SIGNING_KEY must contain a base64 Ed25519 seed")
	}
	key := ed25519.NewKeyFromSeed(seed)
	pubData, err := os.ReadFile(public)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(pubData)) != base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)) {
		return fmt.Errorf("signing key does not match the embedded public key")
	}
	data, err := os.ReadFile(binary)
	if err != nil {
		return err
	}
	if len(data) == 0 || int64(len(data)) > updatemanifest.MaxSize {
		return fmt.Errorf("invalid executable size")
	}
	var packed []byte
	if packedOut != "" {
		if packed, err = compress(data); err != nil {
			return err
		}
	}
	manifest, err := updatemanifest.Sign(version, data, packed, key)
	if err != nil {
		return err
	}
	if _, err = updatemanifest.Verify(manifest, key.Public().(ed25519.PublicKey)); err != nil {
		return fmt.Errorf("generated manifest failed verification: %w", err)
	}
	if packed != nil {
		if err := os.WriteFile(packedOut, packed, 0644); err != nil {
			return err
		}
	}
	return os.WriteFile(out, manifest, 0644)
}

// compress packs the executable with zstd and checks the round trip, so a
// broken archive is never published next to a valid signature.
func compress(data []byte) ([]byte, error) {
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithWindowSize(updatemanifest.PackedWindow))
	if err != nil {
		return nil, err
	}
	packed := encoder.EncodeAll(data, nil)
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxWindow(updatemanifest.PackedWindow))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	unpacked, err := decoder.DecodeAll(packed, nil)
	if err != nil {
		return nil, fmt.Errorf("verify compressed executable: %w", err)
	}
	if !bytes.Equal(unpacked, data) {
		return nil, fmt.Errorf("compressed executable does not match the original")
	}
	if int64(len(packed)) > updatemanifest.MaxSize {
		return nil, fmt.Errorf("invalid compressed executable size")
	}
	return packed, nil
}
