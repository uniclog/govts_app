//go:build !windows

package clientsettings

import "errors"

// ProtectSecret has no protected store outside Windows, so passwords are not kept.
func ProtectSecret(secret string) (string, error) {
	if secret == "" {
		return "", nil
	}
	return "", errors.New("secret storage is unavailable on this platform")
}

func UnprotectSecret(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	return "", errors.New("secret storage is unavailable on this platform")
}
