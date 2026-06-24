package e2b

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"time"
)

// Signature is a v1 sandbox file URL signature.
type Signature struct {
	Signature  string
	Expiration *int64
}

// GetSignature generates a Python SDK-compatible v1 sandbox file URL signature.
func GetSignature(path, operation string, user *string, envdAccessToken string, expirationInSeconds *int) (Signature, error) {
	if envdAccessToken == "" {
		return Signature{}, fmt.Errorf("access token is not set and signature cannot be generated")
	}
	username := ""
	if user != nil {
		username = *user
	}
	var expiration *int64
	raw := ""
	if expirationInSeconds != nil {
		value := time.Now().Unix() + int64(*expirationInSeconds)
		expiration = &value
		raw = fmt.Sprintf("%s:%s:%s:%s:%d", path, operation, username, envdAccessToken, value)
	} else {
		raw = fmt.Sprintf("%s:%s:%s:%s", path, operation, username, envdAccessToken)
	}
	sum := sha256.Sum256([]byte(raw))
	encoded := base64.StdEncoding.EncodeToString(sum[:])
	for len(encoded) > 0 && encoded[len(encoded)-1] == '=' {
		encoded = encoded[:len(encoded)-1]
	}
	return Signature{Signature: "v1_" + encoded, Expiration: expiration}, nil
}
