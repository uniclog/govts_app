package wailsui

import (
	"errors"
	"sort"
	"strings"

	"uniclog.io/sonoryx/internal/clientapp"
)

// connectionMu must be held. Verify the current TLS key immediately before
// publishing or subscribing, including callers that bypass the frontend check.
func (s *Service) verifiedMediaPin() (string, error) {
	identity, err := s.mediaServerIdentity()
	if err != nil {
		return "", err
	}
	if !identity.Trusted {
		return "", errors.New("media-key-confirmation-required")
	}
	return identity.Fingerprint, nil
}

func mediaTrustServerKey(server string) string {
	server = strings.TrimSpace(server)
	if endpoint, err := clientapp.ParseServerEndpoint(server); err == nil {
		return endpoint.String()
	}
	return server
}

// settingsMu must be held. Conflicting legacy aliases deliberately produce a
// value that cannot match a single TLS fingerprint, requiring confirmation.
func (s *Service) mediaPinStateLocked(server string) string {
	server = mediaTrustServerKey(server)
	unique := make(map[string]struct{})
	for address, fingerprint := range s.trustedMediaKeys {
		if fingerprint != "" && mediaTrustServerKey(address) == server {
			unique[fingerprint] = struct{}{}
		}
	}
	fingerprints := make([]string, 0, len(unique))
	for fingerprint := range unique {
		fingerprints = append(fingerprints, fingerprint)
	}
	sort.Strings(fingerprints)
	return strings.Join(fingerprints, "\n")
}
