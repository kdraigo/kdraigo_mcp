package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// The AI sets indicators on the user's shared chart (wiki
// roadmap/ai_chart_indicators.md): with the user's "Let my AI set indicators"
// on, it adds standard indicators, the user's scripts and market data rows,
// changes their settings, hides, shows and removes them. research_service
// checks each change and the user's page applies it, then answers with the
// chart after it.

type (
	indicatorInputType struct {
		Key     string   `json:"key"`
		Label   string   `json:"label"`
		Kind    string   `json:"kind"`
		Default any      `json:"default"`
		Min     *float64 `json:"min"`
		Max     *float64 `json:"max"`
		Options []string `json:"options"`
	}
	indicatorType struct {
		Fn      string               `json:"fn"`
		Name    string               `json:"name"`
		Short   string               `json:"short"`
		Group   string               `json:"group"`
		Overlay bool                 `json:"overlay"`
		Inputs  []indicatorInputType `json:"inputs"`
	}
	indicatorTypes struct {
		Groups []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"groups"`
		Indicators []indicatorType `json:"indicators"`
		Scripts    []struct {
			Ref     string          `json:"ref"`
			Name    string          `json:"name"`
			Version int64           `json:"version"`
			Inputs  json.RawMessage `json:"inputs"`
		} `json:"scripts"`
		Market []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
			Perp bool   `json:"perp"`
		} `json:"market"`
		Limits struct {
			Indicators int `json:"indicators"`
			Scripts    int `json:"scripts"`
			Changes    int `json:"changes"`
		} `json:"limits"`
		AIDraws      bool `json:"ai_draws"`
		AIIndicators bool `json:"ai_indicators"`
		Sharing      bool `json:"sharing"`
	}
	chartIndicator struct {
		ID     string          `json:"id"`
		Fn     string          `json:"fn"`
		Name   string          `json:"name"`
		Inputs json.RawMessage `json:"inputs"`
		Hidden bool            `json:"hidden"`
		By     string          `json:"by"`
	}
	chartMarketRow struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Hidden bool   `json:"hidden"`
		By     string `json:"by"`
	}
	changeResult struct {
		OK    bool   `json:"ok"`
		ID    string `json:"id"`
		Error string `json:"error"`
	}
)

// indicatorsSwitchOff is said when the user has not let the AI set indicators.
const indicatorsSwitchOff = "The user has not let you set indicators: ask them to switch on \"Let my AI set indicators\" in Share with AI on kdraigo.com/chart. Until then, say in the chat which indicator and settings to add."

// inputText says what an indicator's input takes: "length 14 (2–2000)", "source close (close, open, …)".
func inputText(in indicatorInputType) string {
	out := fmt.Sprintf("%s %v", in.Key, in.Default)
	switch {
	case len(in.Options) > 0:
		out += " (" + strings.Join(in.Options, ", ") + ")"
	case in.Min != nil && in.Max != nil:
		out += fmt.Sprintf(" (%s–%s)", render.Num(*in.Min, -1), render.Num(*in.Max, -1))
	case in.Min != nil:
		out += fmt.Sprintf(" (from %s)", render.Num(*in.Min, -1))
	}
	return out
}

// scriptInputsText lists a script's inputs from its check's info: "len int 20 (2–500)".
func scriptInputsText(raw json.RawMessage) string {
	var list []struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Default any      `json:"default"`
		Min     *float64 `json:"min"`
		Max     *float64 `json:"max"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &list) != nil || len(list) == 0 {
		return "no inputs"
	}
	parts := make([]string, 0, len(list))
	for _, in := range list {
		p := fmt.Sprintf("%s %s %v", in.Name, in.Kind, in.Default)
		if in.Min != nil && in.Max != nil {
			p += fmt.Sprintf(" (%s–%s)", render.Num(*in.Min, -1), render.Num(*in.Max, -1))
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, "; ")
}

func addChartIndicatorTypes(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("chart_indicator_types",
		mcp.WithDescription("What can go on the user's shared chart with chart_indicators: the standard indicators with their settings (name, default, range), the user's scripts and their syndicates' with their inputs, and the market data rows. "+
			"Says whether the user lets you set indicators. Every plan reads it, so you can say what you would add before they allow it."),
		mcp.WithString("group", mcp.Description("Only one group of standard indicators, e.g. mom (momentum); left out: all, with the scripts and market rows")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var t indicatorTypes
		if err := d.getJSON(ctx, "chart_indicator_types", client.Research, labsAPI+"/chart/indicator-types", nil, &t); err != nil {
			return toolErr(err), nil
		}
		group := strings.ToLower(strings.TrimSpace(req.GetString("group", "")))
		doc := render.New()
		switch {
		case !t.AIDraws:
			doc.Line("%s", chartReadOnlyNote(ctx))
		case !t.AIIndicators:
			doc.Line("%s", indicatorsSwitchOff)
		default:
			doc.Line("The user lets you set indicators: chart_indicators adds them by fn, script or market, and changes what is on the chart by its id (chart_view lists them).")
		}
		if !t.Sharing {
			doc.Line("The chart is not shared now: the user switches on \"Share with my AI\" first.")
		}
		names := map[string]string{}
		for _, g := range t.Groups {
			names[g.ID] = g.Name
		}
		rows := [][]string{}
		known := false
		for _, it := range t.Indicators {
			if group != "" && it.Group != group {
				continue
			}
			known = true
			inputs := make([]string, len(it.Inputs))
			for i, in := range it.Inputs {
				inputs[i] = inputText(in)
			}
			where := "pane"
			if it.Overlay {
				where = "over the candles"
			}
			rows = append(rows, []string{it.Fn, it.Name, orDash(names[it.Group]), where, orDash(strings.Join(inputs, "; "))})
		}
		if group != "" && !known {
			ids := make([]string, 0, len(t.Groups))
			for _, g := range t.Groups {
				ids = append(ids, g.ID+" ("+g.Name+")")
			}
			return toolErr(fmt.Errorf("chart_indicator_types: no group %q; groups: %s", group, strings.Join(ids, ", "))), nil
		}
		doc.Blank()
		doc.Table("Standard indicators (add by fn; inputs by key, the ones left out take the default)", []string{"fn", "name", "group", "drawn", "inputs: key default (range or options)"}, rows, nil)
		if group == "" {
			srows := make([][]string, 0, len(t.Scripts))
			for _, sc := range t.Scripts {
				srows = append(srows, []string{sc.Ref, sc.Name, fmt.Sprint(sc.Version), scriptInputsText(sc.Inputs)})
			}
			doc.Blank()
			if len(srows) == 0 {
				doc.Line("The user has no scripts of their own or their syndicates' (a library script is forked first with script_fork, and only when the user asks for it).")
			} else {
				doc.Table("The user's scripts (add by script = its ref; inputs by name)", []string{"script", "name", "version", "inputs"}, srows, nil)
			}
			mrows := make([][]string, 0, len(t.Market))
			for _, m := range t.Market {
				on := "every market"
				if m.Perp {
					on = "futures only"
				}
				mrows = append(mrows, []string{m.Key, m.Name, on})
			}
			doc.Blank()
			doc.Table("Market data rows (add by market = its key; no settings)", []string{"market", "name", "on"}, mrows, nil)
			doc.Blank()
			doc.Line("A chart holds at most %d standard indicators and %d scripts; one call makes at most %d changes.", t.Limits.Indicators, t.Limits.Scripts, t.Limits.Changes)
		}
		return toolText(doc.String()), nil
	})
}

func addChartIndicators(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("chart_indicators",
		mcp.WithDescription("Set the indicators on the user's shared chart: add a standard indicator, one of the user's scripts or a market data row, change the settings of one that is there (RSI length 14 → 30, a script's inputs), hide, show or remove it. "+
			"The user's own indicators are changed too, so say in the chat what you changed and why. Only when it helps what the user asked, and a few at a time; remove what you added for one question when it is answered. "+
			"What is there is named by the id chart_view lists (i1, s0, md:oi); what can be added is in chart_indicator_types. The answer is the chart after the change, with each indicator's newest value. "+
			"Needs the user's \"Let my AI set indicators\" on. Part of Plus and Pro; works only while the user shares the chart."),
		mcp.WithArray("changes", mcp.Required(), mcp.Description(`1–20 changes, applied together, e.g. [{"op":"add","fn":"RSI","inputs":{"length":30}},{"op":"set","id":"i1","inputs":{"length":50}},{"op":"add","script":"wyckoff-ranges","inputs":{"maxBars":500}},{"op":"add","market":"oi"},{"op":"hide","id":"s0"},{"op":"remove","id":"i2"}]. `+
			`ops: add (with fn, script or market), set (an id and the inputs to change; the rest stay), hide, show, remove (an id). Ids name the chart as it was before the call.`),
			mcp.Items(map[string]any{"type": "object"})),
		// It can change the settings of the user's own indicators and remove them.
		destructive(false),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		raw, err := objectArg(req, "changes")
		if err != nil || raw == nil {
			return toolErr(fmt.Errorf("changes: a list of changes, each {op, …}: add with fn, script or market; set, hide, show or remove with an id")), nil
		}
		var changes []map[string]any
		if err := json.Unmarshal(raw, &changes); err != nil || len(changes) == 0 {
			return toolErr(fmt.Errorf("changes: a list of 1–20 objects, e.g. [{\"op\":\"add\",\"fn\":\"RSI\",\"inputs\":{\"length\":30}}]")), nil
		}
		var out struct {
			Confirmed bool           `json:"confirmed"`
			Results   []changeResult `json:"results"`
			View      *struct {
				Venue      string           `json:"venue"`
				Pair       string           `json:"pair"`
				Timeframe  string           `json:"timeframe"`
				Bars       []chartBar       `json:"bars"`
				Series     []chartSeries    `json:"series"`
				Indicators []chartIndicator `json:"indicators"`
				Scripts    []chartScript    `json:"scripts"`
				MarketRows []chartMarketRow `json:"market_rows"`
			} `json:"view"`
		}
		if err := d.sendJSON(ctx, "chart_indicators", http.MethodPost, client.Research, labsAPI+"/chart/indicators", map[string]any{"changes": changes}, &out); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		if !out.Confirmed {
			doc.Line("Sent to the user's chart, but the page did not confirm in time (it may still be loading a script's history). Look again with chart_view before you speak of the values.")
		} else {
			rows := make([][]string, 0, len(out.Results))
			failed := 0
			for i, r := range out.Results {
				what := "?"
				if i < len(changes) {
					what = changeText(changes[i])
				}
				state := "done"
				if !r.OK {
					state, failed = "not done: "+r.Error, failed+1
				}
				rows = append(rows, []string{what, orDash(r.ID), state})
			}
			if failed == 0 {
				doc.Line("Done on the user's chart. Say in the chat what you changed and why.")
			} else {
				doc.Line("%d of %d changes were made; the rest say why not.", len(rows)-failed, len(rows))
			}
			doc.Table("Changes", []string{"change", "id", "result"}, rows, nil)
		}
		if v := out.View; v != nil {
			last := lastValues(v.Series, len(v.Bars))
			irows := make([][]string, 0, len(v.Indicators))
			for _, ind := range v.Indicators {
				irows = append(irows, []string{ind.ID, ind.Name, settingsText(ind.Inputs), byText(ind.By), stateText(ind.Hidden, last["ind:"+ind.ID+"/"])})
			}
			for _, sc := range v.Scripts {
				irows = append(irows, []string{orDash(sc.ID), sc.Name + " (script " + sc.Ref + ")", settingsText(sc.Inputs), byText(sc.By), stateText(sc.Hidden, "")})
			}
			for _, m := range v.MarketRows {
				irows = append(irows, []string{m.ID, m.Name, "—", byText(m.By), stateText(m.Hidden, "")})
			}
			doc.Blank()
			if len(irows) == 0 {
				doc.Line("The chart (%s %s %s) has no indicators now.", v.Venue, v.Pair, v.Timeframe)
			} else {
				doc.Table(fmt.Sprintf("On the chart now (%s %s %s; chart_view gives every value at each bar)", v.Venue, v.Pair, v.Timeframe),
					[]string{"id", "name", "settings", "by", "state / newest value"}, irows, nil)
			}
		}
		return toolText(doc.String()), nil
	})
}

