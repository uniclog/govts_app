package session

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/identity"
)

var accountID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)

type Account struct {
	ID           string `json:"-"`
	Name         string `json:"name"`
	Digest       string `json:"tokenSHA256"`
	identityPath string
	digest       [32]byte
}

func validName(name string) bool {
	return name == strings.TrimSpace(name) && name != "" && utf8.ValidString(name) && len(name) <= domain.MaxParticipantNameBytes && !strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' })
}

func NewToken() string {
	var secret [32]byte
	rand.Read(secret[:])
	return base64.RawURLEncoding.EncodeToString(secret[:])
}

func Provision(dir, id, name string) (string, error) {
	if !accountID.MatchString(id) || !validName(name) {
		return "", fmt.Errorf("invalid account ID or display name")
	}
	path := filepath.Join(dir, "accounts", id)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(filepath.Dir(path), ".provision-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(staging)
	if _, err := identity.LoadOrCreate(filepath.Join(staging, "identity")); err != nil {
		return "", err
	}
	token := NewToken()
	digest := sha256.Sum256([]byte(token))
	data, err := json.Marshal(Account{Name: name, Digest: hex.EncodeToString(digest[:])})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staging, "account.json"), data, 0600); err != nil {
		return "", err
	}
	// An account directory is published atomically. An existing, populated
	// account directory cannot be replaced by the rename.
	if err := os.Rename(staging, path); err != nil {
		return "", err
	}
	return token, nil
}

func LoadAccounts(dir string) (map[string]Account, error) {
	root := filepath.Join(dir, "accounts")
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	accounts := make(map[string]Account)
	for _, entry := range entries {
		id := entry.Name()
		if !entry.IsDir() || !accountID.MatchString(id) {
			continue
		}
		path := filepath.Join(root, id)
		data, err := os.ReadFile(filepath.Join(path, "account.json"))
		if err != nil {
			return nil, fmt.Errorf("read pilot account %q: %w", id, err)
		}
		var a Account
		if err := json.Unmarshal(data, &a); err != nil || !validName(a.Name) {
			return nil, fmt.Errorf("invalid pilot account %q", id)
		}
		digest, err := hex.DecodeString(a.Digest)
		if err != nil || len(digest) != len(a.digest) {
			return nil, fmt.Errorf("invalid credential digest for %q", id)
		}
		a.identityPath = filepath.Join(path, "identity")
		seed, err := os.ReadFile(a.identityPath)
		if err != nil {
			return nil, fmt.Errorf("read identity for %q: %w", id, err)
		}
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(seed)))
		if err != nil || len(decoded) != 32 {
			return nil, fmt.Errorf("invalid identity for %q", id)
		}
		a.ID = id
		copy(a.digest[:], digest)
		accounts[id] = a
	}
	return accounts, nil
}

func (a Account) Accepts(token string) bool {
	digest := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(digest[:], a.digest[:]) == 1 && a.ID != ""
}
