package tools

import (
	"math"
	"strconv"
	"strings"
)

// costsLine is a backtest's fill model and costs in one line, for the report
// and the session record: a result is read with the costs that made it.
// Empty when the session recorded none.
func costsLine(e *sessionExecution, venues []string) string {
	if e == nil {
		return ""
	}
	futures, spot := false, len(venues) == 0
	for _, v := range venues {
		if strings.HasSuffix(v, "_futures") {
			futures = true
		} else {
			spot = true
		}
	}
	var parts []string
	fees := func(label string, maker, taker *float64) {
		if maker == nil && taker == nil {
			return
		}
		var f []string
		if taker != nil {
			f = append(f, "taker "+feePct(*taker))
		}
		if maker != nil {
			f = append(f, "maker "+feePct(*maker))
		}
		parts = append(parts, label+" "+strings.Join(f, " / "))
	}
	if futures {
		fees("perp fees", e.FuturesMakerFee, e.FuturesTakerFee)
	}
	if spot {
		fees("spot fees", e.MakerFee, e.TakerFee)
	}
	if m := e.FeeMult; m != nil && *m != 1 {
		parts = append(parts, "fees ×"+trimNum(*m)+" the schedule")
	}
	if s := e.SlippageBps; s != nil && *s > 0 {
		parts = append(parts, "slippage "+trimNum(*s)+" bp on market and stop fills")
	} else {
		parts = append(parts, "no slippage")
	}
	if futures && e.FundingEnabled != nil {
		if *e.FundingEnabled {
			parts = append(parts, "funding charged")
		} else {
			parts = append(parts, "funding off")
		}
	}
	if g := e.GapFills; g != nil {
		if *g {
			parts = append(parts, "a stop gapped through fills at the bar's open")
		} else {
			parts = append(parts, "a stop gapped through fills at its price")
		}
	}
	if p := e.FillPolicy; p != nil && *p != "" {
		parts = append(parts, "fill policy "+*p)
	}
	return "Costs: " + strings.Join(parts, " · ")
}

// stressWarning says a session was made under a cost stress, so it is never
// reported as the strategy's own result. Empty when it was not.
func stressWarning(stress []string) string {
	if len(stress) == 0 {
		return ""
	}
	return "Cost stress (" + strings.Join(stress, ", ") + "): this run was charged worse execution on purpose. " +
		"Its figures are not the strategy's own result: compare it with the run without the stress, and report it as a stress."
}

func feePct(f float64) string { return trimNum(f*100) + "%" }

// trimNum is a number with at most 4 decimals and no trailing zeros.
func trimNum(v float64) string {
	return strconv.FormatFloat(math.Round(v*1e4)/1e4, 'f', -1, 64)
}