// changeText says a change back in a few words: "add RSI {length 30}", "set i1".
func changeText(c map[string]any) string {
	op, _ := c["op"].(string)
	parts := []string{op}
	for _, k := range []string{"fn", "script", "market", "id"} {
		if v, ok := c[k].(string); ok && v != "" {
			parts = append(parts, v)
		}
	}
	if in, ok := c["inputs"].(map[string]any); ok && len(in) > 0 {
		b, _ := json.Marshal(in)
		parts = append(parts, string(b))
	}
	return strings.Join(parts, " ")
}

func byText(by string) string {
	if by == "ai" {
		return "you"
	}
	return "user"
}

// settingsText writes inputs as key value pairs in key order: "length 30, source close".
func settingsText(raw json.RawMessage) string {
	m := map[string]any{}
	if len(raw) == 0 || json.Unmarshal(raw, &m) != nil || len(m) == 0 {
		return "defaults"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %v", k, m[k])
	}
	return strings.Join(parts, ", ")
}

func stateText(hidden bool, value string) string {
	switch {
	case hidden:
		return "hidden"
	case value != "":
		return value
	}
	return "on the chart"
}

// lastValues gives each indicator's newest values by its key prefix
// ("ind:i1/"): its lines, joined.
func lastValues(series []chartSeries, bars int) map[string]string {
	out := map[string]string{}
	for _, s := range series {
		slash := strings.Index(s.Key, "/")
		if slash < 0 || len(s.Values) != bars || bars == 0 {
			continue
		}
		v := s.Values[bars-1]
		if v == nil {
			continue
		}
		prefix := s.Key[:slash+1]
		text := render.Px(*v)
		if out[prefix] != "" {
			text = out[prefix] + " / " + text
		}
		out[prefix] = text
	}
	return out
}
