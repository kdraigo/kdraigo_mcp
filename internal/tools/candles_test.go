package tools

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Times that could only come back empty are refused with what was wrong.
func TestMillisRange(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	f, to, err := millisRange("1778803200000", " 1778889600000 ", now)
	require.NoError(t, err)
	assert.Equal(t, int64(1778803200000), f)
	assert.Equal(t, int64(1778889600000), to)

	for _, tc := range []struct{ from, to, want string }{
		{"1778803200", "1778889600000", "from 1778803200 looks like seconds (2026-05-15 00:00 UTC as seconds, 1970-01-21 14:06 as milliseconds): give milliseconds, 1778803200000"},
		{"1778803200000", "1778889600", "to 1778889600 looks like seconds"},
		{"1778889600000", "1778803200000", "to (2026-05-15 00:00 UTC) is before from (2026-05-16 00:00 UTC): the range is reversed"},
		{"1800000000000", "1800086400000", "from (2027-01-15 08:00 UTC) is in the future"},
		{"2026-05-15", "1778889600000", `from "2026-05-15": give Unix epoch milliseconds`},
		{"-5", "1778889600000", "from -5 is not a time the data holds"},
	} {
		_, _, err := millisRange(tc.from, tc.to, now)
		require.Error(t, err, tc.from)
		assert.Contains(t, err.Error(), tc.want)
	}
}

// An empty answer says why: a market the exchange lacks is an error, a range
// outside the history or in a gap of it is an answer with nothing in it.
func TestGetCandles_EmptySaysWhy(t *testing.T) {
	meta := map[string]any{"binance_futures": map[string]any{"symbols": map[string]any{
		"BTCUSDT": map[string]any{"start": 1567296000000, "end": 1778889600000},
		"BTCUSDC": map[string]any{"start": 1700000000000, "end": 1778889600000},
	}}}
	s := backend(t, map[string]http.HandlerFunc{
		"/data/api/v1/candles": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("null"))
		},
		"/data/api/v1/metadata": jsonReply(meta),
	})
	args := func(symbol, from, to string) map[string]any {
		return map[string]any{"exchange": "binance_futures", "symbol": symbol, "timeframe": "1h", "from": from, "to": to}
	}

	out, isErr := call(t, s, "get_candles", args("BTC/USDX", "1700000000000", "1700086400000"))
	assert.True(t, isErr, out)
	assert.Contains(t, out, "BTC/USDX is not a market on binance_futures")
	assert.Contains(t, out, "it has BTCUSDC, BTCUSDT")

	out, isErr = call(t, s, "get_candles", args("btc/usdt", "1700000000000", "1700086400000"))
	assert.True(t, isErr, out)
	assert.Contains(t, out, "symbols are upper case: BTC/USDT")

	out, isErr = call(t, s, "get_candles", args("BTC/USDT", "1500000000000", "1500086400000"))
	assert.False(t, isErr, out)
	assert.Contains(t, out, "No candles: no candles for BTC/USDT on binance_futures from 2017-07-14 02:40 to 2017-07-15 02:40 UTC. Its history starts 2019-09-01 00:00 UTC")

	out, isErr = call(t, s, "get_candles", args("BTC/USDT", "1700000000000", "1700086400000"))
	assert.False(t, isErr, out)
	assert.Contains(t, out, "inside its history")

	out, isErr = call(t, s, "get_candles", args("BTC/USDT", "1700000000", "1700086400000"))
	assert.True(t, isErr, out)
	assert.Contains(t, out, "looks like seconds")
}
