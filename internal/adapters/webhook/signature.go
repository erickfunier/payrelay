package webhook

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
)

// Sign produces the value for X-PayRelay-Signature. Receivers should verify
// HMAC_SHA256("timestamp.payload", endpoint_secret) before accepting a request.
func Sign(secret string, payload []byte, unixTimestamp int64) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(unixTimestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func Verify(secret string, payload []byte, unixTimestamp int64, signature string) bool {
	return hmac.Equal([]byte(Sign(secret, payload, unixTimestamp)), []byte(signature))
}
