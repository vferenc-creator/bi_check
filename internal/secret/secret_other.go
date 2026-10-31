//go:build !windows

package secret

import (
	"encoding/base64"
	"errors"
	"strings"
)

// Development only (the application itself runs on Windows).
func protect(b []byte) ([]byte, error) {
	return []byte("dev:" + base64.StdEncoding.EncodeToString(b)), nil
}

func unprotect(b []byte) ([]byte, error) {
	s, ok := strings.CutPrefix(string(b), "dev:")
	if !ok {
		return nil, errors.New("unknown format")
	}
	return base64.StdEncoding.DecodeString(s)
}
