package proxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

func ComputeSignature(message, key []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func VerifySignature(message, key []byte, header string) error {
	if header == "" {
		return errors.New("missing signature header")
	}

	if strings.HasPrefix(header, "sha1=") {
		return errors.New("sha1 signatures are not accepted, use sha256")
	}

	if !strings.HasPrefix(header, "sha256=") {
		return errors.New("signature header must start with sha256=")
	}

	sigHex := header[len("sha256="):]
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("invalid hex in signature: %w", err)
	}

	if len(sigBytes) != sha256.Size {
		return errors.New("signature has wrong length")
	}

	mac := hmac.New(sha256.New, key)
	mac.Write(message)
	expected := mac.Sum(nil)

	if !hmac.Equal(sigBytes, expected) {
		return errors.New("signature mismatch")
	}

	return nil
}
