package auth

import (
	"bytes"
	"testing"
	"time"
)

func TestTOTPCodeAndValidation(t *testing.T) {
	// RFC 6238 SHA-1 test secret; the RFC's 8-digit value at 59 seconds is 94287082.
	secret := "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	at := time.Unix(59, 0).UTC()
	code, err := TOTPCode(secret, at)
	if err != nil {
		t.Fatal(err)
	}
	if code != "287082" {
		t.Fatalf("unexpected six-digit code: %s", code)
	}
	if !ValidateTOTP(secret, code, at) || !ValidateTOTP(secret, code, at.Add(TOTPPeriod)) {
		t.Fatal("current and adjacent time windows should validate")
	}
	if ValidateTOTP(secret, "287083", at) || ValidateTOTP(secret, "abc", at) {
		t.Fatal("invalid codes must be rejected")
	}
}

func TestSecretProtectorRoundTrip(t *testing.T) {
	protector, err := NewSecretProtector(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := protector.Encrypt("TOP-SECRET")
	if err != nil {
		t.Fatal(err)
	}
	if encrypted == "TOP-SECRET" {
		t.Fatal("secret was stored as plain text")
	}
	decrypted, err := protector.Decrypt(encrypted)
	if err != nil || decrypted != "TOP-SECRET" {
		t.Fatalf("round trip failed: value=%q err=%v", decrypted, err)
	}
	if _, err := protector.Decrypt(encrypted + "broken"); err == nil {
		t.Fatal("tampered ciphertext should fail authentication")
	}
}

func TestRecoveryCodesAreUniqueAndNormalized(t *testing.T) {
	codes, err := NewRecoveryCodes(8)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, code := range codes {
		hash := RecoveryCodeHash(code)
		if seen[hash] || hash != RecoveryCodeHash(NormalizeRecoveryCode(code)) {
			t.Fatalf("invalid recovery code behavior for %q", code)
		}
		seen[hash] = true
	}
}
