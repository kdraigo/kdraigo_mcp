// Package hosted is kdraigo-mcp as a remote MCP server (kdraigo-mcp serve):
// claude.ai, ChatGPT and Gemini CLI connect to https://mcp.kdraigo.com/mcp and
// sign the user in through the platform's OAuth authorization server
// (users_service).
//
// Each request carries an OAuth access token. The server asks users_service
// what it is (introspection, cached briefly), and calls the platform on the
// user's behalf with an API key minted for that connected app: the token
// itself never leaves this server (MCP: no token passthrough). Disconnecting
// the app on the website revokes the token and the key.
package hosted

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// Scope is the authorization server's one scope.
const Scope = "kdraigo"

// Config places the hosted server.
type Config struct {
	Addr string
	// Resource is this server's canonical URL, the audience of its tokens:
	// https://mcp.kdraigo.com/mcp.
	Resource string
	// Issuer is the authorization server: https://mcp.kdraigo.com.
	Issuer string
	// UsersInternalURL is users_service's internal port (introspection and
	// key minting), with InternalToken.
	UsersInternalURL string
	InternalToken    string
}

// ConfigFromEnv reads the KDRAIGO_MCP_* variables.
func ConfigFromEnv() (Config, error) {
	c := Config{
		Addr:             env("KDRAIGO_MCP_ADDR", ":8090"),
		Resource:         os.Getenv("KDRAIGO_MCP_RESOURCE"),
		Issuer:           os.Getenv("KDRAIGO_MCP_ISSUER"),
		UsersInternalURL: strings.TrimRight(env("KDRAIGO_MCP_USERS_INTERNAL_URL", "http://users_service:4002"), "/"),
		InternalToken:    os.Getenv("KDRAIGO_MCP_INTERNAL_TOKEN"),
	}
	switch {
	case c.Resource == "" || c.Issuer == "":
		return c, errors.New("KDRAIGO_MCP_RESOURCE and KDRAIGO_MCP_ISSUER are required")
	case c.InternalToken == "":
		return c, errors.New("KDRAIGO_MCP_INTERNAL_TOKEN (users_service's INTERNAL_MINT_TOKEN) is required")
	}
	return c, nil
}

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// Identity is what an access token stands for.
type Identity struct {
	UserID   uuid.UUID
	GrantID  uuid.UUID
	ClientID string
	Expires  time.Time
}

var (
	// ErrInvalidToken: the token is unknown, expired, revoked, or for
	// another resource. The client must sign in again (401).
	ErrInvalidToken = errors.New("the access token is not valid for this server")
	// ErrUnavailable: users_service did not answer; the token may be fine.
	ErrUnavailable = errors.New("the platform's sign-in service is not available")
)

const (
	introspectTTL   = time.Minute
	negativeTTL     = 10 * time.Second
	maxCacheEntries = 10_000
)

// Introspector asks users_service what a token is, and remembers the answer
// for a minute: disconnecting an app takes effect within that.
type Introspector struct {
	url, token, resource string
	hc                   *http.Client
	now                  func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	id    *Identity
	until time.Time
}

func NewIntrospector(cfg Config) *Introspector {
	return &Introspector{url: cfg.UsersInternalURL + "/api/v1/internal/oauth/introspect", token: cfg.InternalToken,
		resource: cfg.Resource, hc: &http.Client{Timeout: 5 * time.Second}, now: time.Now, cache: map[string]cached{}}
}

// Check returns the identity behind a live token for this resource.
func (i *Introspector) Check(ctx context.Context, token string) (*Identity, error) {
	sum := sha256.Sum256([]byte(token))
	key := hex.EncodeToString(sum[:])
	now := i.now()
	i.mu.Lock()
	if c, ok := i.cache[key]; ok && now.Before(c.until) {
		i.mu.Unlock()
		if c.id == nil {
			return nil, ErrInvalidToken
		}
		return c.id, nil
	}
	i.mu.Unlock()

	id, err := i.ask(ctx, token)
	if errors.Is(err, ErrUnavailable) {
		return nil, err
	}
	until := now.Add(negativeTTL)
	if id != nil {
		until = now.Add(introspectTTL)
		if id.Expires.Before(until) {
			until = id.Expires
		}
	}
	i.mu.Lock()
	if len(i.cache) >= maxCacheEntries {
		for k, c := range i.cache {
			if !now.Before(c.until) {
				delete(i.cache, k)
			}
		}
		if len(i.cache) >= maxCacheEntries {
			i.cache = map[string]cached{}
		}
	}
	i.cache[key] = cached{id: id, until: until}
	i.mu.Unlock()
	if id == nil {
		return nil, ErrInvalidToken
	}
	return id, nil
}

