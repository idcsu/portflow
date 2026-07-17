package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const (
	passwordCost      = 12
	MinPasswordLength = 12
	MaxPasswordLength = 128
)

func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), passwordCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return errors.New("password must contain at least 12 characters")
	}
	if len(password) > MaxPasswordLength {
		return errors.New("password must contain no more than 128 characters")
	}
	return nil
}

func NormalizeUsername(username string) (string, error) {
	username = strings.ToLower(strings.TrimSpace(username))
	if len(username) < 3 || len(username) > 32 {
		return "", errors.New("username must contain 3 to 32 characters")
	}
	for _, character := range username {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' {
			return "", errors.New("username may only contain letters, numbers, underscore, and hyphen")
		}
	}
	return username, nil
}

func NewToken(bytes int) (string, error) {
	value := make([]byte, bytes)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func NewID(prefix string) (string, error) {
	token, err := NewToken(12)
	if err != nil {
		return "", err
	}
	return prefix + "_" + token, nil
}
