// Package testsupport exposes the real client handshake to server integration tests.
package testsupport

import client "uniclog.io/sonoryx/internal/clientcore/internal/live"

type AuthenticationRejectedError = client.AuthenticationRejectedError
type SecureHandshakeResult = client.SecureHandshakeResult

var PerformSecureHandshakeAttempts = client.PerformSecureHandshakeAttempts
