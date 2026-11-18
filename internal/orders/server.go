package orders

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kdraigo/dev_sdk/orderplan"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// Proposal is research_service's order proposal.
type Proposal struct {
	ID             string           `json:"id"`
	Venue          string           `json:"venue"`
	Pair           string           `json:"pair"`
	DrawingID      string           `json:"drawing_id"`
	DrawingVersion int64            `json:"drawing_version"`
	Side           string           `json:"side"`
	EntryType      string           `json:"entry_type"`
	Ticket         orderplan.Ticket `json:"ticket"`
	Hash           string           `json:"hash"`
	Status         string           `json:"status"`
	Mode           string           `json:"mode"`
	Executor       string           `json:"executor"`
	CreatedAt      time.Time        `json:"created_at"`
	ExpiresAt      time.Time        `json:"expires_at"`
	DecidedAt      *time.Time       `json:"decided_at,omitempty"`
	DecidedVia     *string          `json:"decided_via,omitempty"`
	Result         json.RawMessage  `json:"result,omitempty"`
	FinishedAt     *time.Time       `json:"finished_at,omitempty"`
}

// Proposal states (research_service db.Proposal*).
const (
	StatusPending   = "pending"
	StatusConfirmed = "confirmed"
	StatusPlaced    = "placed"
	StatusFailed    = "failed"
)

// ProposalInput is POST /orders/proposals.
type ProposalInput struct {
	Venue          string           `json:"venue"`
	Pair           string           `json:"pair"`
	DrawingID      string           `json:"drawing_id"`
	DrawingVersion int64            `json:"drawing_version"`
	Ticket         orderplan.Ticket `json:"ticket"`
	Executor       string           `json:"executor"`
	AutoAllowed    bool             `json:"auto_allowed"`
}

// Drawing is a stored chart drawing with its version.
type Drawing struct {
	ID      string          `json:"id"`
	Author  string          `json:"author"`
	Body    json.RawMessage `json:"body"`
	Version int64           `json:"version"`
}

// Server is what the executor asks kdraigo: an interface, so tests use a fake.
type Server interface {
	ChartMarket(ctx context.Context) (venue, pair string, err error)
	Drawings(ctx context.Context, venue, pair string) ([]Drawing, error)
	Propose(ctx context.Context, in ProposalInput) (*Proposal, error)
	Proposal(ctx context.Context, id string) (*Proposal, error)
	Proposals(ctx context.Context, limit int) ([]Proposal, error)
	Withdraw(ctx context.Context, id string) (*Proposal, error)
	Report(ctx context.Context, id, executor, status string, result any) (*Proposal, error)
	Events(ctx context.Context, executor string) (io.ReadCloser, error)
}

// HTTPServer is Server over the MCP's signed client (research_service).
type HTTPServer struct{ HTTP *client.HTTP }

const api = "/api/v1/dev"

// APIError is a refusal from kdraigo, with its status and words.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

func (s HTTPServer) do(ctx context.Context, method, path string, q url.Values, body, out any) error {
	raw, status, err := s.HTTP.Do(ctx, true, client.HeaderStyleStandard, method, client.Research, api+path, q, body)
	if err != nil {
		return err
	}
	if status < 200 || status >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return &APIError{Status: status, Message: msg}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("%s %s: decode: %w", method, path, err)
	}
	return nil
}

// ChartMarket is the market of the user's shared chart now.
func (s HTTPServer) ChartMarket(ctx context.Context) (string, string, error) {
	var r struct {
		Reason string `json:"reason"`
		View   *struct {
			Venue string `json:"venue"`
			Pair  string `json:"pair"`
		} `json:"view"`
	}
	if err := s.do(ctx, http.MethodGet, "/chart/view", nil, nil, &r); err != nil {
		return "", "", err
	}
	if r.View == nil {
		reason := r.Reason
		if reason == "" {
			reason = "there is no shared chart"
		}
		return "", "", fmt.Errorf("no chart to read: %s", reason)
	}
	return r.View.Venue, r.View.Pair, nil
}

func (s HTTPServer) Drawings(ctx context.Context, venue, pair string) ([]Drawing, error) {
	var r struct {
		Drawings []Drawing `json:"drawings"`
	}
	err := s.do(ctx, http.MethodGet, "/charts/"+url.PathEscape(venue)+"/"+url.PathEscape(pair)+"/drawings", nil, nil, &r)
	return r.Drawings, err
}

func (s HTTPServer) Propose(ctx context.Context, in ProposalInput) (*Proposal, error) {
	var p Proposal
	return &p, s.do(ctx, http.MethodPost, "/orders/proposals", nil, in, &p)
}

func (s HTTPServer) Proposal(ctx context.Context, id string) (*Proposal, error) {
	var p Proposal
	return &p, s.do(ctx, http.MethodGet, "/orders/proposals/"+url.PathEscape(id), nil, nil, &p)
}

func (s HTTPServer) Proposals(ctx context.Context, limit int) ([]Proposal, error) {
	var r struct {
		Proposals []Proposal `json:"proposals"`
	}
	err := s.do(ctx, http.MethodGet, "/orders/proposals", url.Values{"limit": {strconv.Itoa(limit)}}, nil, &r)
	return r.Proposals, err
}

func (s HTTPServer) Withdraw(ctx context.Context, id string) (*Proposal, error) {
	var p Proposal
	return &p, s.do(ctx, http.MethodPost, "/orders/proposals/"+url.PathEscape(id)+"/reject", nil, struct{}{}, &p)
}

func (s HTTPServer) Report(ctx context.Context, id, executor, status string, result any) (*Proposal, error) {
	var p Proposal
	body := map[string]any{"executor": executor, "status": status, "result": result}
	return &p, s.do(ctx, http.MethodPost, "/orders/proposals/"+url.PathEscape(id)+"/result", nil, body, &p)
}

func (s HTTPServer) Events(ctx context.Context, executor string) (io.ReadCloser, error) {
	resp, err := s.HTTP.Stream(ctx, client.Research, api+"/orders/executor/events", url.Values{"executor": {executor}})
	if err != nil {
		var se *client.StatusError
		if ok := asStatus(err, &se); ok {
			var e struct {
				Error string `json:"error"`
			}
			msg := se.Body
			if json.Unmarshal([]byte(se.Body), &e) == nil && e.Error != "" {
				msg = e.Error
			}
			return nil, &APIError{Status: se.Status, Message: msg}
		}
		return nil, err
	}
	return resp.Body, nil
}
