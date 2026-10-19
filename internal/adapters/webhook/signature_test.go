package webhook

import "testing"

func TestSignature_GivenPayloadAndSecret_WhenVerified_ThenAcceptsOnlyUntamperedRequest(t *testing.T) {
	payload := []byte(`{"id":"evt_test","amount_cents":1250}`)
	signature := Sign("whsec_test", payload, 1735689600)
	cases := []struct {
		name    string
		secret  string
		payload []byte
		want    bool
	}{
		{name: "given_matching_secret_and_payload_when_verified_then_accepts", secret: "whsec_test", payload: payload, want: true},
		{name: "given_tampered_payload_when_verified_then_rejects", secret: "whsec_test", payload: []byte(`{"tampered":true}`), want: false},
		{name: "given_different_secret_when_verified_then_rejects", secret: "another_secret", payload: payload, want: false},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := Verify(tt.secret, tt.payload, 1735689600, signature); got != tt.want {
				t.Fatalf("Verify() = %v, want %v", got, tt.want)
			}
		})
	}
}
