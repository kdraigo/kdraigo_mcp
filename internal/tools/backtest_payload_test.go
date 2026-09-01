package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engineInitialWalletRequest is a copy of backtester_engine's
// dev.InitialWalletRequest, tags included. It is duplicated rather than
// imported because the engine is a separate repository.
//
// Decoding our own payload into it is the only check that actually proves the
// two agree. The engine's DTO uses snake_case tags and Go's case-insensitive
// matching does not cross underscores, so a field named "MarketType" on the
// wire decodes to empty here — no error, no warning, and a perpetual session
// that silently runs as spot. That failure has no symptom until the fees,
// leverage and liquidation of a whole run turn out to be wrong.
type engineInitialWalletRequest struct {
	Exchange   string  `json:"exchange"`
	Asset      string  `json:"asset"`
	Balance    float64 `json:"balance"`
	MarketType string  `json:"market_type,omitempty"`
	Leverage   float64 `json:"leverage,omitempty"`
}

func TestInitialWalletDecodesIntoTheEngineDTO(t *testing.T) {
	sent := initialWallet{
		Exchange:   "binance_futures",
		Asset:      "USDT",
		Balance:    10_000,
		MarketType: "linear_perp",
		Leverage:   5,
	}

	raw, err := json.Marshal(sent)
	require.NoError(t, err)

	var got engineInitialWalletRequest
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.Equal(t, "binance_futures", got.Exchange)
	assert.Equal(t, "USDT", got.Asset)
	assert.Equal(t, 10_000.0, got.Balance)
	assert.Equal(t, "linear_perp", got.MarketType,
		"if this is empty the wallet is spot and the whole run is wrong")
	assert.Equal(t, 5.0, got.Leverage)
}

// TestSpotWalletPayloadIsUnchanged: adding futures fields must not alter what a
// spot session sends. omitempty is what guarantees that, and it is worth
// asserting rather than assuming.
func TestSpotWalletPayloadIsUnchanged(t *testing.T) {
	raw, err := json.Marshal(initialWallet{Exchange: "binance", Asset: "USDT", Balance: 5000})
	require.NoError(t, err)

	var fields map[string]any
	require.NoError(t, json.Unmarshal(raw, &fields))

	assert.NotContains(t, fields, "market_type")
	assert.NotContains(t, fields, "leverage")
	assert.Len(t, fields, 3)
}

// TestSimulationBlockIsOmittedForSpot: an absent simulation block means "engine
// defaults", which is what a spot session has always sent.
func TestSimulationBlockIsOmittedForSpot(t *testing.T) {
	raw, err := json.Marshal(createSessionBody{
		Streams:        []streamConfig{{Exchange: "binance", Pair: "BTC/USDT"}},
		InitialWallets: []initialWallet{{Exchange: "binance", Asset: "USDT", Balance: 1}},
	})
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))
	assert.NotContains(t, body, "simulation")
}

// TestFundingEnabledFalseSurvivesEncoding: a pointer is what keeps "explicitly
// off" distinguishable from "not specified". Encoding a plain bool would drop
// false under omitempty and silently leave funding on — reversing the caller's
// only reason for setting it.
func TestFundingEnabledFalseSurvivesEncoding(t *testing.T) {
	off := false
	raw, err := json.Marshal(simulationOptions{MarginMode: "isolated", FundingEnabled: &off})
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Contains(t, got, "funding_enabled")
	assert.Equal(t, false, got["funding_enabled"])
}
