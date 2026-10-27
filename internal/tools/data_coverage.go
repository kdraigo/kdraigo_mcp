package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

func addDataCoverage(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_data_coverage",
		mcp.WithDescription("What market history the platform holds: per exchange and symbol, the first and last 1m bar. "+
			"Check it before choosing a backtest's window: a session that starts before its market's first bar is refused (no_data), "+
			"and binance spot starts later than binance_futures. Symbols are listed as stored, without the slash (BTCUSDT is BTC/USDT)."),
		mcp.WithString("exchange", mcp.Description("Only this exchange, e.g. binance or binance_futures")),
		mcp.WithString("symbol", mcp.Description("Only this symbol, with or without the slash, e.g. BTC/USDT")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		exchange := req.GetString("exchange", "")
		symbol := strings.ToUpper(strings.ReplaceAll(req.GetString("symbol", ""), "/", ""))
		body, status, err := d.HTTP.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Data, "/api/v1/metadata", nil, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("get_data_coverage", status, body)), nil
		}
		var meta map[string]struct {
			Symbols map[string]struct {
				Start int64 `json:"start"`
				End   int64 `json:"end"`
			} `json:"symbols"`
		}
		if err := json.Unmarshal(body, &meta); err != nil {
			return toolErr(err), nil
		}
		var rows [][]string
		for ex, m := range meta {
			if exchange != "" && ex != exchange {
				continue
			}
			for sym, span := range m.Symbols {
				if symbol != "" && sym != symbol {
					continue
				}
				first, last := time.UnixMilli(span.Start).UTC(), time.UnixMilli(span.End).UTC()
				rows = append(rows, []string{ex, sym, render.Date(first), render.Time(last), render.Num(last.Sub(first).Hours()/24, 0)})
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i][0] != rows[j][0] {
				return rows[i][0] < rows[j][0]
			}
			return rows[i][1] < rows[j][1]
		})
		if len(rows) == 0 {
			return toolText(fmt.Sprintf("No history for exchange %q, symbol %q.", exchange, symbol)), nil
		}
		doc := render.New()
		doc.Table(fmt.Sprintf("market history (%d), UTC", len(rows)), []string{"exchange", "symbol", "first_bar", "last_bar", "days"}, rows, nil)
		return toolText(doc.String()), nil
	})
}
