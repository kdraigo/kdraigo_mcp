package auth

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
)

type Signer struct {
	keyID   string
	privKey ed25519.PrivateKey
}

func NewSigner(keyID, privateKeyHex string) (*Signer, error) {
	raw, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return nil, fmt.Errorf("decode private key hex: %w", err)
	}
	if len(raw) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("private key must be %d bytes, got %d", ed25519.PrivateKeySize, len(raw))
	}
	return &Signer{keyID: keyID, privKey: ed25519.PrivateKey(raw)}, nil
}

func (s *Signer) KeyID() string { return s.keyID }

// Sign returns hex(ed25519(METHOD\nPATH\nTIMESTAMP\nBODY)).
// Mirrors lib/auth.Sign on the backend.
func (s *Signer) Sign(method, path, timestamp, body string) string {
	payload := fmt.Sprintf("%s\n%s\n%s\n%s", method, path, timestamp, body)
	sig := ed25519.Sign(s.privKey, []byte(payload))
	return hex.EncodeToString(sig)
}
