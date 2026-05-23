package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestComputeSignature(t *testing.T) {
	key := []byte("test-secret")
	message := []byte(`{"action":"push"}`)

	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	expectedHex := hex.EncodeToString(mac.Sum(nil))

	got := ComputeSignature(message, key)
	want := "sha256=" + expectedHex

	if got != want {
		t.Errorf("ComputeSignature() = %q, want %q", got, want)
	}
}

func TestComputeSignature_DifferentKeysProduceDifferentResults(t *testing.T) {
	message := []byte("same message")
	sig1 := ComputeSignature(message, []byte("key1"))
	sig2 := ComputeSignature(message, []byte("key2"))

	if sig1 == sig2 {
		t.Error("different keys should produce different signatures")
	}
}

func TestVerifySignature(t *testing.T) {
	key := []byte("test-secret")
	message := []byte(`{"ref":"refs/heads/main"}`)

	tests := []struct {
		name    string
		header  string
		wantErr bool
	}{
		{
			name:    "valid signature",
			header:  ComputeSignature(message, key),
			wantErr: false,
		},
		{
			name:    "wrong signature",
			header:  "sha256=" + "00" + ComputeSignature(message, key)[len("sha256=")+2:],
			wantErr: true,
		},
		{
			name:    "empty header",
			header:  "",
			wantErr: true,
		},
		{
			name:    "missing sha256= prefix",
			header:  "abcdef1234567890",
			wantErr: true,
		},
		{
			name:    "sha1 prefix rejected",
			header:  "sha1=abcdef1234567890abcdef1234567890abcdef12",
			wantErr: true,
		},
		{
			name:    "invalid hex after prefix",
			header:  "sha256=not-valid-hex",
			wantErr: true,
		},
		{
			name:    "wrong length hex",
			header:  "sha256=abcdef",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := VerifySignature(message, key, tt.header)
			if (err != nil) != tt.wantErr {
				t.Errorf("VerifySignature() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
