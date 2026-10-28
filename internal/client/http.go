package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
)

// ServicePrefix routes to a service behind the kdraigo.com gateway.
type ServicePrefix string

const (
	// Backtester routes directly to the backtester_engine host (api.kdraigo.com),
	// which forwards /api/v1/dev/* verbatim — no gateway prefix. The kdraigo.com
	// gateway has no /backtester location and 405s the session POST (falls through
	// to the SPA static handler), so the backtester uses its own base URL.
	Backtester  ServicePrefix = ""
	FrontendAPI ServicePrefix = "/frontend-api"
	Analytics   ServicePrefix = "/analytics"
	Data        ServicePrefix = "/data"
	// Workspace is workspace_service: the cloud workspace that builds and runs
	// a strategy on a sandbox machine.
	Workspace ServicePrefix = "/workspace"
	// Research is research_service: the user's research labs.
	Research ServicePrefix = "/research"
)

// HeaderStyle selects which header names carry the Ed25519 signature.
// The backtester_engine controller predates the lib/auth convention and uses uppercased names.
type HeaderStyle int

const (
	HeaderStyleStandard   HeaderStyle = iota // X-Key-ID / X-Signature / X-Timestamp
	HeaderStyleBacktester                    // X-API-KEY / X-SIGNATURE / X-TIMESTAMP
)

type HTTP struct {
	endpoint           string
	backtesterEndpoint string
	signer             *auth.Signer
	hc                 *http.Client
}

func NewHTTP(endpoint, backtesterEndpoint string, signer *auth.Signer) *HTTP {
	return &HTTP{
		endpoint:           strings.TrimRight(endpoint, "/"),
		backtesterEndpoint: strings.TrimRight(backtesterEndpoint, "/"),
		signer:             signer,
		// No client-wide timeout: each tool bounds its own call, and a
		// report over a long session can take longer than a page of orders.
		hc: &http.Client{},
	}
}

// Endpoint returns the gateway base URL (data/analytics/frontend-api services).
func (h *HTTP) Endpoint() string { return h.endpoint }

// BacktesterEndpoint returns the backtester_engine base URL (used by the WS client).
func (h *HTTP) BacktesterEndpoint() string { return h.backtesterEndpoint }

// baseFor picks the correct base URL for a service prefix.
func (h *HTTP) baseFor(svc ServicePrefix) string {
	if svc == Backtester {
		return h.backtesterEndpoint
	}
	return h.endpoint
}

type signerKey struct{}

// WithSigner returns ctx carrying the key to sign this call's requests with:
// in the hosted server, the key minted for the user's connected app. Without
// one the client's own key (stdio, ~/.kdraigo/config.yaml) signs.
func WithSigner(ctx context.Context, s *auth.Signer) context.Context {
	return context.WithValue(ctx, signerKey{}, s)
}

// SignerFor returns the key that signs a call made with ctx.
func (h *HTTP) SignerFor(ctx context.Context) (*auth.Signer, error) {
	if s, ok := ctx.Value(signerKey{}).(*auth.Signer); ok && s != nil {
		return s, nil
	}
	if h.signer == nil {
		return nil, errors.New("no API key to sign the request with")
	}
	return h.signer, nil
}

// Do issues a JSON request. `style` chooses header naming; pass HeaderStyleStandard
// for the lib/auth path or HeaderStyleBacktester for the backtester_engine controller.
// `signed=false` skips auth headers (for data_provider).
// `query` may be nil. `body` may be nil.
func (h *HTTP) Do(ctx context.Context, signed bool, style HeaderStyle, method string, svc ServicePrefix, path string, query url.Values, body any) ([]byte, int, error) {
	full := h.baseFor(svc) + string(svc) + path
	if query != nil && len(query) > 0 {
		full += "?" + query.Encode()
	}

	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal body: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, method, full, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("new request: %w", err)
	}
	if bodyBytes != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if signed {
		signer, err := h.SignerFor(ctx)
		if err != nil {
			return nil, 0, err
		}
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		nonce, err := auth.NewNonce()
		if err != nil {
			return nil, 0, fmt.Errorf("nonce: %w", err)
		}
		// Signing version 2: the upstream path (nginx strips the service
		// prefix before forwarding), the query and a one-time nonce.
		sig := signer.SignV2(method, path, query, ts, nonce, bodyBytes)
		switch style {
		case HeaderStyleBacktester:
			req.Header.Set("X-API-KEY", signer.KeyID())
			req.Header.Set("X-SIGNATURE", sig)
			req.Header.Set("X-TIMESTAMP", ts)
		default:
			req.Header.Set("X-Key-ID", signer.KeyID())
			req.Header.Set("X-Signature", sig)
			req.Header.Set("X-Timestamp", ts)
		}
		req.Header.Set("X-Nonce", nonce)
	}

	resp, err := h.hc.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}
	return respBody, resp.StatusCode, nil
}

// GetJSON convenience wrapper for signed GET with query params, decoding into `out`.
func (h *HTTP) GetJSON(ctx context.Context, signed bool, style HeaderStyle, svc ServicePrefix, path string, query url.Values, out any) error {
	body, status, err := h.Do(ctx, signed, style, http.MethodGet, svc, path, query, nil)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		return fmt.Errorf("GET %s%s: status %d: %s", svc, path, status, string(body))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode %s%s response: %w", svc, path, err)
	}
	return nil
}
