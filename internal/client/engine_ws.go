package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"

	"github.com/kdraigo/kdraigo_mcp/internal/auth"
)

// WSAction matches backtester/internal/controller/dev/engine.go ReadJSON envelope.
type WSAction struct {
	Action    string `json:"action"`
	RequestID string `json:"request_id,omitempty"`
	Data      any    `json:"data,omitempty"`
}

// WSResponse matches backtester/internal/controller/dev/engine.go WriteJSON envelope.
type WSResponse struct {
	Action    string          `json:"action"`
	RequestID string          `json:"request_id,omitempty"`
	Status    string          `json:"status"`
	Data      json.RawMessage `json:"data,omitempty"`
	Error     string          `json:"error,omitempty"`
}

// SessionWS is a thin connection to /api/v1/dev/session/ws on backtester_engine.
type SessionWS struct {
	conn *websocket.Conn
}

// DialSessionWS opens an authenticated WS to backtester_engine.
// `endpoint` should be the backtester base (api.kdraigo.com); the scheme is rewritten to ws(s).
func DialSessionWS(ctx context.Context, endpoint string, signer *auth.Signer, sessionID string) (*SessionWS, error) {
	// Canonical signed path: /api/v1/dev/session/ws (upstream path, post-prefix-strip).
	const wsPath = "/api/v1/dev/session/ws"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := signer.Sign("GET", wsPath, ts, "")

	base := strings.TrimRight(endpoint, "/")
	switch {
	case strings.HasPrefix(base, "https://"):
		base = "wss://" + strings.TrimPrefix(base, "https://")
	case strings.HasPrefix(base, "http://"):
		base = "ws://" + strings.TrimPrefix(base, "http://")
	}

	q := url.Values{}
	q.Set("id", sessionID)
	q.Set("key_id", signer.KeyID())
	q.Set("signature", sig)
	q.Set("timestamp", ts)

	full := base + string(Backtester) + wsPath + "?" + q.Encode()

	conn, _, err := websocket.Dial(ctx, full, nil)
	if err != nil {
		return nil, fmt.Errorf("ws dial %s: %w", full, err)
	}
	conn.SetReadLimit(16 << 20) // 16 MiB; tick payloads with orders can be large
	return &SessionWS{conn: conn}, nil
}

func (s *SessionWS) Send(ctx context.Context, action WSAction) error {
	return wsjson.Write(ctx, s.conn, action)
}

func (s *SessionWS) Recv(ctx context.Context) (*WSResponse, error) {
	var resp WSResponse
	if err := wsjson.Read(ctx, s.conn, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (s *SessionWS) Close() error {
	return s.conn.Close(websocket.StatusNormalClosure, "")
}
