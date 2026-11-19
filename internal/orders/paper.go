package orders

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/kdraigo/dev_sdk/orderplan"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// AI orders on paper wallets (Phase 9 of the paper exchange, wiki
// roadmap/livetester_engine.md §6.4): kdraigo's server builds, checks and
// places them on the user's paper wallet itself, after the user's yes. No
// executor, no exchange key: the hosted server offers them too.

// PaperInput is POST /orders/paper/proposals (research_service PaperOrderInput).
type PaperInput struct {
	Kind      string  `json:"kind,omitempty"`
	Venue     string  `json:"venue"`
	Pair      string  `json:"pair"`
	Wallet    string  `json:"wallet,omitempty"`
	DrawingID string  `json:"drawing_id,omitempty"`
	EntryType string  `json:"entry_type,omitempty"`
	Risk      float64 `json:"risk,omitempty"`
	OrderID   string  `json:"order_id,omitempty"`
	Stop      float64 `json:"stop,omitempty"`
	Target    float64 `json:"target,omitempty"`
}

// PaperRefusal is a paper order that did not pass its checks: nothing was
// proposed; Ticket or Action says why.
type PaperRefusal struct {
	Message string
	Ticket  *orderplan.Ticket
	Action  *orderplan.Action
}

func (r *PaperRefusal) Error() string { return r.Message }

// ProposePaper proposes an order on a paper wallet.
func (s HTTPServer) ProposePaper(ctx context.Context, in PaperInput) (*Proposal, error) {
	raw, status, err := s.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodPost, client.Research, api+"/orders/paper/proposals", nil, in)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnprocessableEntity {
		var r struct {
			Error  string            `json:"error"`
			Ticket *orderplan.Ticket `json:"ticket"`
			Action *orderplan.Action `json:"action"`
		}
		if json.Unmarshal(raw, &r) == nil {
			return nil, &PaperRefusal{Message: r.Error, Ticket: r.Ticket, Action: r.Action}
		}
	}
	if status < 200 || status >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(raw))
		if json.Unmarshal(raw, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		return nil, &APIError{Status: status, Message: msg}
	}
	var p Proposal
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// PaperWallet is one of the user's paper wallets (or a market's own paper account).
type PaperWallet struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // wallet | manual
	Name   string `json:"name"`
	Venue  string `json:"venue"`
	Status string `json:"status"`
}

// PaperWallets is the user's futures paper wallets and the active one.
type PaperWallets struct {
	Accounts []PaperWallet     `json:"accounts"`
	Active   map[string]string `json:"active"`
}

func (s HTTPServer) PaperWallets(ctx context.Context) (PaperWallets, error) {
	var out PaperWallets
	return out, s.do(ctx, http.MethodGet, "/orders/paper/wallets", nil, nil, &out)
}

// PaperPosition is an open futures position on a paper wallet.
type PaperPosition struct {
	Symbol           string      `json:"symbol"`
	Side             string      `json:"side"` // LONG | SHORT
	Size             json.Number `json:"size"`
	Entry            json.Number `json:"entry_price"`
	Leverage         int         `json:"leverage"`
	Mark             json.Number `json:"mark"`
	UnrealizedPnL    json.Number `json:"unrealized_pnl"`
	LiquidationPrice json.Number `json:"liquidation_price"`
}

// PaperOrder is a working order on a paper wallet.
type PaperOrder struct {
	ID         uint64       `json:"id"`
	Symbol     string       `json:"symbol"`
	Side       string       `json:"side"`
	Type       string       `json:"type"`
	Qty        json.Number  `json:"quantity"`
	Filled     json.Number  `json:"filled_quantity"`
	Price      *json.Number `json:"price"`
	StopPrice  *json.Number `json:"stop_price"`
	ReduceOnly bool         `json:"reduce_only"`
	Status     string       `json:"status"`
	StopLoss   *json.Number `json:"stop_loss"`
	TakeProfit *json.Number `json:"take_profit"`
}

// PaperView is a paper wallet as it stands.
type PaperView struct {
	Account   PaperWallet     `json:"account"`
	Symbols   []string        `json:"symbols"`
	Equity    json.Number     `json:"equity"`
	Positions []PaperPosition `json:"positions"`
	Orders    []PaperOrder    `json:"open_orders"`
	// Ticks is each open position's price tick (research_service adds it).
	Ticks map[string]float64 `json:"ticks"`
}

func (s HTTPServer) PaperPositions(ctx context.Context, wallet string) (PaperView, error) {
	var out PaperView
	return out, s.do(ctx, http.MethodGet, "/orders/paper/positions", url.Values{"wallet": {wallet}}, nil, &out)
}
