package utils

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"

	"golang.org/x/crypto/bcrypt"
)

func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func VerifyPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func HashHTPasswdPassword(password string) (string, error) {
	salt := make([]byte, 8)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	sum := sha1.Sum(append([]byte(password), salt...))
	payload := append(sum[:], salt...)
	return "{SSHA}" + base64.StdEncoding.EncodeToString(payload), nil
}
