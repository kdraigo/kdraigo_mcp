package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
)

// A refusal names the plan that stopped it; the offer goes to the plans
// above it, and to none above Pro.
func TestRefusalTier(t *testing.T) {
	for text, want := range map[string]string{
		"limit reached: 4 pairs, your basic account backtests at most 2 at once; split the pairs (Plus, kdraigo.com/pricing, runs 5 pairs and 150,000 bars per pair)": "basic",
		"limit reached: your basic (Google sign-in) account keeps at most 3 workspaces; delete one first":                                                               "basic",
		"limit reached: less than 1m0s of your plus account's workspace time is left today; it renews at 00:00 UTC":                                                     "plus",
		"forbidden: alerts are part of Plus and Pro (kdraigo.com/pricing); they reach you in Telegram once it is linked on kdraigo.com/account":                         "basic",
		"limit reached: your pro account runs 5 at once":                                                                                                               "",
		"workspace: HTTP 404: not found":                                                                                                                                "",
	} {
		got, ok := refusalTier(text)
		assert.Equal(t, want, got, text)
		assert.Equal(t, want != "", ok, text)
	}
}

func TestOfferLine(t *testing.T) {
	backend(t, nil) // serves the price list
	ctx := context.Background()
	assert.Contains(t, offerLine(ctx, "limit reached: your basic account keeps 1 research lab(s); delete one or upgrade"),
		"A limit of the user's plan: Plus ($8 a month) or Pro ($19 a month) raises it (kdraigo.com/pricing")
	assert.Contains(t, offerLine(ctx, "limit reached: your plus account keeps 5 research lab(s)"), ": Pro ($19 a month) raises it")
	assert.Empty(t, offerLine(ctx, "limit reached: your pro account keeps 25 research lab(s)"))
	assert.Empty(t, offerLine(ctx, "session not found"))

	// Only a refused call gets it, and only once.
	res := withOffer(ctx, toolErr(errors.New("limit reached: your basic account keeps 3 board(s)")))
	text := res.Content[0].(mcp.TextContent).Text
	assert.Contains(t, text, "\nA limit of the user's plan: Plus ($8 a month)")
	ok := withOffer(ctx, toolText("your basic account keeps 3 board(s)"))
	assert.NotContains(t, ok.Content[0].(mcp.TextContent).Text, "A limit of the user's plan")
}

// Without the price list the offer still names the plans and the page.
func TestOfferWithoutPrices(t *testing.T) {
	resetPrices()
	saved := priceHTTP.Swap(nil)
	t.Cleanup(func() {
		if saved != nil {
			priceHTTP.Store(saved)
		}
		resetPrices()
	})
	assert.Contains(t, offerLine(context.Background(), "your basic account keeps 3 board(s)"), ": Plus or Pro raises it (kdraigo.com/pricing")
}

func TestPriced(t *testing.T) {
	assert.Equal(t, "Plus ($8 a month)", priced("plus", map[string]int{"plus": 800}))
	assert.Equal(t, "Pro ($19.50 a month)", priced("pro", map[string]int{"pro": 1950}))
	assert.Equal(t, "Pro", priced("pro", nil))
}
