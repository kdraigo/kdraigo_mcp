package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// RegisterScripts adds chart scripts: indicators in kdscript (Pine-shaped,
// kdraigo's own) that the AI writes and a board's price_chart draws. Saving
// checks a script and never runs it; script_preview runs it on the platform
// in an isolated runner and shows the numbers, so the AI can check its
// indicator before the user looks at it.
func RegisterScripts(s *server.MCPServer, d Deps) {
	addScriptReference(s, d)
	addScriptList(s, d)
	addScriptOpen(s, d)
	addScriptCheck(s, d)
	addScriptSave(s, d)
	addScriptPreview(s, d)
	addScriptRemove(s, d)
}

type (
	scriptDiag struct {
		Pos struct {
			Line int `json:"line"`
			Col  int `json:"col"`
		} `json:"pos"`
		Msg     string `json:"msg"`
		Warning bool   `json:"warning"`
	}
	scriptInput struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Title   string   `json:"title"`
		Default any      `json:"default"`
		Min     *float64 `json:"min"`
		Max     *float64 `json:"max"`
	}
	scriptInfo struct {
		Title   string        `json:"title"`
		Overlay bool          `json:"overlay"`
		Inputs  []scriptInput `json:"inputs"`
		Plots   []struct {
			Key   string `json:"key"`
			Title string `json:"title"`
			Kind  string `json:"kind"`
		} `json:"plots"`
	}
	script struct {
		Ref       string       `json:"ref"`
		Name      string       `json:"name"`
		Role      string       `json:"role"`
		Version   int64        `json:"version"`
		Source    string       `json:"source"`
		Info      scriptInfo   `json:"info"`
		UpdatedBy string       `json:"updated_by"`
		UpdatedAt time.Time    `json:"updated_at"`
		Diags     []scriptDiag `json:"diags"`
		Syndicate *struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"syndicate"`
	}
	scriptPoint struct {
		T  int64    `json:"t"`
		V  float64  `json:"v"`
		V2 *float64 `json:"v2"`
		S  string   `json:"s"`
	}
	previewResult struct {
		Info  *scriptInfo  `json:"info"`
		Diags []scriptDiag `json:"diags"`
		Error *struct {
			Pos struct {
				Line int `json:"line"`
				Col  int `json:"col"`
			} `json:"pos"`
			Bar int64  `json:"bar"`
			Msg string `json:"msg"`
		} `json:"error"`
		Plots []struct {
			Key    string        `json:"key"`
			Title  string        `json:"title"`
			Kind   string        `json:"kind"`
			Points int           `json:"points"`
			First  *int64        `json:"first"`
			Min    *float64      `json:"min"`
			Max    *float64      `json:"max"`
			Last   []scriptPoint `json:"last"`
		} `json:"plots"`
		Bars   int   `json:"bars"`
		From   int64 `json:"from"`
		To     int64 `json:"to"`
		Millis int64 `json:"ms"`
	}
)

func scriptPath(ref string) string { return labsAPI + "/scripts/" + url.PathEscape(ref) }

func whoseScript(sc script) string {
	if sc.Syndicate == nil {
		return "yours"
	}
	return fmt.Sprintf("%s (%s)", sc.Syndicate.Name, sc.Role)
}

func msTime(ms int64) string { return render.Time(time.UnixMilli(ms).UTC()) }

// sig writes an indicator's value with up to 8 significant digits, no
// padding: 1.5, 65432.123, 0.00012345679.
func sig(v float64) string {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	r, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 8, 64), 64)
	return strconv.FormatFloat(r, 'f', -1, 64)
}

func optSig(v *float64) string {
	if v == nil {
		return "-"
	}
	return sig(*v)
}

func inputsLine(in []scriptInput) string {
	if len(in) == 0 {
		return "none"
	}
	parts := make([]string, len(in))
	for i, x := range in {
		d, _ := json.Marshal(x.Default)
		bits := x.Kind + ", default " + string(d)
		switch {
		case x.Min != nil && x.Max != nil:
			bits += fmt.Sprintf(", %s–%s", sig(*x.Min), sig(*x.Max))
		case x.Min != nil:
			bits += ", min " + sig(*x.Min)
		case x.Max != nil:
			bits += ", max " + sig(*x.Max)
		}
		parts[i] = fmt.Sprintf("%s (%s)", x.Name, bits)
	}
	return strings.Join(parts, "; ")
}

func plotsLine(info scriptInfo) string {
	if len(info.Plots) == 0 {
		return "none"
	}
	parts := make([]string, len(info.Plots))
	for i, p := range info.Plots {
		parts[i] = fmt.Sprintf("%s %q (%s)", p.Key, p.Title, p.Kind)
	}
	return strings.Join(parts, "; ")
}

func diagLines(doc *render.Doc, diags []scriptDiag) {
	for _, d := range diags {
		kind := "error"
		if d.Warning {
			kind = "warning"
		}
		doc.Line("- line %d:%d %s: %s", d.Pos.Line, d.Pos.Col, kind, d.Msg)
	}
}

func addScriptReference(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("script_reference",
		mcp.WithDescription("The kdscript language guide: how a chart script is written (Pine-shaped, with what it leaves out), every built-in, inputs, drawing calls and complete examples. Read it before writing a script."),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var out struct {
			Version   string `json:"version"`
			Reference string `json:"reference"`
		}
		if err := d.getJSON(ctx, "script_reference", client.Research, labsAPI+"/scripts/reference", nil, &out); err != nil {
			return toolErr(err), nil
		}
		return toolText(out.Reference), nil
	})
}

func addScriptList(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("script_list",
		mcp.WithDescription("The user's chart scripts and their syndicates' (as syndicate:script), with their inputs."),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var list []script
		if err := d.getJSON(ctx, "script_list", client.Research, labsAPI+"/scripts", nil, &list); err != nil {
			return toolErr(err), nil
		}
		if len(list) == 0 {
			return toolText("No scripts yet. Read script_reference, then script_save makes one."), nil
		}
		rows := make([][]string, 0, len(list))
		for _, sc := range list {
			rows = append(rows, []string{sc.Ref, sc.Name, whoseScript(sc), sc.Info.Title, inputsLine(sc.Info.Inputs),
				fmt.Sprint(len(sc.Info.Plots)), fmt.Sprint(sc.Version), whoWrote(sc.UpdatedBy), render.Date(sc.UpdatedAt)})
		}
		doc := render.New()
		doc.Table("Scripts", []string{"script", "name", "whose", "title", "inputs", "plots", "version", "last_by", "updated"}, rows, nil)
		return toolText(doc.String()), nil
	})
}

func addScriptOpen(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("script_open",
		mcp.WithDescription("A chart script's source, inputs, plots and version. Open it before changing it."),
		mcp.WithString("script", mcp.Required(), mcp.Description("The script (script_list): its slug, or syndicate:slug")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("script")
		if err != nil {
			return toolErr(err), nil
		}
		var sc script
		if err := d.getJSON(ctx, "script_open", client.Research, scriptPath(ref), nil, &sc); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		doc.Line("Script %s (%s) · %s · version %d, last saved by %s %s", sc.Name, sc.Ref, whoseScript(sc), sc.Version, whoWrote(sc.UpdatedBy), render.Date(sc.UpdatedAt))
		if sc.Syndicate != nil {
			doc.Line(othersContent)
		}
		doc.Line("Draws %q %s. Inputs: %s. Plots: %s.", sc.Info.Title, map[bool]string{true: "over the candles", false: "in a pane"}[sc.Info.Overlay], inputsLine(sc.Info.Inputs), plotsLine(sc.Info))
		doc.Blank()
		doc.Line("```")
		doc.Line("%s", strings.TrimRight(sc.Source, "\n"))
		doc.Line("```")
		if sc.Role == "viewer" {
			doc.Line("You are a viewer: you read this script; members change it.")
		}
		return toolText(doc.String()), nil
	})
}

