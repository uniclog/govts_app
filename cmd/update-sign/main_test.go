package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"

	"uniclog.io/sonoryx/internal/updatemanifest"
)

func TestSignMacArchiveWritesVerifiableManifest(t *testing.T) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	archive := filepath.Join(dir, updatemanifest.MacFilename)
	out := filepath.Join(dir, updatemanifest.MacAssetName)
	content := []byte("PK\x03\x04 archive body")
	if err := os.WriteFile(archive, content, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := signMacArchive("2.0.0", archive, out, private); err != nil {
		t.Fatalf("signMacArchive: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	m, err := updatemanifest.VerifyAs(data, public, updatemanifest.MacFilename)
	if err != nil {
		t.Fatalf("VerifyAs: %v", err)
	}
	if m.Version != "2.0.0" || m.Size != int64(len(content)) {
		t.Fatalf("unexpected manifest: %+v", m)
	}
}

func TestSignMacArchiveRejectsNonZip(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for name, content := range map[string][]byte{"empty": nil, "dmg": []byte("koly not a zip"), "short": []byte("PK")} {
		archive := filepath.Join(dir, name)
		if err := os.WriteFile(archive, content, 0o644); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(dir, name+".signature")
		if err := signMacArchive("2.0.0", archive, out, private); err == nil {
			t.Errorf("%s: signMacArchive accepted a non-zip archive", name)
		}
		if _, err := os.Stat(out); err == nil {
			t.Errorf("%s: manifest written for a rejected archive", name)
		}
	}
}
