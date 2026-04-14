package handlers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

const loginCaptchaTTL = 20 * time.Minute

type loginCaptchaPayload struct {
	Expr string `json:"e"`
	Ans  string `json:"a"`
	Exp  int64  `json:"x"`
}

// signLoginCaptchaToken builds a tamper-proof captcha token (no server session required).
func signLoginCaptchaToken(secret, expression, answer string) (string, error) {
	p := loginCaptchaPayload{
		Expr: expression,
		Ans:  strings.TrimSpace(answer),
		Exp:  time.Now().Add(loginCaptchaTTL).Unix(),
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(raw) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func verifyLoginCaptchaToken(secret, token, userAnswer string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return false
	}
	raw, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	sig, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	if err1 != nil || err2 != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(raw)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return false
	}
	var p loginCaptchaPayload
	if json.Unmarshal(raw, &p) != nil {
		return false
	}
	if time.Now().Unix() > p.Exp {
		return false
	}
	return strings.TrimSpace(userAnswer) == strings.TrimSpace(p.Ans)
}
