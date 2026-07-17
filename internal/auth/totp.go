package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" // TOTP authenticator applications standardize on HMAC-SHA1 by default.
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	TOTPPeriod = 30 * time.Second
	TOTPDigits = 6
)

type SecretProtector struct {
	aead cipher.AEAD
}

func NewSecretProtector(key []byte) (*SecretProtector, error) {
	if len(key) != 32 {
		return nil, errors.New("MFA encryption key must contain exactly 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &SecretProtector{aead: aead}, nil
}

func NewEncryptionKey() ([]byte, error) {
	key := make([]byte, 32)
	_, err := rand.Read(key)
	return key, err
}

func (protector *SecretProtector) Encrypt(value string) (string, error) {
	nonce := make([]byte, protector.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := protector.aead.Seal(nonce, nonce, []byte(value), nil)
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (protector *SecretProtector) Decrypt(value string) (string, error) {
	sealed, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(sealed) < protector.aead.NonceSize() {
		return "", errors.New("invalid encrypted MFA secret")
	}
	nonce := sealed[:protector.aead.NonceSize()]
	plain, err := protector.aead.Open(nil, nonce, sealed[protector.aead.NonceSize():], nil)
	if err != nil {
		return "", errors.New("unable to decrypt MFA secret")
	}
	return string(plain), nil
}

func NewTOTPSecret() (string, error) {
	value := make([]byte, 20)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(value), nil
}

func TOTPCode(secret string, at time.Time) (string, error) {
	normalized := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(secret), " ", ""))
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(normalized)
	if err != nil || len(key) == 0 {
		return "", errors.New("invalid TOTP secret")
	}
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(at.Unix()/int64(TOTPPeriod/time.Second)))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := binary.BigEndian.Uint32(digest[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", TOTPDigits, value%1000000), nil
}

func ValidateTOTP(secret, code string, at time.Time) bool {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != TOTPDigits {
		return false
	}
	if _, err := strconv.Atoi(code); err != nil {
		return false
	}
	for offset := -1; offset <= 1; offset++ {
		expected, err := TOTPCode(secret, at.Add(time.Duration(offset)*TOTPPeriod))
		if err == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

func NewRecoveryCodes(count int) ([]string, error) {
	result := make([]string, 0, count)
	for index := 0; index < count; index++ {
		raw, err := NewToken(9)
		if err != nil {
			return nil, err
		}
		raw = strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(raw, "-", ""), "_", ""))
		if len(raw) < 12 {
			index--
			continue
		}
		result = append(result, raw[:4]+"-"+raw[4:8]+"-"+raw[8:12])
	}
	return result, nil
}

func NormalizeRecoveryCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
}

func RecoveryCodeHash(code string) string {
	return TokenHash(NormalizeRecoveryCode(code))
}
