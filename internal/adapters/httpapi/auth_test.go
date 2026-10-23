package httpapi

import "testing"

func TestAPIKeyAuthenticator_GivenConfiguration_WhenAuthenticating_ThenResolvesOnlyMatchingTenant(t *testing.T) {
	authenticator, err := NewAPIKeyAuthenticator("merchant_alpha:prk_alpha_0123456789abcdef0123456789,merchant_beta:prk_beta_0123456789abcdef01234567890")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name          string
		authorization string
		wantTenant    string
		wantAllowed   bool
	}{
		{name: "Given alpha key When authenticating Then returns alpha tenant", authorization: "Bearer prk_alpha_0123456789abcdef0123456789", wantTenant: "merchant_alpha", wantAllowed: true},
		{name: "Given missing Bearer scheme When authenticating Then rejects", authorization: "prk_alpha_0123456789abcdef0123456789"},
		{name: "Given unknown key When authenticating Then rejects", authorization: "Bearer prk_unknown_0123456789abcdef012345678"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tenantID, allowed := authenticator.Authenticate(tt.authorization)
			if tenantID != tt.wantTenant || allowed != tt.wantAllowed {
				t.Fatalf("Authenticate() = (%q, %v), want (%q, %v)", tenantID, allowed, tt.wantTenant, tt.wantAllowed)
			}
		})
	}
}

func TestAPIKeyAuthenticator_GivenInvalidConfiguration_WhenCreated_ThenReturnsValidationError(t *testing.T) {
	for _, configuration := range []string{"", "merchant:short", "merchant alpha:prk_alpha_0123456789abcdef0123456789", "merchant"} {
		if _, err := NewAPIKeyAuthenticator(configuration); err == nil {
			t.Fatalf("NewAPIKeyAuthenticator(%q) error = nil, want validation error", configuration)
		}
	}
}
