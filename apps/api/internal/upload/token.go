package upload

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// Signed, not encrypted. Tokens contain no AWS credentials and must never be
// logged. Scope binds them to the configured bucket/region. V permits migration.
type claims struct {
	V         int    `json:"v"`
	ID        string `json:"id"`
	Key       string `json:"key"`
	AWSID     string `json:"awsId"`
	Scope     string `json:"scope"`
	Size      int64  `json:"size"`
	PartSize  int64  `json:"partSize"`
	Type      string `json:"type"`
	ExpiresAt int64  `json:"exp"`
}

func (s *Service) sign(c claims) (string, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	return payload + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Service) verify(a Access) (claims, error) {
	var c claims
	if len(a.Token) == 0 || len(a.Token) > MaxTokenBytes {
		return c, ErrToken
	}
	payload, signature, ok := strings.Cut(a.Token, ".")
	if !ok {
		return c, ErrToken
	}
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return c, ErrToken
	}
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write([]byte(payload))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return c, ErrToken
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || json.Unmarshal(data, &c) != nil {
		return claims{}, ErrToken
	}
	if c.V != 1 || c.ID == "" || c.ID != a.UploadID || c.Scope != s.scope || c.AWSID == "" ||
		c.Size <= 0 || c.Size > MaxUploadBytes || c.PartSize != PartSizeBytes ||
		!strings.HasPrefix(c.Key, "dev-uploads/"+c.ID+"/source.") ||
		!time.Unix(c.ExpiresAt, 0).After(s.now()) {
		return claims{}, ErrToken
	}
	return c, nil
}