func (i *Introspector) ask(ctx context.Context, token string) (*Identity, error) {
	body, _ := json.Marshal(map[string]string{"token": token})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", i.token)
	resp, err := i.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: introspection HTTP %d", ErrUnavailable, resp.StatusCode)
	}
	var r struct {
		Active   bool      `json:"active"`
		UserID   uuid.UUID `json:"user_id"`
		GrantID  uuid.UUID `json:"grant_id"`
		ClientID string    `json:"client_id"`
		Scope    string    `json:"scope"`
		Audience string    `json:"aud"`
		Exp      int64     `json:"exp"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	// Audience and scope are checked here too: a token for another resource
	// is never accepted (RFC 8707).
	if !r.Active || !sameResource(r.Audience, i.resource) || !hasScope(r.Scope, Scope) {
		return nil, nil
	}
	return &Identity{UserID: r.UserID, GrantID: r.GrantID, ClientID: r.ClientID, Expires: time.Unix(r.Exp, 0)}, nil
}

func hasScope(scopes, want string) bool {
	for _, s := range strings.Fields(scopes) {
		if s == want {
			return true
		}
	}
	return false
}

func sameResource(a, b string) bool {
	norm := func(s string) string {
		u, err := url.Parse(s)
		if err != nil {
			return s
		}
		u.Scheme, u.Host = strings.ToLower(u.Scheme), strings.ToLower(u.Host)
		return strings.TrimSuffix(u.String(), "/")
	}
	return a != "" && norm(a) == norm(b)
}

const (
	keyTTL         = 24 * time.Hour
	keyRenewBefore = time.Hour
	keyIdle        = 24 * time.Hour
)

// Keyring holds one minted API key per connected app, in memory only: a
// restart mints new ones. A key is renewed an hour before it expires.
type Keyring struct {
	url, token string
	hc         *http.Client
	now        func() time.Time

	mu    sync.Mutex
	keys  map[uuid.UUID]*grantKey
	locks map[uuid.UUID]*sync.Mutex
}

type grantKey struct {
	signer  *auth.Signer
	expires time.Time
	used    time.Time
}

func NewKeyring(cfg Config) *Keyring {
	return &Keyring{url: cfg.UsersInternalURL + "/api/v1/internal/api-keys", token: cfg.InternalToken,
		hc: &http.Client{Timeout: 10 * time.Second}, now: time.Now, keys: map[uuid.UUID]*grantKey{}, locks: map[uuid.UUID]*sync.Mutex{}}
}

// Signer returns the key that signs the connected app's calls, minting one
// when there is none or it is about to expire.
func (k *Keyring) Signer(ctx context.Context, id *Identity) (*auth.Signer, error) {
	k.mu.Lock()
	lock := k.locks[id.GrantID]
	if lock == nil {
		lock = &sync.Mutex{}
		k.locks[id.GrantID] = lock
	}
	k.mu.Unlock()
	lock.Lock()
	defer lock.Unlock()

	now := k.now()
	k.mu.Lock()
	gk := k.keys[id.GrantID]
	if gk != nil && gk.expires.Sub(now) > keyRenewBefore {
		gk.used = now
		k.mu.Unlock()
		return gk.signer, nil
	}
	k.mu.Unlock()

	signer, expires, err := k.mint(ctx, id)
	if err != nil {
		return nil, err
	}
	k.mu.Lock()
	k.keys[id.GrantID] = &grantKey{signer: signer, expires: expires, used: now}
	for g, other := range k.keys {
		if now.Sub(other.used) > keyIdle {
			delete(k.keys, g)
			delete(k.locks, g)
		}
	}
	k.mu.Unlock()
	return signer, nil
}

// Forget drops a connection's key (its grant was revoked).
func (k *Keyring) Forget(grantID uuid.UUID) {
	k.mu.Lock()
	delete(k.keys, grantID)
	k.mu.Unlock()
}

func (k *Keyring) mint(ctx context.Context, id *Identity) (*auth.Signer, time.Time, error) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, time.Time{}, err
	}
	body, _ := json.Marshal(map[string]any{
		"user_id": id.UserID, "public_key": hex.EncodeToString(pub), "kind": "mcp",
		"label": "AI app " + clientLabel(id.ClientID), "ttl_seconds": int64(keyTTL / time.Second), "grant_id": id.GrantID,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, k.url, bytes.NewReader(body))
	if err != nil {
		return nil, time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", k.token)
	resp, err := k.hc.Do(req)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusBadRequest:
		// The grant was revoked between introspection and now.
		return nil, time.Time{}, ErrInvalidToken
	case resp.StatusCode != http.StatusCreated:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, time.Time{}, fmt.Errorf("%w: minting a key: HTTP %d: %s", ErrUnavailable, resp.StatusCode, b)
	}
	var r struct {
		KeyID     uuid.UUID `json:"key_id"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil, time.Time{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	signer, err := auth.NewSigner(r.KeyID.String(), hex.EncodeToString(priv))
	if err != nil {
		return nil, time.Time{}, err
	}
	return signer, r.ExpiresAt, nil
}

// clientLabel is a short name for a client id in a key's label: a metadata
// URL's host, or the registered id.
func clientLabel(clientID string) string {
	if u, err := url.Parse(clientID); err == nil && u.Scheme == "https" {
		return u.Hostname()
	}
	if len(clientID) > 24 {
		return clientID[:24]
	}
	return clientID
}

// ProtectedResourceMetadata is this server's RFC 9728 document.
func (c Config) ProtectedResourceMetadata() map[string]any {
	return map[string]any{
		"resource":                 c.Resource,
		"authorization_servers":    []string{c.Issuer},
		"scopes_supported":         []string{Scope},
		"bearer_methods_supported": []string{"header"},
		"resource_name":            "kdraigo",
		"resource_documentation":   "https://kdraigo.com/docs",
	}
}

// metadataURL is where the document is served for this resource:
// /.well-known/oauth-protected-resource plus the resource's path.
func (c Config) metadataURL() string {
	u, err := url.Parse(c.Resource)
	if err != nil {
		return c.Resource
	}
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + strings.TrimSuffix(u.Path, "/")
}

// Handler serves the MCP endpoint behind OAuth, and the metadata clients
// discover it with. mcp is the MCP server's streamable HTTP handler.
func Handler(cfg Config, mcp http.Handler, intro *Introspector, keys *Keyring, logger *log.Logger) http.Handler {
	mux := http.NewServeMux()
	meta := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(w).Encode(cfg.ProtectedResourceMetadata())
	}
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", meta)
	if u, err := url.Parse(cfg.Resource); err == nil && strings.Trim(u.Path, "/") != "" {
		mux.HandleFunc("GET /.well-known/oauth-protected-resource"+strings.TrimSuffix(u.Path, "/"), meta)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	challenge := func(w http.ResponseWriter, errCode, desc string) {
		v := `Bearer resource_metadata="` + cfg.metadataURL() + `", scope="` + Scope + `"`
		if errCode != "" {
			v = `Bearer error="` + errCode + `", error_description="` + desc + `", resource_metadata="` + cfg.metadataURL() + `", scope="` + Scope + `"`
		}
		w.Header().Set("WWW-Authenticate", v)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
	path := "/mcp"
	if u, err := url.Parse(cfg.Resource); err == nil && u.Path != "" {
		path = u.Path
	}
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || strings.TrimSpace(token) == "" {
			challenge(w, "", "")
			return
		}
		id, err := intro.Check(r.Context(), strings.TrimSpace(token))
		if errors.Is(err, ErrInvalidToken) {
			challenge(w, "invalid_token", "The access token is expired, revoked or not for this server")
			return
		}
		if err != nil {
			logger.Printf("introspection: %v", err)
			http.Error(w, "the platform's sign-in service is not available; try again shortly", http.StatusServiceUnavailable)
			return
		}
		signer, err := keys.Signer(r.Context(), id)
		if errors.Is(err, ErrInvalidToken) {
			challenge(w, "invalid_token", "The connection was revoked")
			return
		}
		if err != nil {
			logger.Printf("key for grant %s: %v", id.GrantID, err)
			http.Error(w, "the platform is not available; try again shortly", http.StatusServiceUnavailable)
			return
		}
		mcp.ServeHTTP(w, r.WithContext(client.WithSigner(r.Context(), signer)))
	})
	return mux
}
