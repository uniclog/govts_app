package clientcore

import "testing"

func TestParseServerEndpoint(t *testing.T) {
	for input, want := range map[string]string{
		"192.0.2.1":       "192.0.2.1:9000",
		"192.0.2.1:12345": "192.0.2.1:12345",
		"::1":             "[::1]:9000",
		"[::1]:12345":     "[::1]:12345",
	} {
		got, err := ParseServerEndpoint(input)
		if err != nil || got.String() != want {
			t.Fatalf("parse %q = %v, %v; want %s", input, got, err, want)
		}
	}
	for _, input := range []string{"", "invalid", "192.0.2.1:0", "192.0.2.1:65536", "192.0.2.1:-1", "192.0.2.1:"} {
		if _, err := ParseServerEndpoint(input); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
