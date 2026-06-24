package e2b

import (
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

func TestGetSignatureWithoutExpiration(t *testing.T) {
	user := "user"
	got, err := GetSignature("/tmp/file", "read", &user, "token", nil)
	if err != nil {
		t.Fatalf("GetSignature: %v", err)
	}
	sum := sha256.Sum256([]byte("/tmp/file:read:user:token"))
	want := base64.StdEncoding.EncodeToString(sum[:])
	for len(want) > 0 && want[len(want)-1] == '=' {
		want = want[:len(want)-1]
	}
	want = "v1_" + want
	if got.Signature != want {
		t.Fatalf("signature = %q, want %q", got.Signature, want)
	}
	if got.Expiration != nil {
		t.Fatalf("expiration = %v", *got.Expiration)
	}
}
