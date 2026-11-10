package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// marketInfo is what data_provider's catalogue says a market is.
type marketInfo struct {
	Name     string `json:"name"`
	Class    string `json:"class"`
	Leverage int    `json:"leverage"`
}

func addDataCoverage(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_data_coverage",
		mcp.WithDescription("What market history the platform holds: per exchange and symbol, the first and last 1m bar, "+
			"and what the market is: its class (crypto, stock, etf, commodity) and name. "+
			"Tickers do not say it: SKUUBUSDT on binance is \"GraniteShares 2x Long SK Hynix Daily ETF\", a tokenized fund on spot (Binance's bStocks); "+
			"XAUUSDT on binance_futures is gold. A fund's daily leverage is in its name and marked (2x, -3x): leveraged funds reset every day, "+
			"so a long backtest on one is not a backtest on what it tracks. Stock and fund markets trade 24/7 while their exchanges are shut, "+
			"so weekend bars are thin. "+
			"Check it before choosing a backtest's window: a session that starts before its market's first bar is refused (no_data), "+
			"and binance spot starts later than binance_futures. Symbols are listed as stored, without the slash (BTCUSDT is BTC/USDT)."),
		mcp.WithString("exchange", mcp.Description("Only this exchange, e.g. binance or binance_futures")),
		mcp.WithString("symbol", mcp.Description("Only this symbol, with or without the slash, e.g. BTC/USDT")),
		mcp.WithString("class", mcp.Description("Only this class: crypto, stock, etf, commodity, or tradfi (everything but crypto)"),
			mcp.Enum("crypto", "stock", "etf", "commodity", "tradfi")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		exchange := req.GetString("exchange", "")
		symbol := strings.ToUpper(strings.ReplaceAll(req.GetString("symbol", ""), "/", ""))
		class := strings.ToLower(req.GetString("class", ""))
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

		// What each market is, per exchange. A catalogue that cannot be read
		// leaves its markets' class and name blank; the history still answers.
		catalogue := map[string]map[string]marketInfo{}
		var unnamed []string
		for ex := range meta {
			if exchange != "" && ex != exchange {
				continue
			}
			q := url.Values{}
			q.Set("exchange", ex)
			b, st, err := d.HTTP.Do(ctx, false, client.HeaderStyleStandard, http.MethodGet, client.Data, "/api/v1/instruments", q, nil)
			var markets map[string]marketInfo
			if err != nil || st < 200 || st >= 300 || json.Unmarshal(b, &markets) != nil {
				unnamed = append(unnamed, ex)
				continue
			}
			catalogue[ex] = markets
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
				info := catalogue[ex][sym]
				if !classMatches(class, info.Class) {
					continue
				}
				name := info.Name
				if info.Leverage != 0 {
					name = fmt.Sprintf("%s [%+dx]", name, info.Leverage)
				}
				first, last := time.UnixMilli(span.Start).UTC(), time.UnixMilli(span.End).UTC()
				rows = append(rows, []string{ex, sym, render.Date(first), render.Time(last), render.Num(last.Sub(first).Hours()/24, 0), info.Class, name})
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i][0] != rows[j][0] {
				return rows[i][0] < rows[j][0]
			}
			return rows[i][1] < rows[j][1]
		})
		if len(rows) == 0 {
			return toolText(fmt.Sprintf("No history for exchange %q, symbol %q, class %q.", exchange, symbol, class)), nil
		}
		doc := render.New()
		if len(unnamed) > 0 {
			sort.Strings(unnamed)
			doc.Line("Class and name unavailable right now for: %s.", strings.Join(unnamed, ", "))
		}
		doc.Table(fmt.Sprintf("market history (%d), UTC", len(rows)), []string{"exchange", "symbol", "first_bar", "last_bar", "days", "class", "name"}, rows, nil)
		return toolText(doc.String()), nil
	})
}

// classMatches applies the class filter: empty takes all, "tradfi" every
// market that is not crypto.
func classMatches(want, class string) bool {
	switch want {
	case "":
		return true
	case "tradfi":
		return class != "" && class != "crypto"
	default:
		return class == want
	}
}
