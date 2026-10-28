package tools

import (
	"fmt"
	"strings"

	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// renderDaily adds the account's value at each day's close, open positions
// included, and buy and hold over the window: both apart from the
// closed-trade figures, which they do not change.
func renderDaily(doc *render.Doc, p *performanceReport) {
	m, b := p.MarkToMarket, p.Benchmark
	if m == nil && b == nil {
		return
	}
	doc.Blank()
	doc.Line("## mark to market and buy & hold (daily closes; apart from the figures above)")
	if m != nil {
		doc.Line("Value at each day's close, open positions included: %s → %s %s · return %s · max DD %s (%s) over %d days",
			render.Num(p.InitialCapital, 2), render.Num(m.FinalEquity, 2), p.Quote, signedPct(m.ReturnPct),
			render.Num(m.MaxDrawdown, 2), render.OptPct(m.MaxDrawdownPct, 1), len(m.Curve))
	}
	if b == nil || len(b.Instruments) == 0 {
		return
	}
	parts := make([]string, 0, len(b.Instruments))
	for _, h := range b.Instruments {
		parts = append(parts, fmt.Sprintf("%s %s (max DD %s)", h.Pair, render.Signed(h.ReturnPct, 1)+"%", render.Pct(h.MaxDrawdownPct, 1)))
	}
	line := "Buy & hold, first open → last close: " + strings.Join(parts, " · ")
	if len(b.Instruments) > 1 {
		line += " · equal weight " + signedPctOpt(b.BasketReturnPct)
	}
	if b.ExcessPct != nil {
		line += " · the strategy " + signedPctOpt(b.ExcessPct) + " against it"
	}
	doc.Line("%s", line)
}