func addScriptCheck(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("script_check",
		mcp.WithDescription("Check a draft script without saving it: its problems by line and column, or its inputs and plots. Nothing runs."),
		mcp.WithString("source", mcp.Required(), mcp.Description("The whole script")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		src, err := req.RequireString("source")
		if err != nil {
			return toolErr(err), nil
		}
		var out struct {
			OK    bool         `json:"ok"`
			Info  *scriptInfo  `json:"info"`
			Diags []scriptDiag `json:"diags"`
		}
		if err := d.sendJSON(ctx, "script_check", http.MethodPost, client.Research, labsAPI+"/scripts/check", map[string]string{"source": src}, &out); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		if !out.OK {
			doc.Line("The script has problems; fix them and check again:")
			diagLines(doc, out.Diags)
			return toolText(doc.String()), nil
		}
		doc.Line("The script passes. %q draws %s. Inputs: %s. Plots: %s.", out.Info.Title,
			map[bool]string{true: "over the candles", false: "in a pane"}[out.Info.Overlay], inputsLine(out.Info.Inputs), plotsLine(*out.Info))
		diagLines(doc, out.Diags)
		return toolText(doc.String()), nil
	})
}

func addScriptSave(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("script_save",
		mcp.WithDescription("Save a chart script: a new one (name, and syndicate to make it there) or over an existing one (script). It is checked first; problems come back by line and column and nothing is saved. Then draw it on a board: widget_add or widget_update a price_chart with params.scripts [{script, inputs}]."),
		mcp.WithString("source", mcp.Required(), mcp.Description("The whole script (script_reference is the language)")),
		mcp.WithString("script", mcp.Description("An existing script to save over: its slug, or syndicate:slug. Empty: a new script")),
		mcp.WithString("name", mcp.Description("A new script's name (its slug is made from it); for an existing one, a new name")),
		mcp.WithString("syndicate", mcp.Description("A syndicate's slug to make a new script there (a syndicate's boards draw its scripts)")),
		mcp.WithNumber("base_version", mcp.Description("The version you opened (script_open); left out, you save over the newest")),
		additive(false),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		src, err := req.RequireString("source")
		if err != nil {
			return toolErr(err), nil
		}
		ref, name := req.GetString("script", ""), req.GetString("name", "")
		var sc script
		if ref == "" {
			if name == "" {
				return toolErr(fmt.Errorf("script_save: a new script needs a name (or pass script to save over one)")), nil
			}
			body := map[string]string{"name": name, "syndicate": req.GetString("syndicate", ""), "source": src}
			if err := d.sendJSON(ctx, "script_save", http.MethodPost, client.Research, labsAPI+"/scripts", body, &sc); err != nil {
				return toolErr(err), nil
			}
		} else {
			base := int64(req.GetFloat("base_version", 0))
			if base == 0 {
				var cur script
				if err := d.getJSON(ctx, "script_save", client.Research, scriptPath(ref), nil, &cur); err != nil {
					return toolErr(err), nil
				}
				base = cur.Version
			}
			body := map[string]any{"source": src, "name": name, "base_version": base}
			if err := d.sendJSON(ctx, "script_save", http.MethodPut, client.Research, scriptPath(ref), body, &sc); err != nil {
				return toolErr(err), nil
			}
		}
		doc := render.New()
		doc.Line("Saved %s (%s), version %d. It draws %q %s. Inputs: %s. Plots: %s.", sc.Name, sc.Ref, sc.Version, sc.Info.Title,
			map[bool]string{true: "over the candles", false: "in a pane"}[sc.Info.Overlay], inputsLine(sc.Info.Inputs), plotsLine(sc.Info))
		diagLines(doc, sc.Diags)
		slug := sc.Ref
		if i := strings.IndexByte(slug, ':'); i >= 0 {
			slug = slug[i+1:]
		}
		doc.Line("Check its numbers with script_preview; draw it with a price_chart's params.scripts [{\"script\":%q}].", slug)
		return toolText(doc.String()), nil
	})
}

