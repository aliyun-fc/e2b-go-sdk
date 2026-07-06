package e2b

import (
	"encoding/hex"
	"testing"
)

func TestNewRandomTokenUsesCryptoSizedSecret(t *testing.T) {
	first, err := newRandomToken()
	if err != nil {
		t.Fatalf("newRandomToken: %v", err)
	}
	second, err := newRandomToken()
	if err != nil {
		t.Fatalf("newRandomToken: %v", err)
	}
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
