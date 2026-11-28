package tools

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/mark3labs/mcp-go/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func indicatorTypesFixture(draws, allowed bool) map[string]any {
	return map[string]any{
		"groups": []any{map[string]any{"id": "ma", "name": "Moving averages"}, map[string]any{"id": "mom", "name": "Momentum"}},
		"indicators": []any{
			map[string]any{"fn": "EMA", "name": "Exponential moving average", "short": "EMA", "group": "ma", "overlay": true, "inputs": []any{
				map[string]any{"key": "source", "label": "Source", "kind": "source", "default": "close", "options": []any{"close", "open", "high", "low", "volume"}},
				map[string]any{"key": "length", "label": "Length", "kind": "int", "default": 20, "min": 2, "max": 2000},
			}},
			map[string]any{"fn": "RSI", "name": "Relative strength index", "short": "RSI", "group": "mom", "overlay": false, "inputs": []any{
				map[string]any{"key": "length", "label": "Length", "kind": "int", "default": 14, "min": 2, "max": 2000},
			}},
		},
		"scripts": []any{map[string]any{"ref": "wyckoff-ranges", "name": "Wyckoff Ranges", "version": 13,
			"inputs": []any{map[string]any{"name": "maxBars", "kind": "int", "default": 300, "min": 20, "max": 2000}}}},
		"market":        []any{map[string]any{"key": "delta", "name": "Delta and CVD"}, map[string]any{"key": "oi", "name": "Open interest", "perp": true}},
		"limits":        map[string]any{"indicators": 24, "scripts": 4, "changes": 20},
		"ai_draws":      draws,
		"ai_indicators": allowed,
		"sharing":       true,
	}
}

// What the AI may put on the chart: every indicator with its settings, the
// user's scripts with their inputs, the market rows; and whether it may.
func TestChartIndicatorTypes(t *testing.T) {
	types := indicatorTypesFixture(true, false)
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/chart/indicator-types"): func(w http.ResponseWriter, r *http.Request) { jsonReply(types)(w, r) },
	})
	out, isErr := call(t, s, "chart_indicator_types", nil)
	require.False(t, isErr, out)
	assert.Contains(t, out, "Let my AI set indicators")
	assert.Contains(t, out, "EMA\tExponential moving average\tMoving averages\tover the candles\tsource close (close, open, high, low, volume); length 20 (2–2000)\n")
	assert.Contains(t, out, "RSI\tRelative strength index\tMomentum\tpane\tlength 14 (2–2000)\n")
	assert.Contains(t, out, "wyckoff-ranges\tWyckoff Ranges\t13\tmaxBars int 300 (20–2000)\n")
	assert.Contains(t, out, "oi\tOpen interest\tfutures only\n")
	assert.Contains(t, out, "at most 24 standard indicators and 4 scripts")

	types["ai_indicators"] = true
	out, _ = call(t, s, "chart_indicator_types", map[string]any{"group": "mom"})
	assert.Contains(t, out, "The user lets you set indicators")
	assert.Contains(t, out, "RSI\t")
	assert.NotContains(t, out, "EMA\t")
	assert.NotContains(t, out, "wyckoff-ranges", "one group: the indicators only")

	out, isErr = call(t, s, "chart_indicator_types", map[string]any{"group": "weather"})
	assert.True(t, isErr)
	assert.Contains(t, out, "mom (Momentum)")

	types["ai_draws"] = false
	out, _ = call(t, s, "chart_indicator_types", nil)
	assert.Contains(t, out, "part of Plus")
}