func addScriptPreview(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("script_preview",
		mcp.WithDescription("Run a chart script on the newest bars of a pair (on the platform, isolated) and show each plot's last values, first value time, min and max, or where it failed. Use it to check an indicator's numbers before drawing it."),
		mcp.WithString("script", mcp.Description("A saved script: its slug, or syndicate:slug")),
		mcp.WithString("source", mcp.Description("Or a draft script's whole source (not saved)")),
		mcp.WithString("exchange", mcp.Required(), mcp.Description("binance, binance_futures, bybit or bybit_futures")),
		mcp.WithString("symbol", mcp.Required(), mcp.Description("Pair as BASE/QUOTE, e.g. BTC/USDT")),
		mcp.WithString("timeframe", mcp.Description("1m, 5m, 15m, 30m, 1h (default), 4h, 1d, 1w")),
		mcp.WithObject("inputs", mcp.Description(`The script's inputs by name, e.g. {"len": 21}`)),
		mcp.WithNumber("bars", mcp.Description("How many of the newest bars to run over (default 500, at most 5000)")),
		mcp.WithNumber("last", mcp.Description("How many of each plot's newest values to show (default 20, at most 500)")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		exchange, err := req.RequireString("exchange")
		if err != nil {
			return toolErr(err), nil
		}
		symbol, err := req.RequireString("symbol")
		if err != nil {
			return toolErr(err), nil
		}
		inputs, err := objectArg(req, "inputs")
		if err != nil {
			return toolErr(err), nil
		}
		body := map[string]any{"script": req.GetString("script", ""), "source": req.GetString("source", ""),
			"exchange": exchange, "symbol": symbol, "timeframe": req.GetString("timeframe", ""),
			"bars": req.GetInt("bars", 0), "last": req.GetInt("last", 20)}
		if inputs != nil {
			body["inputs"] = inputs
		}
		var out previewResult
		if err := d.sendJSON(ctx, "script_preview", http.MethodPost, client.Research, labsAPI+"/scripts/preview", body, &out); err != nil {
			return toolErr(err), nil
		}
		return toolText(renderPreview(out)), nil
	})
}

