//go:build windows

package clientsettings

import (
	"strings"
	"testing"
)

func TestProtectSecretRoundTrip(t *testing.T) {
	if value, err := ProtectSecret(""); err != nil || value != "" {
		t.Fatalf("ProtectSecret(\"\") = %q, %v", value, err)
	}
	protected, err := ProtectSecret("пароль сервера")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(protected, "dpapi:") || strings.Contains(protected, "пароль") {
		t.Fatalf("protected value = %q", protected)
	}
	plain, err := UnprotectSecret(protected)
	if err != nil || plain != "пароль сервера" {
		t.Fatalf("UnprotectSecret() = %q, %v", plain, err)
	}
	for _, value := range []string{"plain", "dpapi:not base64!", "dpapi:", "dpapi:AAAA"} {
		if _, err := UnprotectSecret(value); err == nil {
			t.Errorf("UnprotectSecret(%q) accepted", value)
		}
	}
}
