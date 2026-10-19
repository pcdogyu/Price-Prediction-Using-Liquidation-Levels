package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"strings"
)

const Salt = "Yuhao@jiansutech.com"

type TokenService struct {
	secret string
}

func NewTokenService(secret string) TokenService {
	return TokenService{secret: secret}
}

func (s TokenService) Generate(payload string) string {
	mac := hmac.New(sha256.New, []byte(s.secret))
	mac.Write([]byte(Salt))
	mac.Write([]byte("\n"))
	mac.Write([]byte(payload))
	hexToken := hex.EncodeToString(mac.Sum(nil))
	return base64.StdEncoding.EncodeToString([]byte(hexToken))
}

func (s TokenService) Validate(payload, token string) bool {
	token = strings.TrimSpace(token)
	if token == "" {
		return false
	}
	expected := s.Generate(payload)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(token)) == 1
}
