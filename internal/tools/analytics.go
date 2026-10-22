package tools

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// RegisterAnalytics adds get_session_report, get_trades, get_breakdown,
// get_analytics_types and get_analytics_series, all via analytics_service's
// /dev path.
func RegisterAnalytics(s *server.MCPServer, d Deps) {
	addGetSessionReport(s, d)
	addGetTrades(s, d)
	addGetBreakdown(s, d)
	addGetAnalyticsTypes(s, d)
	addGetAnalyticsSeries(s, d)
}

func addGetAnalyticsTypes(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_analytics_types",
		mcp.WithDescription("List available analytics metric types (e.g. equity, drawdown, trading_frequency, fees_cumulative)."),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var types []struct {
			Type string `json:"type"`
			Desc string `json:"desc"`
		}
		if err := d.getJSON(ctx, "get_analytics_types", client.Analytics, "/api/v1/dev/analytics/types", nil, &types); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		rows := make([][]string, 0, len(types))
		for _, t := range types {
			rows = append(rows, []string{t.Type, t.Desc})
		}
		doc.Table("", []string{"type", "description"}, rows, nil)
		return toolText(doc.String()), nil
	})
}

// The bucket sizes analytics_service serves, finest first.
var seriesTimeframes = []struct {
	name string
	step time.Duration
}{
	{"1m", time.Minute}, {"5m", 5 * time.Minute}, {"15m", 15 * time.Minute},
	{"1h", time.Hour}, {"4h", 4 * time.Hour}, {"1d", 24 * time.Hour},
}

// maxSeriesBuckets keeps one series inside the output cap: a row is about
// 25 bytes.
const maxSeriesBuckets = 2000

func addGetAnalyticsSeries(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_analytics_series",
		mcp.WithDescription("Time-bucketed analytics series for a session, one `time value` row per bucket (UTC). `type` must be one of the values from get_analytics_types. "+
			"equity is closed-trade equity at each bucket's end; drawdown is the percent below the running peak (the deepest point in the bucket). "+
			fmt.Sprintf("At most %d buckets per call: a range longer than that is clamped to the session's window, and if the session is still too long for the timeframe it comes back at the finest one that fits, with a note. ", maxSeriesBuckets)+
			"For P&L per month use get_session_report instead."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("session_type", mcp.Required(), mcp.Description("Session type: 'backtest' or 'live'"), mcp.Enum("backtest", "live")),
		mcp.WithString("type", mcp.Required(), mcp.Description("Metric type, e.g. equity")),
		mcp.WithString("timeframe", mcp.Description("Bucket size"), mcp.DefaultString("1h"), mcp.Enum("1m", "5m", "15m", "1h", "4h", "1d")),
		mcp.WithNumber("from", mcp.Required(), mcp.Description("Start timestamp in Unix milliseconds")),
		mcp.WithNumber("to", mcp.Required(), mcp.Description("End timestamp in Unix milliseconds")),
		mcp.WithString("exchange", mcp.Description("Narrow equity or drawdown to one instrument, with pair")),
		mcp.WithString("pair", mcp.Description("Narrow equity or drawdown to one instrument, with exchange, e.g. BTC/USDT")),
		readOnly(),
	)
	addTimeout(s, tool, heavyTimeout, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		sessionType, err := req.RequireString("session_type")
		if err != nil {
			return toolErr(err), nil
		}
		metric, err := req.RequireString("type")
		if err != nil {
			return toolErr(err), nil
		}
		from, err := req.RequireFloat("from")
		if err != nil {
			return toolErr(err), nil
		}
		to, err := req.RequireFloat("to")
		if err != nil {
			return toolErr(err), nil
		}
		tf := req.GetString("timeframe", "1h")
		if seriesStep(tf) == 0 {
			return toolErr(fmt.Errorf("unsupported timeframe %q: use 1m, 5m, 15m, 1h, 4h or 1d", tf)), nil
		}
		if to <= from {
			return toolErr(fmt.Errorf("to must be after from")), nil
		}
		exchange, pair := req.GetString("exchange", ""), req.GetString("pair", "")
		if (exchange == "") != (pair == "") {
			return toolErr(fmt.Errorf("exchange and pair go together")), nil
		}

		// ClickHouse metrics are gap-filled and uncapped, so a fine timeframe
		// over a wide range costs a response the size of the range. A range
		// too long for the timeframe is first clamped to the session's own
		// window, then coarsened if it still does not fit.
		lo, hi := int64(from), int64(to)
		note := ""
		if seriesBuckets(lo, hi, tf) > maxSeriesBuckets {
			wlo, whi, err := d.sessionWindow(ctx, sessionID, sessionType)
			if err != nil {
				return toolErr(fmt.Errorf("from..to at %s is %d buckets (max %d), and the session's window could not be read to narrow it (%v): pass from/to inside the session's window",
					tf, seriesBuckets(lo, hi, tf), maxSeriesBuckets, err)), nil
			}
			lo, hi = max(lo, wlo), min(hi, whi)
			if hi <= lo {
				return toolErr(fmt.Errorf("from..to does not overlap the session's window, %s to %s", render.Time(time.UnixMilli(wlo)), render.Time(time.UnixMilli(whi)))), nil
			}
			if n := seriesBuckets(lo, hi, tf); n > maxSeriesBuckets {
				coarser := fittingTimeframe(time.Duration(hi-lo) * time.Millisecond)
				if coarser == "" {
					return toolErr(fmt.Errorf("%d buckets at %s, too many even at 1d (max %d): narrow from/to", n, tf, maxSeriesBuckets)), nil
				}
				note = fmt.Sprintf("%s would be %d buckets over the session (max %d), so this is %s.", tf, n, maxSeriesBuckets, coarser)
				tf = coarser
			}
		}

		q := url.Values{}
		q.Set("session_id", sessionID)
		q.Set("session_type", sessionType)
		q.Set("type", metric)
		q.Set("timeframe", tf)
		q.Set("from", strconv.FormatInt(lo, 10))
		q.Set("to", strconv.FormatInt(hi, 10))
		if exchange != "" {
			q.Set("exchange", exchange)
			q.Set("pair", pair)
		}
		var series seriesResponse
		if err := d.getJSON(ctx, "get_analytics_series", client.Analytics, "/api/v1/dev/analytics/series", q, &series); err != nil {
			return toolErr(err), nil
		}
		scope := "whole session"
		if exchange != "" {
			scope = exchange + " " + pair
		}
		return toolText(renderSeries(&series, metric, tf, scope, note)), nil
	})
}

