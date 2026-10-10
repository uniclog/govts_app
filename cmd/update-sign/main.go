package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"strings"
	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/updatemanifest"
)

func main() {
	generate := flag.String("generate-key", "", "write a NEW private seed to this file (outside the repository)")
	public := flag.String("public-key", "", "public key file")
	binary := flag.String("binary", "", "desktop executable to sign")
	version := flag.String("version", appversion.ClientVersion, "desktop version (defaults to version/release.json)")
	out := flag.String("out", "", "signed manifest destination")
	flag.Parse()
	if err := run(*generate, *public, *binary, *version, *out); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(generate, public, binary, version, out string) error {
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
	manifest, err := updatemanifest.Sign(version, data, key)
	if err != nil {
		return err
	}
	if _, err = updatemanifest.Verify(manifest, key.Public().(ed25519.PublicKey)); err != nil {
		return fmt.Errorf("generated manifest failed verification: %w", err)
	}
	return os.WriteFile(out, manifest, 0644)
}
