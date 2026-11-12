package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fptr(v float64) *float64 { return &v }
func bptr(v bool) *bool       { return &v }
func sptr(v string) *string   { return &v }

func plainPerp() *sessionExecution {
	return &sessionExecution{FillPolicy: sptr("pessimistic"), GapFills: bptr(true), MakerFee: fptr(0.001), TakerFee: fptr(0.001),
		FuturesMakerFee: fptr(0.0002), FuturesTakerFee: fptr(0.0005), SlippageBps: fptr(0), FeeMult: fptr(1), FundingEnabled: bptr(true)}
}

func TestCostsLine(t *testing.T) {
	assert.Empty(t, costsLine(nil, []string{"binance_futures"}), "a session that recorded none")
	assert.Equal(t, "Costs: perp fees taker 0.05% / maker 0.02% · no slippage · funding charged · a stop gapped through fills at the bar's open · fill policy pessimistic",
		costsLine(plainPerp(), []string{"binance_futures"}))

	stressed := plainPerp()
	stressed.SlippageBps, stressed.FeeMult, stressed.FuturesTakerFee, stressed.FundingEnabled = fptr(10), fptr(1.5), fptr(0.00075), bptr(false)
	c := costsLine(stressed, []string{"binance_futures"})
	assert.Contains(t, c, "perp fees taker 0.075%")
	assert.Contains(t, c, "fees ×1.5 the schedule")
	assert.Contains(t, c, "slippage 10 bp on market and stop fills")
	assert.Contains(t, c, "funding off")

	spot := costsLine(plainPerp(), []string{"binance"})
	assert.Contains(t, spot, "spot fees taker 0.1% / maker 0.1%")
	assert.NotContains(t, spot, "funding", "spot pays none")
}

func TestStressWarning(t *testing.T) {
	assert.Empty(t, stressWarning(nil))
	w := stressWarning([]string{"slippage 10 bp"})
	assert.Contains(t, w, "Cost stress (slippage 10 bp)")
	assert.Contains(t, w, "not the strategy's own result")
}

// The report opens with what the run assumed, and a stressed run says so first.
func TestReportNamesItsCostsAndStress(t *testing.T) {
	routes := reportRoutes()
	ex := plainPerp()
	ex.SlippageBps = fptr(10)
	routes["/frontend-api/api/v1/dev/sessions/"+sid] = jsonReply(sessionDetail{ID: sid, Status: "DONE", Timeframe: "1h",
		Exchanges: []string{"binance_futures"}, Execution: ex, Stress: []string{"slippage 10 bp"}})
	s := backend(t, routes)
	text, isErr := call(t, s, "get_session_report", map[string]any{"session_id": sid})
	require.False(t, isErr, text)
	assert.Contains(t, text, "Costs: perp fees taker 0.05% / maker 0.02% · slippage 10 bp on market and stop fills")
	assert.Contains(t, text, "## warnings\n- [high] Cost stress (slippage 10 bp)")

	// The same from an analytics_service that writes the warnings itself
	// (report version 2 and later), as production does.
	p := reportFixture()
	p.SessionID, p.ReportVersion = sid, 4
	p.Warnings = []reportWarning{{Code: "few_trades", Severity: "medium", Message: "Only 1 closed trades."}}
	routes["/analytics/api/v1/dev/analytics/performance"] = jsonReply(p)
	s = backend(t, routes)
	text, isErr = call(t, s, "get_session_report", map[string]any{"session_id": sid})
	require.False(t, isErr, text)
	assert.Contains(t, text, "## warnings\n- [high] Cost stress (slippage 10 bp)")
	assert.Contains(t, text, "- [medium] Only 1 closed trades.", "the server's warnings follow")

	text, isErr = call(t, s, "get_session_detail", map[string]any{"session_id": sid})
	require.False(t, isErr, text)
	assert.Contains(t, text, "Costs: perp fees")
	assert.Contains(t, text, "Cost stress (slippage 10 bp)")
}

// A session list row carries its stress among the tags.
func TestSessionListShowsStress(t *testing.T) {
	out := renderSessions([]sessionSummary{{ID: "a", Name: "n", Status: "DONE", Stress: []string{"fees ×1.5"}}}, 10, 0)
	assert.Contains(t, out, "stress: fees ×1.5")
}