func renderPreview(out previewResult) string {
	doc := render.New()
	if out.Info == nil {
		doc.Line("The script has problems; fix them and preview again:")
		diagLines(doc, out.Diags)
		return doc.String()
	}
	span := ""
	if out.Bars > 0 {
		span = fmt.Sprintf(" from %s to %s", msTime(out.From), msTime(out.To))
	}
	doc.Line("Ran %q on %d bars%s in %d ms.", out.Info.Title, out.Bars, span, out.Millis)
	if out.Error != nil {
		where := ""
		if out.Error.Pos.Line > 0 {
			where = fmt.Sprintf(" at line %d:%d", out.Error.Pos.Line, out.Error.Pos.Col)
		}
		if out.Error.Bar > 0 {
			where += " on the bar of " + msTime(out.Error.Bar)
		}
		doc.Line("It failed%s: %s. The values below are from the bars before.", where, out.Error.Msg)
	}
	diagLines(doc, out.Diags)
	if len(out.Plots) == 0 {
		return doc.String()
	}
	summary := make([][]string, 0, len(out.Plots))
	for _, p := range out.Plots {
		first := "-"
		if p.First != nil {
			first = msTime(*p.First)
		}
		summary = append(summary, []string{p.Key, p.Title, p.Kind, fmt.Sprint(p.Points), first, optSig(p.Min), optSig(p.Max)})
	}
	doc.Table("Plots", []string{"plot", "title", "kind", "values", "first", "min", "max"}, summary, nil)

	// The newest values side by side, one row per bar.
	byTime := map[int64][]string{}
	var times []int64
	cols := []string{"time"}
	for i, p := range out.Plots {
		cols = append(cols, p.Key)
		for _, pt := range p.Last {
			row, ok := byTime[pt.T]
			if !ok {
				row = make([]string, len(out.Plots))
				for k := range row {
					row[k] = "-"
				}
				byTime[pt.T] = row
				times = append(times, pt.T)
			}
			v := sig(pt.V)
			if pt.V2 != nil {
				v += " / " + sig(*pt.V2)
			}
			if pt.S != "" {
				v += " " + pt.S
			}
			row[i] = v
		}
	}
	sort.Slice(times, func(a, b int) bool { return times[a] < times[b] })
	rows := make([][]string, 0, len(times))
	for _, t := range times {
		rows = append(rows, append([]string{msTime(t)}, byTime[t]...))
	}
	doc.Table("Newest values (- : no value on that bar; bands are upper / lower; markers show where they fire)", cols, rows, nil)
	return doc.String()
}

func addScriptRemove(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("script_remove",
		mcp.WithDescription("Delete a chart script. Refused while a board draws it: take it off that board's charts first."),
		mcp.WithString("script", mcp.Required(), mcp.Description("The script: its slug, or syndicate:slug")),
		destructive(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("script")
		if err != nil {
			return toolErr(err), nil
		}
		if err := d.sendJSON(ctx, "script_remove", http.MethodDelete, client.Research, scriptPath(ref), nil, nil); err != nil {
			return toolErr(err), nil
		}
		return toolText(fmt.Sprintf("Deleted the script %s.", ref)), nil
	})
}
