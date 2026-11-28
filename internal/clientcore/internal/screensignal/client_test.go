package screensignal

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestMediaEndpointErrorExplainsServerConfiguration(t *testing.T) {
	err := mediaEndpointError(netip.MustParseAddrPort("193.187.92.89:9002"), &net.DNSError{IsTimeout: true})
	message := err.Error()
	for _, want := range []string{"неверная конфигурация сервера", "193.187.92.89:9002", "TCP-порт", "firewall"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error %q does not contain %q", message, want)
		}
	}
}

func TestLegacyScreenCapabilitiesRequireActualHTTP404(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		legacy bool
	}{
		{"success", nil, false},
		{"old endpoint", &mediaHTTPError{statusCode: 404, status: "404 Not Found", body: "missing endpoint"}, true},
		{"wrapped 404", fmt.Errorf("capabilities: %w", &mediaHTTPError{statusCode: 404, status: "404 Not Found"}), true},
		{"500 containing misleading text", &mediaHTTPError{statusCode: 500, status: "500 Internal Server Error", body: "media signaling: 404 Not Found:"}, false},
		{"TLS failure", errors.New("media server identity changed"), false},
		{"transport error containing status text", errors.New("upstream error: media signaling: 404 Not Found:"), false},
		{"authorization failure", &mediaHTTPError{statusCode: 401, status: "401 Unauthorized"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := legacyMediaServer(test.err); got != test.legacy {
				t.Fatalf("legacy=%v want=%v", got, test.legacy)
			}
		})
	}
}

func TestMediaEndpointErrorPreservesUnexpectedCause(t *testing.T) {
	cause := errors.New("unexpected TLS failure")
	err := mediaEndpointError(netip.MustParseAddrPort("127.0.0.1:9002"), cause)
	if !errors.Is(err, cause) {
		t.Fatalf("error %v does not preserve cause", err)
	}
}
