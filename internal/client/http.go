package client

import (
	"bytes"
	"context"
	"encoding/json"
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
		hc:                 &http.Client{Timeout: 60 * time.Second},
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

// Signer exposes the configured Ed25519 signer.
func (h *HTTP) Signer() *auth.Signer { return h.signer }

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
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		// Sign the upstream path (nginx strips the service prefix before forwarding).
		sig := h.signer.Sign(method, path, ts, string(bodyBytes))
		switch style {
		case HeaderStyleBacktester:
			req.Header.Set("X-API-KEY", h.signer.KeyID())
			req.Header.Set("X-SIGNATURE", sig)
			req.Header.Set("X-TIMESTAMP", ts)
		default:
			req.Header.Set("X-Key-ID", h.signer.KeyID())
			req.Header.Set("X-Signature", sig)
			req.Header.Set("X-Timestamp", ts)
		}
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
