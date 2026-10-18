package delivery

import "testing"

func TestSignAndVerify(t *testing.T) {
	payload := []byte(`{"id":"evt_test","amount_cents":1250}`)
	signature := Sign("whsec_test", payload, 1735689600)
	if !Verify("whsec_test", payload, 1735689600, signature) {
		t.Fatal("expected valid signature")
	}
	if Verify("whsec_test", []byte(`{"tampered":true}`), 1735689600, signature) {
		t.Fatal("accepted a modified payload")
	}
	if Verify("another_secret", payload, 1735689600, signature) {
		t.Fatal("accepted a modified secret")
	}
}
