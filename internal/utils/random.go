package utils

import (
	"crypto/rand"
	"encoding/base64"
)

func GeneratePassword() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
