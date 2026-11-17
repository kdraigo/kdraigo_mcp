package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// What a paid plan adds, and the line that tells the AI to offer it. The
// user's wish (2026-11-17): the AI that uses this server suggests a paid plan
// when the user's plan stops what they asked for. get_started lists what each
// higher plan adds; a refusal a higher plan lifts carries an offer line.
//
// Offered once, and only when something was refused: a nudge on every answer
// would bury the work the user came for.

const pricingURL = "kdraigo.com/pricing"

// planAdds is what each paid plan gives, from lib/tiers (v0.0.25). Orders
// from the AI are left out until they can be placed.
var planAdds = map[string]string{
	"plus": "5 pairs and 150,000 bars per backtest, 3 backtests at once, 512 MB of results, the cloud workspace 4 h a day (10 workspaces, 5 research labs), " +
		"5 chart-script alerts on 15m bars or longer for 30 days, and the AI drawing, pointing and replaying on up to 3 Chart pages",
	"pro": "15 pairs and 1,500,000 bars per backtest, 5 at once, 3 GB of results, a cloud workspace machine of its own 8 h a day (25 workspaces, 25 research labs), " +
		"50 alerts on 5m bars or longer, and the AI on up to 8 Chart pages",
}

// plansAbove lists the paid plans above a tier, cheapest first.
func plansAbove(tier string) []string {
	switch strings.ToLower(strings.TrimSpace(tier)) {
	case "basic", "":
		return []string{"plus", "pro"}
	case "plus", "verified": // verified: a server from before Plus
		return []string{"pro"}
	}
	return nil
}

func planName(plan string) string {
	switch plan {
	case "plus":
		return "Plus"
	case "pro":
		return "Pro"
	}
	return plan
}

// The prices come from billing_service's public price list, kept a while;
// without it the offer names the plans and the pricing page only.
const (
	pricesTTL   = 15 * time.Minute
	pricesRetry = time.Minute
	pricesWait  = 3 * time.Second
)

type billingPlans struct {
	Plans []struct {
		Plan   string `json:"plan"`
		Prices []struct {
			Months   int `json:"months"`
			USDCents int `json:"usd_cents"`
		} `json:"prices"`
	} `json:"plans"`
}

var priceList struct {
	mu      sync.Mutex
	monthly map[string]int // plan → cents for one month
	next    time.Time      // when to read the list again
}

// priceHTTP is the client the price list is read with: set when the tools
// are registered, for the tool wrapper, which has no Deps of its own.
var priceHTTP atomic.Pointer[client.HTTP]

func setPriceSource(h *client.HTTP) {
	if h != nil {
		priceHTTP.Store(h)
	}
}

// monthlyPrices is each paid plan's one-month price in cents; empty when the
// list was never read.
func monthlyPrices(ctx context.Context) map[string]int {
	priceList.mu.Lock()
	defer priceList.mu.Unlock()
	now := time.Now()
	if now.Before(priceList.next) {
		return priceList.monthly
	}
	priceList.next = now.Add(pricesRetry)
	h := priceHTTP.Load()
	if h == nil {
		return priceList.monthly
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pricesWait)
	defer cancel()
	body, status, err := h.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Billing, "/api/v1/plans", nil, nil)
	if err != nil || status != http.StatusOK {
		return priceList.monthly
	}
	var list billingPlans
	if json.Unmarshal(body, &list) != nil {
		return priceList.monthly
	}
	m := map[string]int{}
	for _, p := range list.Plans {
		for _, pr := range p.Prices {
			if pr.Months == 1 && pr.USDCents > 0 {
				m[p.Plan] = pr.USDCents
			}
		}
	}
	if len(m) > 0 {
		priceList.monthly = m
		priceList.next = now.Add(pricesTTL)
	}
	return priceList.monthly
}

// priced is "Plus ($8 a month)", or "Plus" without a price.
func priced(plan string, prices map[string]int) string {
	c, ok := prices[plan]
	if !ok {
		return planName(plan)
	}
	if c%100 == 0 {
		return fmt.Sprintf("%s ($%d a month)", planName(plan), c/100)
	}
	return fmt.Sprintf("%s ($%d.%02d a month)", planName(plan), c/100, c%100)
}

// planList is "Plus ($8 a month) or Pro ($19 a month)" with sep " or ".
func planList(plans []string, prices map[string]int, sep string) string {
	out := make([]string, len(plans))
	for i, p := range plans {
		out[i] = priced(p, prices)
	}
	return strings.Join(out, sep)
}

// upgradeLines is what get_started shows a Basic or Plus account: what each
// higher plan adds, and when to offer it.
func upgradeLines(ctx context.Context, tier string) []string {
	plans := plansAbove(tier)
	if len(plans) == 0 {
		return nil
	}
	prices := monthlyPrices(ctx)
	lines := make([]string, 0, len(plans)+1)
	for _, p := range plans {
		lines = append(lines, fmt.Sprintf("- %s: %s.", priced(p, prices), planAdds[p]))
	}
	lines = append(lines, fmt.Sprintf("When the user asks for something their plan does not allow, tell them once which of these allows it, at its price (%s), then do what their plan allows.", pricingURL))
	return lines
}

// refusedPlan reads which plan a refusal names: "your basic account…",
// "a plus account's alerts…",
// "your basic (Google sign-in) account…", "your plus account…", or a feature
// "part of Plus and Pro (kdraigo.com/pricing)". "" when it names none.
var refusedPlan = regexp.MustCompile(`(?:your|a) (basic|plus|verified)(?: \(Google sign-in\))? account`)

func refusalTier(text string) (string, bool) {
	if m := refusedPlan.FindStringSubmatch(text); m != nil {
		return m[1], true
	}
	if strings.Contains(text, pricingURL) {
		return "basic", true
	}
	return "", false
}

// offerLine is the line a refusal gets when a higher plan lifts it; "" when
// none does (Pro and admin limits, other errors).
func offerLine(ctx context.Context, text string) string {
	tier, ok := refusalTier(text)
	if !ok {
		return ""
	}
	plans := plansAbove(tier)
	if len(plans) == 0 {
		return ""
	}
	return fmt.Sprintf("A limit of the user's plan: %s raises it (%s; get_started lists what each adds). Tell the user once in this conversation, then carry on with what their plan allows.",
		planList(plans, monthlyPrices(ctx), " or "), pricingURL)
}

// withOffer adds the offer line to a refused call's result.
func withOffer(ctx context.Context, res *mcp.CallToolResult) *mcp.CallToolResult {
	if res == nil || !res.IsError {
		return res
	}
	for i, c := range res.Content {
		t, ok := c.(mcp.TextContent)
		if !ok {
			continue
		}
		if line := offerLine(ctx, t.Text); line != "" {
			t.Text += "\n" + line
			res.Content[i] = t
		}
		break
	}
	return res
}

// chartReadOnlyNote is what chart_view says on a plan whose AI only reads the
// chart: no drawing to promise, levels in words, and the plan that draws.
func chartReadOnlyNote(ctx context.Context) string {
	return fmt.Sprintf("On the user's plan you can read a shared chart, but drawing on it, moving it, replaying it and saving snapshots are part of %s (%s): do not offer to draw. "+
		"Give levels in words with their prices and times, and tell the user once that with Plus you could draw them on their chart.",
		planList([]string{"plus", "pro"}, monthlyPrices(ctx), " and "), pricingURL)
}
