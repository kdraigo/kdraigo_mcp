package tools

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// sweepRow is one variant's line of the comparison.
type sweepRow struct {
	session string
	perf    *performanceReport
	err     error
}

// renderSweep compares a sweep's variants: each one's settings and status,
// and the closed-trade figures of the session its key opened (the first, for
// a strategy that opens several), plus the mark-to-market return where the
// engine recorded the days.
func (d Deps) renderSweep(ctx context.Context, doc *render.Doc, r *workspaceRun) {
	rows := make([]sweepRow, len(r.Variants))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, v := range r.Variants {
		if v.KeyID == "" || v.Status == "running" {
			continue
		}
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			q := url.Values{"api_key_id": {v.KeyID}, "limit": {"5"}}
			var list []sessionSummary
			if err := d.getJSON(ctx, "sessions of a variant", client.FrontendAPI, "/api/v1/dev/sessions", q, &list); err != nil {
				rows[i].err = err
				return
			}
			if len(list) == 0 {
				return
			}
			sort.Slice(list, func(a, b int) bool { return list[a].CreatedAt.Before(list[b].CreatedAt) })
			rows[i].session = list[0].ID
			var p performanceReport
			pq := url.Values{"session_id": {list[0].ID}, "session_type": {"backtest"}}
			if err := d.getJSON(ctx, "a variant's performance", client.Analytics, "/api/v1/dev/analytics/performance", pq, &p); err != nil {
				rows[i].err = err
				return
			}
			rows[i].perf = &p
		})
	}
	wg.Wait()

	table := make([][]string, 0, len(r.Variants))
	best, bestReturn := -1, 0.0
	for i, v := range r.Variants {
		row := []string{fmt.Sprint(i + 1), variantSettings(v), v.Status}
		if rows[i].session == "" {
			row = append(row, render.Dash, render.Dash, render.Dash, render.Dash, render.Dash, render.Dash, render.Dash, render.Dash)
			table = append(table, row)
			continue
		}
		p := rows[i].perf
		if p == nil {
			row = append(row, rows[i].session, render.Dash, render.Dash, render.Dash, render.Dash, render.Dash, render.Dash, render.Dash)
			table = append(table, row)
			continue
		}
		t := &p.Total
		mtm := render.Dash
		if p.MarkToMarket != nil {
			mtm = signedPctOpt(p.MarkToMarket.ReturnPct)
		}
		row = append(row, rows[i].session, render.Signed(t.NetPnL, 2), signedPctOpt(t.ReturnPct), render.OptPct(t.MaxDrawdownPct, 1),
			fmt.Sprint(t.Trades), rate(t.Wins, t.Trades), render.Opt(t.ProfitFactor, 2), mtm)
		if t.ReturnPct != nil && (best < 0 || *t.ReturnPct > bestReturn) {
			best, bestReturn = i, *t.ReturnPct
		}
		table = append(table, row)
	}
	doc.Table(fmt.Sprintf("Sweep: %d variants, closed-trade figures of each one's session", len(r.Variants)),
		[]string{"#", "settings", "status", "session_id", "net", "return", "max_dd", "trades", "win", "pf", "mtm_return"}, table, nil)
	if best >= 0 {
		doc.Line("Best return: variant %d (%s). One window's best is a fit to it: check it on another window before trusting it.",
			best+1, signedPctOpt(&bestReturn))
	}
	for i, row := range rows {
		if row.err != nil {
			doc.Line("Variant %d: %v", i+1, row.err)
		}
	}
}

// variantSettings is a variant's own settings: declared values, and the
// names of the rest.
func variantSettings(v runVariant) string {
	names := make([]string, 0, len(v.Params))
	for n := range v.Params {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names)+len(v.EnvNames))
	for _, n := range names {
		parts = append(parts, n+"="+v.Params[n])
	}
	for _, n := range v.EnvNames {
		parts = append(parts, n+"=(set)")
	}
	return strings.Join(parts, " ")
}