// seriesBuckets is how many buckets of tf cover [from, to), in Unix ms.
func seriesBuckets(from, to int64, tf string) int64 {
	step := seriesStep(tf).Milliseconds()
	return (to - from + step - 1) / step
}

// sessionWindow is the span a session's series can cover, in Unix ms. A
// backtest's record has it exactly. A live session's comes from its daily
// equity, which the server clamps to the session, so it is exact to the day.
func (d Deps) sessionWindow(ctx context.Context, id, kind string) (from, to int64, err error) {
	if kind == "backtest" {
		var det sessionDetail
		if err := d.getJSON(ctx, "session window", client.FrontendAPI, "/api/v1/dev/sessions/"+url.PathEscape(id), nil, &det); err != nil {
			return 0, 0, err
		}
		if det.StartAt == nil || det.EndAt == nil {
			return 0, 0, fmt.Errorf("the session has no start or end")
		}
		return det.StartAt.UnixMilli(), det.EndAt.UnixMilli(), nil
	}
	q := url.Values{
		"session_id": {id}, "session_type": {kind}, "type": {"equity"}, "timeframe": {"1d"},
		"from": {strconv.FormatInt(seriesEpoch, 10)},
		"to":   {strconv.FormatInt(time.Now().Add(24*time.Hour).UnixMilli(), 10)},
	}
	var s seriesResponse
	if err := d.getJSON(ctx, "session window", client.Analytics, "/api/v1/dev/analytics/series", q, &s); err != nil {
		return 0, 0, err
	}
	if len(s.Points) == 0 {
		return 0, 0, fmt.Errorf("the session has no days yet")
	}
	day := (24 * time.Hour).Milliseconds()
	return s.Points[0].OpenTime, s.Points[len(s.Points)-1].OpenTime + day, nil
}

func seriesStep(tf string) time.Duration {
	for _, t := range seriesTimeframes {
		if t.name == tf {
			return t.step
		}
	}
	return 0
}

// fittingTimeframe is the finest timeframe that covers span in at most
// maxSeriesBuckets buckets, or "" when even 1d does not.
func fittingTimeframe(span time.Duration) string {
	for _, t := range seriesTimeframes {
		if int64(span/t.step) <= maxSeriesBuckets {
			return t.name
		}
	}
	return ""
}

func renderSeries(s *seriesResponse, metric, tf, scope, note string) string {
	doc := render.New()
	if note != "" {
		doc.Line("%s", note)
	}
	if len(s.Points) == 0 {
		doc.Line("%s · %s · %s: no points in range.", metric, tf, scope)
		return doc.String()
	}
	lo, hi := s.Points[0], s.Points[0]
	for _, p := range s.Points {
		if p.Value < lo.Value {
			lo = p
		}
		if p.Value > hi.Value {
			hi = p
		}
	}
	first, last := s.Points[0], s.Points[len(s.Points)-1]
	doc.Line("%s · %s · %s · %d buckets, times UTC bucket opens", metric, tf, scope, len(s.Points))
	doc.Line("first %s · last %s · min %s at %s · max %s at %s",
		render.Px(first.Value), render.Px(last.Value),
		render.Px(lo.Value), render.Time(time.UnixMilli(lo.OpenTime)),
		render.Px(hi.Value), render.Time(time.UnixMilli(hi.OpenTime)))
	rows := make([][]string, 0, len(s.Points))
	for _, p := range s.Points {
		rows = append(rows, []string{render.Time(time.UnixMilli(p.OpenTime)), render.Px(p.Value)})
	}
	doc.Table("", []string{"time", "value"}, rows, func(shown int) string {
		return fmt.Sprintf("narrow from to %d", s.Points[shown].OpenTime)
	})
	return doc.String()
}
