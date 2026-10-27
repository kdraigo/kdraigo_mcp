package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// markWindows are tried in order for the last close before the end: minutes
// first, then hours for a market whose minute data ends earlier.
var markWindows = []struct {
	tf   string
	back time.Duration
}{{"1m", 3 * time.Hour}, {"1h", 3 * 24 * time.Hour}}

// priceOpen fills in the price at the end of positions the engine recorded
// none for (sessions from before it did): the last close before end, from
// market data, marked as looked up. A position it cannot price stays
// unpriced, and the error says which.
func (d Deps) priceOpen(ctx context.Context, o *openBlock, end time.Time) error {
	var missing []string
	for i := range o.Positions {
		p := &o.Positions[i]
		if p.MarkPrice != nil || p.Qty == 0 {
			continue
		}
		price, at, err := d.lastClose(ctx, p.Exchange, p.Pair, end)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s %s (%v)", p.Exchange, p.Pair, err))
			continue
		}
		u := p.Qty * (price - p.AvgEntry)
		if strings.EqualFold(p.Side, "short") {
			u = -u
		}
		p.MarkPrice, p.MarkAt, p.Unrealized, p.lookedUp = &price, &at, &u, true
	}
	total := 0.0
	o.Unrealized = &total
	for _, p := range o.Positions {
		if p.Unrealized == nil {
			o.Unrealized = nil
			break
		}
		total += *p.Unrealized
	}
	if len(missing) > 0 {
		return fmt.Errorf("no price at the end for %s", strings.Join(missing, "; "))
	}
	return nil
}

// lastClose is the close of the last bar that closed by end.
func (d Deps) lastClose(ctx context.Context, exchange, pair string, end time.Time) (float64, time.Time, error) {
	for _, w := range markWindows {
		q := url.Values{
			"exchange": {exchange}, "symbol": {pair}, "timeframe": {w.tf},
			"from": {strconv.FormatInt(end.Add(-w.back).UnixMilli(), 10)},
			"to":   {strconv.FormatInt(end.UnixMilli(), 10)},
		}
		body, status, err := d.HTTP.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Data, "/api/v1/candles", q, nil)
		if err != nil {
			return 0, time.Time{}, err
		}
		if status < 200 || status >= 300 {
			return 0, time.Time{}, httpErr("candles", status, body)
		}
		var bars []struct {
			Close     float64 `json:"close"`
			CloseTime int64   `json:"close_time"`
		}
		if err := json.Unmarshal(body, &bars); err != nil {
			return 0, time.Time{}, err
		}
		for i := len(bars) - 1; i >= 0; i-- {
			at := time.UnixMilli(bars[i].CloseTime)
			if !at.After(end) && bars[i].Close > 0 {
				return bars[i].Close, at, nil
			}
		}
	}
	return 0, time.Time{}, fmt.Errorf("no candles before %s", render.Time(end))
}

// markedEnd is where a backtest's positions are valued: its last processed
// candle, else the end of its window.
func markedEnd(det *sessionDetail) (time.Time, bool) {
	if det == nil {
		return time.Time{}, false
	}
	if pr := det.Progress; pr != nil && pr.LastCandleTime != nil {
		return *pr.LastCandleTime, true
	}
	if det.EndAt != nil {
		return *det.EndAt, true
	}
	return time.Time{}, false
}

// renderOpen reports what was still open at the end, apart from the figures.
func renderOpen(doc *render.Doc, in *reportInputs) {
	p := &in.perf
	if p.ReportVersion < 3 {
		if p.Total.OpenAtEnd > 0 {
			doc.Line("Open at the end: %d position(s), which this analytics_service (report version %d) counts in the figures above.", p.Total.OpenAtEnd, p.ReportVersion)
		}
		return
	}
	o := p.Total.Open
	if o == nil || len(o.Positions) == 0 {
		return
	}
	doc.Blank()
	doc.Line("## open at the end: %d position(s), left out of every figure above", len(o.Positions))
	unreal := "unknown (no price at the end)"
	if o.Unrealized != nil {
		unreal = render.Signed(*o.Unrealized, 2) + " at the last close"
	}
	line := fmt.Sprintf("Unrealized %s · paid on them so far: fees %s, funding %s", unreal, render.Num(o.Fees, 2), render.Num(o.Funding, 2))
	if o.Realized != 0 {
		line += " · realized by partial closes " + render.Signed(o.Realized, 2)
	}
	doc.Line("%s", line)
	if in.openErr != nil {
		doc.Line("(%v)", in.openErr)
	}
	rows := make([][]string, 0, len(o.Positions))
	looked := false
	for _, op := range o.Positions {
		source := "engine"
		if op.lookedUp {
			source, looked = "looked up", true
		} else if op.MarkPrice == nil {
			source = render.Dash
		}
		rows = append(rows, []string{op.Exchange, op.Pair, op.Side, render.Num(op.Qty, 6), render.Px(op.AvgEntry),
			render.OptPx(op.MarkPrice), render.OptTime(op.MarkAt), signedOpt(op.Unrealized, 2),
			render.Signed(op.Realized, 2), render.Num(op.Fees, 2), render.Num(op.Funding, 2), render.OptTime(op.OpenedAt), source})
	}
	doc.Table("", []string{"exchange", "pair", "side", "qty", "avg_entry", "last_close", "close_at", "unrealized", "realized", "fees", "funding", "opened", "price"}, rows, nil)
	if looked {
		doc.Line("looked up: the engine recorded no price at the end (a session from before it did); this is the last close in the market data.")
	}
}
