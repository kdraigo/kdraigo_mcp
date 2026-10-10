package auth

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"testing"
)

// testdata/signature_v2.json is lib/auth's file. If this fails, the server and
// this client sign differently.
func TestSignV2_SharedFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/signature_v2.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		SeedHex string `json:"seed_hex"`
		Vectors []struct {
			Name      string              `json:"name"`
			Method    string              `json:"method"`
			Path      string              `json:"path"`
			Query     map[string][]string `json:"query"`
			Timestamp string              `json:"timestamp"`
			Nonce     string              `json:"nonce"`
			Body      string              `json:"body"`
			Canonical string              `json:"canonical"`
			Signature string              `json:"signature"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	seed, _ := hex.DecodeString(fx.SeedHex)
	priv := ed25519.NewKeyFromSeed(seed)
	s, err := NewSigner("k", hex.EncodeToString(priv))
	if err != nil {
		t.Fatal(err)
	}
	if len(fx.Vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range fx.Vectors {
		q := url.Values(v.Query)
		if got := canonicalV2(v.Method, v.Path, q, v.Timestamp, v.Nonce, []byte(v.Body)); got != v.Canonical {
			t.Errorf("%s: canonical\n got %q\nwant %q", v.Name, got, v.Canonical)
		}
		if got := s.SignV2(v.Method, v.Path, q, v.Timestamp, v.Nonce, []byte(v.Body)); got != v.Signature {
			t.Errorf("%s: signature differs", v.Name)
		}
	}
}

func TestNewNonce(t *testing.T) {
	a, err := NewNonce()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewNonce()
	if len(a) != 32 || a == b {
		t.Fatalf("nonces %q %q", a, b)
	}
}
