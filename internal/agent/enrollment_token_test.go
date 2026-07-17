package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadEnrollmentToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("one-time-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := ReadEnrollmentToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if token != "one-time-token" {
		t.Fatalf("unexpected token %q", token)
	}
}

func TestReadEnrollmentTokenRejectsUnsafeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnrollmentToken(path); err == nil {
		t.Fatal("unsafe token file permissions accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maxEnrollmentTokenFileSize+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadEnrollmentToken(path); err == nil {
		t.Fatal("oversized token file accepted")
	}
}
