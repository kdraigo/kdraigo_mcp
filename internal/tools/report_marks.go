package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// positionMark is a position still open at a backtest's end, valued at the
// last close before the end as well as at its last fill. The report's own
// figures use the last fill (analytics has no price feed), which for a trend
// strategy holding for months can flip the conclusion.
type positionMark struct {
	Exchange, Pair, Side string
	Qty, AvgEntry        float64
	LastFill, Close      float64
	CloseAt              time.Time
	AtFill, AtClose      float64 // unrealized P&L
}

// markWindows are tried in order for the last close before the end: minutes
// first, then hours for a market whose minute data ends earlier.
var markWindows = []struct {
	tf   string
	back time.Duration
}{{"1m", 3 * time.Hour}, {"1h", 3 * 24 * time.Hour}}

// markOpenPositions prices each open position at the last close before end.
// A position it cannot price is left out, and the error says which.
func (d Deps) markOpenPositions(ctx context.Context, p *performanceReport, end time.Time) ([]positionMark, error) {
	var marks []positionMark
	var missing []string
	for _, s := range p.Instruments {
		op := s.OpenPosition
		if op == nil || op.Qty == 0 {
			continue
		}
		price, at, err := d.lastClose(ctx, s.Exchange, s.Pair, end)
		if err != nil {
			missing = append(missing, fmt.Sprintf("%s %s (%v)", s.Exchange, s.Pair, err))
			continue
		}
		atClose := op.Qty * (price - op.AvgEntry)
		if strings.EqualFold(op.Side, "short") {
			atClose = -atClose
		}
		marks = append(marks, positionMark{Exchange: s.Exchange, Pair: s.Pair, Side: op.Side, Qty: op.Qty, AvgEntry: op.AvgEntry,
			LastFill: op.LastFillPrice, Close: price, CloseAt: at, AtFill: op.UnrealizedAtLastFill, AtClose: atClose})
	}
	if len(missing) > 0 {
		return marks, fmt.Errorf("no price at the end for %s", strings.Join(missing, "; "))
	}
	return marks, nil
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

// markShift is how much the open positions are worth at the last close over
// their last fill.
func markShift(marks []positionMark) float64 {
	shift := 0.0
	for _, m := range marks {
		shift += m.AtClose - m.AtFill
	}
	return shift
}

// renderMarks adds the marked-to-market headline and the open positions.
func renderMarks(doc *render.Doc, in *reportInputs) {
	if len(in.marks) == 0 {
		if in.markErr != nil {
			doc.Line("Marked to the last close: not available (%v)", in.markErr)
		}
		return
	}
	p := &in.perf
	shift := markShift(in.marks)
	equity := p.FinalEquity + shift
	ret := render.Dash
	if p.InitialCapital > 0 {
		ret = render.Signed((equity-p.InitialCapital)/p.InitialCapital*100, 2) + "%"
	}
	doc.Line("Marked to the last close (open positions at the market, not their last fill): final equity %s · return %s · %s against the headline",
		render.Num(equity, 2), ret, render.Signed(shift, 2))
	if in.markErr != nil {
		doc.Line("(partly: %v)", in.markErr)
	}
	rows := make([][]string, 0, len(in.marks))
	for _, m := range in.marks {
		rows = append(rows, []string{m.Exchange, m.Pair, m.Side, render.Num(m.Qty, 6), render.Num(m.AvgEntry, 4),
			render.Num(m.LastFill, 4), render.Num(m.Close, 4), render.Time(m.CloseAt), render.Signed(m.AtFill, 2), render.Signed(m.AtClose, 2)})
	}
	doc.Table("open at the end", []string{"exchange", "pair", "side", "qty", "avg_entry", "last_fill", "last_close", "close_at", "unreal_at_fill", "unreal_at_close"}, rows, nil)
}

// markWarning says when the headline misstates the result by more than 1% of
// the capital because of how open positions are valued.
func markWarning(in *reportInputs) string {
	if len(in.marks) == 0 || in.perf.InitialCapital <= 0 {
		return ""
	}
	shift := markShift(in.marks)
	if math.Abs(shift) < 0.01*in.perf.InitialCapital {
		return ""
	}
	way, does := "more", "understate"
	if shift < 0 {
		way, does = "less", "overstate"
	}
	return fmt.Sprintf("Open positions are worth %s %s at the last close than at their last fill: the headline final equity and return %s the result, "+
		"and drawdown, Sharpe and the months count closed trades only. Read the marked line.",
		render.Num(math.Abs(shift), 2), way, does)
}