// The AI's changes go to the service as given; the answer says what became of
// each and shows the chart after them, with each indicator's newest value.
func TestChartIndicators(t *testing.T) {
	var sent map[string]any
	reply := map[string]any{
		"confirmed": true,
		"results":   []any{map[string]any{"ok": true, "id": "i1"}, map[string]any{"ok": true, "id": "i2"}, map[string]any{"ok": false, "id": "i9", "error": "nothing on the chart is i9 now: read the chart again"}},
		"view": map[string]any{
			"venue": "binance_futures", "pair": "BTCUSDT", "timeframe": "4h",
			"bars": []any{map[string]any{"t": t0, "o": 1, "h": 1, "l": 1, "c": 1, "v": 1}, map[string]any{"t": t0 + 3600, "o": 1, "h": 1, "l": 1, "c": 1, "v": 1}},
			"series": []any{
				map[string]any{"key": "ind:i1/rsi", "name": "RSI 30 close", "source": "indicator", "kind": "line", "values": []any{41.2, 42.02}},
				map[string]any{"key": "ind:i2/macd", "name": "MACD · MACD", "source": "indicator", "kind": "line", "values": []any{nil, 120.5}},
				map[string]any{"key": "ind:i2/signal", "name": "MACD · signal", "source": "indicator", "kind": "line", "values": []any{nil, 98}},
			},
			"indicators": []any{
				map[string]any{"id": "i1", "fn": "RSI", "name": "RSI 30 close", "inputs": map[string]any{"source": "close", "length": 30}},
				map[string]any{"id": "i2", "fn": "MACD", "name": "MACD 8 26 9 close", "inputs": map[string]any{"fast": 8, "slow": 26, "signal": 9, "source": "close"}, "by": "ai"},
				map[string]any{"id": "i3", "fn": "EMA", "name": "EMA 20 close", "hidden": true},
			},
			"scripts":     []any{map[string]any{"id": "s0", "ref": "wyckoff-ranges", "name": "Wyckoff Ranges", "inputs": map[string]any{"maxBars": 500}, "by": "ai"}},
			"market_rows": []any{map[string]any{"id": "md:oi", "name": "Open interest", "by": "ai"}},
		},
	}
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/chart/indicators"): func(w http.ResponseWriter, r *http.Request) {
			sent = nil
			require.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
			jsonReply(reply)(w, r)
		},
	})
	changes := []any{
		map[string]any{"op": "set", "id": "i1", "inputs": map[string]any{"length": 30}},
		map[string]any{"op": "add", "fn": "MACD", "inputs": map[string]any{"fast": 8}},
		map[string]any{"op": "hide", "id": "i9"},
	}
	out, isErr := call(t, s, "chart_indicators", map[string]any{"changes": changes})
	require.False(t, isErr, out)
	var want any
	b, _ := json.Marshal(changes)
	require.NoError(t, json.Unmarshal(b, &want))
	assert.Equal(t, want, sent["changes"], "sent as given: the service checks them")
	assert.Contains(t, out, "2 of 3 changes were made")
	assert.Contains(t, out, "set i1 {\"length\":30}\ti1\tdone\n")
	assert.Contains(t, out, "add MACD {\"fast\":8}\ti2\tdone\n")
	assert.Contains(t, out, "hide i9\ti9\tnot done: nothing on the chart is i9 now: read the chart again\n")
	assert.Contains(t, out, "i1\tRSI 30 close\tlength 30, source close\tuser\t42.02\n")
	assert.Contains(t, out, "i2\tMACD 8 26 9 close\tfast 8, signal 9, slow 26, source close\tyou\t120.5 / 98\n")
	assert.Contains(t, out, "i3\tEMA 20 close\tdefaults\tuser\thidden\n")
	assert.Contains(t, out, "s0\tWyckoff Ranges (script wyckoff-ranges)\tmaxBars 500\tyou\ton the chart\n")
	assert.Contains(t, out, "md:oi\tOpen interest\t—\tyou\ton the chart\n")

	reply["results"] = []any{map[string]any{"ok": true, "id": "i1"}}
	out, _ = call(t, s, "chart_indicators", map[string]any{"changes": changes[:1]})
	assert.Contains(t, out, "Done on the user's chart.")

	reply["confirmed"] = false
	out, _ = call(t, s, "chart_indicators", map[string]any{"changes": changes[:1]})
	assert.Contains(t, out, "did not confirm in time")

	out, isErr = call(t, s, "chart_indicators", map[string]any{"changes": []any{}})
	assert.True(t, isErr)
	assert.Contains(t, out, "1–20")
}

// chart_view names each indicator by its id and settings, says who added it,
// and whether the AI may set them.
func TestChartView_Indicators(t *testing.T) {
	read := chartFixture()
	read["ai_indicators"] = true
	view := read["view"].(map[string]any)
	view["indicators"] = []any{
		map[string]any{"id": "i1", "fn": "EMA", "name": "EMA 20 close", "inputs": map[string]any{"source": "close", "length": 20}},
		map[string]any{"id": "i4", "fn": "RSI", "name": "RSI 30 close", "inputs": map[string]any{"length": 30}, "hidden": true, "by": "ai"},
	}
	view["market_rows"] = []any{map[string]any{"id": "md:funding", "name": "Funding rate", "by": "ai"}}
	view["scripts"] = []any{map[string]any{"id": "s0", "ref": "wyckoff-ranges", "name": "Wyckoff Ranges", "version": 13, "by": "ai"}}
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/chart/view"): func(w http.ResponseWriter, r *http.Request) { jsonReply(read)(w, r) },
	})
	out, isErr := call(t, s, "chart_view", nil)
	require.False(t, isErr, out)
	assert.Contains(t, out, "The user lets you set indicators: chart_indicators")
	assert.Contains(t, out, "i1\tEMA 20 close\tlength 20, source close\tuser\ton the chart\n")
	assert.Contains(t, out, "i4\tRSI 30 close\tlength 30\tyou\thidden\n")
	assert.Contains(t, out, "md:funding\tFunding rate\t—\tyou\ton the chart\n")
	assert.Contains(t, out, "s0\twyckoff-ranges\tWyckoff Ranges\t13\tthe script's defaults\ton the chart\tyou\n")

	read["ai_indicators"] = false
	out, _ = call(t, s, "chart_view", nil)
	assert.Contains(t, out, "Setting indicators is off")
}

func TestChartIndicators_PlusAndPro(t *testing.T) {
	s := server.NewMCPServer("test", "0")
	RegisterChart(s, Deps{})
	tool := s.GetTool("chart_indicators")
	require.NotNil(t, tool)
	assert.Contains(t, tool.Tool.Description, "Part of Plus and Pro")
	assert.Contains(t, tool.Tool.Description, "Let my AI set indicators")
	require.NotNil(t, s.GetTool("chart_indicator_types"))

	out, err := Guide("chart")
	require.NoError(t, err)
	assert.Contains(t, out, "chart_indicators")
	assert.Contains(t, out, "Let my AI set indicators")
}
