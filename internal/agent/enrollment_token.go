package agent

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxEnrollmentTokenFileSize = 4096

// ReadEnrollmentToken avoids exposing the one-time token in process arguments.
func ReadEnrollmentToken(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("token path is not a regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("token file permissions %04o allow group or other access; require 0600 or stricter", info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maxEnrollmentTokenFileSize+1))
	if err != nil {
		return "", err
	}
	if len(contents) > maxEnrollmentTokenFileSize {
		return "", errors.New("token file is too large")
	}
	token := strings.TrimSpace(string(contents))
	if token == "" {
		return "", errors.New("token file is empty")
	}
	return token, nil
}
