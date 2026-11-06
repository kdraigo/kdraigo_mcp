package tools

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetFlowForwardsItsWindow(t *testing.T) {
	var got string
	s := backend(t, map[string]http.HandlerFunc{
		"/data/api/v1/flow": func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.RawQuery
			jsonReply([]map[string]any{{"open_time": 0, "buy_volume": 1, "delta": 1}})(w, r)
		},
	})
	text, isErr := call(t, s, "get_flow", map[string]any{"exchange": "binance_futures", "symbol": "BTC/USDT", "timeframe": "1h", "from": "0", "to": "3600000"})
	assert.False(t, isErr, text)
	assert.Contains(t, text, `"delta":1`)
	assert.Equal(t, "exchange=binance_futures&from=0&symbol=BTC%2FUSDT&timeframe=1h&to=3600000", got)

	text, isErr = call(t, s, "get_flow", map[string]any{"exchange": "binance"})
	assert.True(t, isErr, "every argument is required: %s", text)
}
