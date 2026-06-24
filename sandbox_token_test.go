package e2b

import (
	"encoding/hex"
	"testing"
)

func TestNewRandomTokenUsesCryptoSizedSecret(t *testing.T) {
	first := newRandomToken()
	second := newRandomToken()
	if first == second {
		t.Fatal("expected distinct tokens")
	}
	if len(first) != 64 {
		t.Fatalf("token length = %d", len(first))
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("token is not hex: %v", err)
	}
}
