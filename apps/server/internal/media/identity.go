package media

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Identity struct {
	CertPath    string
	KeyPath     string
	Fingerprint string
}

func LoadOrCreateIdentity(certPath, keyPath string) (Identity, error) {
	if certPath == "" || keyPath == "" {
		return Identity{}, errors.New("certificate and key paths are required")
	}
	certExists := fileExists(certPath)
	keyExists := fileExists(keyPath)
	if certExists != keyExists {
		return Identity{}, errors.New("media certificate and key must either both exist or both be absent")
	}
	if !certExists {
		if err := createIdentity(certPath, keyPath); err != nil {
			return Identity{}, err
		}
	}
	certPEM, err := os.ReadFile(certPath)
	if err != nil {
		return Identity{}, err
	}
	block, _ := pem.Decode(certPEM)
	if block == nil || block.Type != "CERTIFICATE" {
		return Identity{}, errors.New("invalid media certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return Identity{}, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return Identity{}, err
	}
	keyBlock, _ := pem.Decode(keyPEM)
	if keyBlock == nil || keyBlock.Type != "EC PRIVATE KEY" {
		return Identity{}, errors.New("invalid media key PEM")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return Identity{}, err
	}
	if !key.PublicKey.Equal(cert.PublicKey) {
		return Identity{}, errors.New("media certificate and key do not match")
	}
	sum := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return Identity{CertPath: certPath, KeyPath: keyPath, Fingerprint: formatFingerprint(sum[:])}, nil
}

func createIdentity(certPath, keyPath string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return err
	}
	now := time.Now()
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Sonoryx media server"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	if err := atomicWritePrivate(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})); err != nil {
		return fmt.Errorf("write media key: %w", err)
	}
	if err := atomicWritePrivate(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return fmt.Errorf("write media certificate: %w", err)
	}
	return nil
}

func atomicWritePrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".sonoryx-identity-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if closeErr := temp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func fileExists(path string) bool { _, err := os.Stat(path); return err == nil }
func formatFingerprint(value []byte) string {
	encoded := strings.ToUpper(hex.EncodeToString(value))
	parts := make([]string, 0, len(encoded)/2)
	for i := 0; i < len(encoded); i += 2 {
		parts = append(parts, encoded[i:i+2])
	}
	return strings.Join(parts, ":")
}
