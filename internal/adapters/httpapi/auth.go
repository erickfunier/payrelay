package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"strings"
)

// TenantAuthenticator resolves a Bearer credential to its tenant boundary.
// It belongs to the inbound HTTP adapter: application use cases receive only
// the resulting tenant ID and never inspect transport credentials.
type TenantAuthenticator interface {
	Authenticate(string) (string, bool)
}

type apiCredential struct {
	tenantID string
	digest   [sha256.Size]byte
}

// APIKeyAuthenticator accepts PAYRELAY_API_KEYS in tenant:api-key pairs,
// separated by commas. Keys are retained only as SHA-256 digests in memory.
type APIKeyAuthenticator struct {
	credentials []apiCredential
}

func NewAPIKeyAuthenticator(configuration string) (*APIKeyAuthenticator, error) {
	entries := strings.Split(strings.TrimSpace(configuration), ",")
	authenticator := &APIKeyAuthenticator{}
	for _, entry := range entries {
		parts := strings.SplitN(strings.TrimSpace(entry), ":", 2)
		if len(parts) != 2 || !validTenantID(parts[0]) || len(strings.TrimSpace(parts[1])) < 20 {
			return nil, errors.New("PAYRELAY_API_KEYS must contain comma-separated tenant:api-key pairs; API keys must be at least 20 characters")
		}
		key := strings.TrimSpace(parts[1])
		authenticator.credentials = append(authenticator.credentials, apiCredential{tenantID: parts[0], digest: sha256.Sum256([]byte(key))})
	}
	if len(authenticator.credentials) == 0 {
		return nil, errors.New("PAYRELAY_API_KEYS must configure at least one tenant API key")
	}
	return authenticator, nil
}

func (a *APIKeyAuthenticator) Authenticate(authorization string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return "", false
	}
	digest := sha256.Sum256([]byte(strings.TrimSpace(strings.TrimPrefix(authorization, prefix))))
	var tenantID string
	matched := 0
	for _, credential := range a.credentials {
		match := subtle.ConstantTimeCompare(digest[:], credential.digest[:])
		if match == 1 {
			tenantID = credential.tenantID
		}
		matched |= match
	}
	return tenantID, matched == 1
}

func validTenantID(value string) bool {
	if len(value) < 3 || len(value) > 64 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
