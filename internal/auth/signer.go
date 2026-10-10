package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
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

// Sign returns hex(ed25519(METHOD\nPATH\nTIMESTAMP\nBODY)): request signing
// version 1. Kept for reference; requests are signed with SignV2.
func (s *Signer) Sign(method, path, timestamp, body string) string {
	payload := fmt.Sprintf("%s\n%s\n%s\n%s", method, path, timestamp, body)
	sig := ed25519.Sign(s.privKey, []byte(payload))
	return hex.EncodeToString(sig)
}

// Request signing version 2, a copy of lib/auth/signing_v2.go (this module
// does not import lib). It also signs the query string and a one-time nonce,
// so a captured signature cannot be pointed at other parameters or sent
// again:
//
//	KDRAIGO-SIG-V2\nMETHOD\nPATH\nQUERY\nTIMESTAMP\nNONCE\nhex(SHA256(BODY))
//
// testdata/signature_v2.json is the same file as lib's; signer_test.go keeps
// the two in step.

const signatureV2Tag = "KDRAIGO-SIG-V2"

// credentialParams are never part of the signed query.
var credentialParams = map[string]bool{"key_id": true, "signature": true, "timestamp": true, "nonce": true}

// SignV2 returns the version 2 signature of a request. query may be nil.
func (s *Signer) SignV2(method, path string, query url.Values, timestamp, nonce string, body []byte) string {
	return hex.EncodeToString(ed25519.Sign(s.privKey, []byte(canonicalV2(method, path, query, timestamp, nonce, body))))
}

func canonicalV2(method, path string, query url.Values, timestamp, nonce string, body []byte) string {
	signed := url.Values{}
	for k, vs := range query {
		if !credentialParams[k] {
			signed[k] = vs
		}
	}
	sum := sha256.Sum256(body)
	return strings.Join([]string{signatureV2Tag, method, path, signed.Encode(), timestamp, nonce, hex.EncodeToString(sum[:])}, "\n")
}

// NewNonce returns a fresh nonce: 16 random bytes in hex.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
