package tools

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAlerts(t *testing.T) {
	var created map[string]any
	alert := map[string]any{"id": "a1b2", "script": "alpha:sqz", "script_name": "SQZ", "condition": "Squeeze fired",
		"exchange": "binance_futures", "symbol": "ETH/USDT", "timeframe": "1h", "status": "active", "expires_at": "2026-11-29T12:00:00Z"}
	deleted := false
	s := backend(t, map[string]http.HandlerFunc{
		labRoute("/alerts"): func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&created))
				w.WriteHeader(http.StatusCreated)
				jsonReply(alert)(w, r)
				return
			}
			paused := map[string]any{"id": "c3d4", "script": "mine", "script_name": "Mine", "condition": "Up", "exchange": "bybit",
				"symbol": "SOL/USDT", "timeframe": "4h", "status": "paused", "reason": "Mine no longer has the alert \"Up\"",
				"fired_bar": 1793365200000, "expires_at": "2026-11-29T12:00:00Z"}
			jsonReply([]any{alert, paused})(w, r)
		},
		labRoute("/alerts/events"): func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "5", r.URL.Query().Get("limit"))
			jsonReply([]any{map[string]any{"kind": "fired", "condition": "Squeeze fired", "symbol": "ETH/USDT", "timeframe": "1h",
				"bar_time": 1793365200000, "message": "ETH/USDT 1h squeeze fired", "delivery": "queued", "created_at": "2026-10-30T13:00:21Z"}})(w, r)
		},
		labRoute("/alerts/a1b2/renew"): jsonReply(alert),
		labRoute("/alerts/a1b2"): func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, http.MethodDelete, r.Method)
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		},
	})

	out, isErr := call(t, s, "alert_create", map[string]any{"script": "alpha:sqz", "condition": "Squeeze fired",
		"exchange": "binance_futures", "symbol": "ETH/USDT", "timeframe": "1h", "inputs": map[string]any{"len": 20}})
	require.False(t, isErr, out)
	assert.Equal(t, map[string]any{"len": float64(20)}, created["inputs"])
	assert.Equal(t, "Squeeze fired", created["condition"])
	assert.Contains(t, out, `Armed "Squeeze fired" on ETH/USDT binance_futures 1h with SQZ (alpha:sqz): alert a1b2, active until`)

	_, isErr = call(t, s, "alert_create", map[string]any{"script": "sqz"})
	assert.True(t, isErr, "condition and the chart are required")

	out, isErr = call(t, s, "alert_list", map[string]any{"events": 5})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Mine no longer has the alert")
	assert.Contains(t, out, "ETH/USDT 1h squeeze fired")
	assert.Contains(t, out, "queued")

	out, isErr = call(t, s, "alert_renew", map[string]any{"alert": "a1b2"})
	require.False(t, isErr, out)
	assert.Contains(t, out, "Renewed")
	out, isErr = call(t, s, "alert_delete", map[string]any{"alert": "a1b2"})
	require.False(t, isErr, out)
	assert.True(t, deleted)
}
