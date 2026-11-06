package tools

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// get_flow asks data_provider for columns and answers CSV: a header, a row
// per bar, null as an empty cell.
func TestGetFlowAnswersCSV(t *testing.T) {
	var got string
	s := backend(t, map[string]http.HandlerFunc{
		"/data/api/v1/flow": func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.RawQuery
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"open_time":[0,3600000],"delta":[1.5,-2],"liq_long_quote":[null,0],"complete":[true,false]}`))
		},
	})
	text, isErr := call(t, s, "get_flow", map[string]any{"exchange": "binance_futures", "symbol": "BTC/USDT", "timeframe": "1h", "from": "0", "to": "3600000", "fields": "delta, liq_long_quote"})
	assert.False(t, isErr, text)
	assert.Equal(t, "open_time,delta,liq_long_quote,complete\n0,1.5,,true\n3600000,-2,0,false\n", text)
	assert.Equal(t, "exchange=binance_futures&fields=delta%2Cliq_long_quote&format=columns&from=0&symbol=BTC%2FUSDT&timeframe=1h&to=3600000", got)

	text, isErr = call(t, s, "get_flow", map[string]any{"exchange": "binance", "symbol": "BTC/USDT", "timeframe": "1h"})
	assert.True(t, isErr, "a window or times are required: %s", text)
	assert.Contains(t, text, "from and to")
}

// at looks up many times in one call; json gives data_provider's rows as they are.
func TestGetPositioningLooksUpTimes(t *testing.T) {
	var got string
	s := backend(t, map[string]http.HandlerFunc{
		"/data/api/v1/positioning": func(w http.ResponseWriter, r *http.Request) {
			got = r.URL.RawQuery
			jsonReply([]map[string]any{{"at": 300000, "open_time": 0, "open_interest": 95000}})(w, r)
		},
	})
	text, isErr := call(t, s, "get_positioning", map[string]any{"exchange": "binance_futures", "symbol": "BTC/USDT", "timeframe": "5m", "at": "300000, 600000", "format": "json"})
	assert.False(t, isErr, text)
	assert.Contains(t, text, `"open_interest":95000`)
	assert.Equal(t, "at=300000%2C600000&exchange=binance_futures&symbol=BTC%2FUSDT&timeframe=5m", got)
}

// An answer past what a reply holds is refused with what to narrow.
func TestMarketAnswerTooBig(t *testing.T) {
	big := `{"open_time":[` + strings.Repeat("1777777777777,", 9000) + `1]}`
	s := backend(t, map[string]http.HandlerFunc{
		"/data/api/v1/flow": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(big))
		},
	})
	text, isErr := call(t, s, "get_flow", map[string]any{"exchange": "binance", "symbol": "BTC/USDT", "timeframe": "1m", "from": "0", "to": "1"})
	assert.True(t, isErr)
	assert.Contains(t, text, "fewer columns (fields)")
}
