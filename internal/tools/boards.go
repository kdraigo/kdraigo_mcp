package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

// RegisterBoards adds boards: the user's arrangements of widgets on the
// website (kdraigo.com/boards). A widget is data the website draws (a type,
// its params and a place), never code: every write is checked against the
// widget catalog, and a refusal names the field to fix. Moving a board into
// a syndicate is the user's, on the website.
func RegisterBoards(s *server.MCPServer, d Deps) {
	addWidgetTypes(s, d)
	addBoardList(s, d)
	addBoardOpen(s, d)
	addBoardCreate(s, d)
	addWidgetAdd(s, d)
	addWidgetUpdate(s, d)
	addWidgetRemove(s, d)
	addBoardSetLayout(s, d)
}

const boardsSite = "kdraigo.com/boards/"

type (
	place struct {
		X int `json:"x"`
		Y int `json:"y"`
		W int `json:"w"`
		H int `json:"h"`
	}
	boardWidget struct {
		ID     string          `json:"id"`
		Type   string          `json:"type"`
		Title  string          `json:"title"`
		At     place           `json:"at"`
		Params json.RawMessage `json:"params"`
	}
	board struct {
		Ref       string    `json:"ref"`
		Name      string    `json:"name"`
		Role      string    `json:"role"`
		Version   int64     `json:"version"`
		UpdatedBy string    `json:"updated_by"`
		UpdatedAt time.Time `json:"updated_at"`
		Syndicate *struct {
			Slug string `json:"slug"`
			Name string `json:"name"`
		} `json:"syndicate"`
		Spec struct {
			Widgets []boardWidget `json:"widgets"`
		} `json:"spec"`
	}
	widgetTypeDoc struct {
		Type        string          `json:"type"`
		Title       string          `json:"title"`
		Description string          `json:"description"`
		Size        place           `json:"size"`
		Example     json.RawMessage `json:"example"`
		Params      []struct {
			Name     string   `json:"name"`
			Kind     string   `json:"kind"`
			Required bool     `json:"required"`
			Values   []string `json:"values"`
			Min      *float64 `json:"min"`
			Max      *float64 `json:"max"`
			MaxItems int      `json:"max_items"`
			Default  any      `json:"default"`
			Doc      string   `json:"doc"`
		} `json:"params"`
	}
)

func boardPath(ref string) string { return labsAPI + "/boards/" + url.PathEscape(ref) }

func whoseBoard(b board) string {
	if b.Syndicate == nil {
		return "yours"
	}
	return fmt.Sprintf("%s (%s)", b.Syndicate.Name, b.Role)
}

// oneLineJSON is a widget's params on one line, keys sorted.
func oneLineJSON(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	b, _ := json.Marshal(v)
	return unescapeJSON(b)
}

// boardResult says what a write left: the version and where to look.
func boardResult(what string, b board) string {
	return fmt.Sprintf("%s Board %s is at version %d with %d widget(s); the user sees it at %s%s (it updates while open).",
		what, b.Ref, b.Version, len(b.Spec.Widgets), boardsSite, b.Ref)
}

// objectArg is an object argument as JSON; nil when absent.
func objectArg(req mcp.CallToolRequest, name string) (json.RawMessage, error) {
	v, ok := req.GetArguments()[name]
	if !ok || v == nil {
		return nil, nil
	}
	if s, isString := v.(string); isString {
		// Some clients send an object as its JSON text.
		if !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("%s: not JSON", name)
		}
		return json.RawMessage(s), nil
	}
	return json.Marshal(v)
}

func addWidgetTypes(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("widget_types",
		mcp.WithDescription("The widget catalog: every kind of widget a board can hold, its params (with allowed values and defaults) and an example. Read it before widget_add; the board refuses anything else and names the field."),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var types []widgetTypeDoc
		if err := d.getJSON(ctx, "widget_types", client.Research, labsAPI+"/widget-types", nil, &types); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		doc.Line("Widget types. A widget is {type, title?, params, at?}; at is {x, y, w, h} on a 12-column grid (left out: the type's size, below the others).")
		for _, t := range types {
			doc.Blank()
			doc.Line("## %s (%s): %s", t.Type, t.Title, t.Description)
			doc.Line("size %dx%d · example params: %s", t.Size.W, t.Size.H, oneLineJSON(t.Example))
			for _, p := range t.Params {
				var bits []string
				bits = append(bits, p.Kind)
				if p.Required {
					bits = append(bits, "required")
				}
				if len(p.Values) > 0 {
					bits = append(bits, "one of "+strings.Join(p.Values, "|"))
				}
				if p.Min != nil && p.Max != nil {
					bits = append(bits, fmt.Sprintf("%g–%g", *p.Min, *p.Max))
				}
				if p.MaxItems > 0 {
					bits = append(bits, fmt.Sprintf("at most %d", p.MaxItems))
				}
				if p.Default != nil {
					b, _ := json.Marshal(p.Default)
					bits = append(bits, "default "+string(b))
				}
				doc.Line("- %s (%s): %s", p.Name, strings.Join(bits, ", "), p.Doc)
			}
		}
		return toolText(doc.String()), nil
	})
}

func addBoardList(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("board_list",
		mcp.WithDescription("The user's boards and their syndicates' (as syndicate:board)."),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var list []board
		if err := d.getJSON(ctx, "board_list", client.Research, labsAPI+"/boards", nil, &list); err != nil {
			return toolErr(err), nil
		}
		if len(list) == 0 {
			return toolText("No boards yet. board_create makes one; widget_types lists what it can hold."), nil
		}
		rows := make([][]string, 0, len(list))
		for _, b := range list {
			rows = append(rows, []string{b.Ref, b.Name, whoseBoard(b), fmt.Sprint(b.Version), whoWrote(b.UpdatedBy), render.Date(b.UpdatedAt)})
		}
		doc := render.New()
		doc.Table("Boards", []string{"board", "name", "whose", "version", "last_by", "updated"}, rows, nil)
		return toolText(doc.String()), nil
	})
}

func whoWrote(by string) string {
	if by == "web" {
		return "the user"
	}
	return "an AI"
}

func addBoardOpen(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("board_open",
		mcp.WithDescription("A board's widgets: ids, types, titles, places and params. Open it before changing it."),
		mcp.WithString("board", mcp.Required(), mcp.Description("The board (board_list): its slug, or syndicate:slug")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("board")
		if err != nil {
			return toolErr(err), nil
		}
		var b board
		if err := d.getJSON(ctx, "board_open", client.Research, boardPath(ref), nil, &b); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		doc.Line("Board %s (%s) · %s · version %d, last changed by %s %s · %s%s",
			b.Name, b.Ref, whoseBoard(b), b.Version, whoWrote(b.UpdatedBy), render.Date(b.UpdatedAt), boardsSite, b.Ref)
		if b.Syndicate != nil {
			doc.Line(othersContent)
		}
		rows := make([][]string, 0, len(b.Spec.Widgets))
		for _, w := range b.Spec.Widgets {
			rows = append(rows, []string{w.ID, w.Type, orDash(w.Title), fmt.Sprintf("%d,%d %dx%d", w.At.X, w.At.Y, w.At.W, w.At.H), oneLineJSON(w.Params)})
		}
		doc.Table("Widgets", []string{"id", "type", "title", "at", "params"}, rows, nil)
		if b.Role == "viewer" {
			doc.Line("You are a viewer: you read this board; members change it.")
		}
		return toolText(doc.String()), nil
	})
}

func addBoardCreate(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("board_create",
		mcp.WithDescription("Create a board for the user (or in a syndicate they write in). Then add widgets with widget_add."),
		mcp.WithString("name", mcp.Required(), mcp.Description("The board's name; its slug is made from it")),
		mcp.WithString("syndicate", mcp.Description("A syndicate's slug to make it there (syndicate_list); empty: the user's own")),
		additive(false),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return toolErr(err), nil
		}
		var b board
		if err := d.sendJSON(ctx, "board_create", http.MethodPost, client.Research, labsAPI+"/boards",
			map[string]string{"name": name, "syndicate": req.GetString("syndicate", "")}, &b); err != nil {
			return toolErr(err), nil
		}
		return toolText(boardResult(fmt.Sprintf("Created %q.", b.Name), b) + " widget_types lists what it can hold."), nil
	})
}

func addWidgetAdd(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("widget_add",
		mcp.WithDescription("Add a widget to a board. params follow the type in widget_types; anything else is refused with the field named, so fix it and call again."),
		mcp.WithString("board", mcp.Required(), mcp.Description("The board: its slug, or syndicate:slug")),
		mcp.WithString("type", mcp.Required(), mcp.Description("The widget type (widget_types), e.g. price_chart")),
		mcp.WithObject("params", mcp.Required(), mcp.Description(`The type's params, e.g. {"exchange":"binance_futures","symbol":"BTC/USDT","timeframe":"15m","panes":["volume","cvd"]}`)),
		mcp.WithString("title", mcp.Description("A title shown on the widget (one line)")),
		mcp.WithObject("at", mcp.Description(`Place on the 12-column grid: {"x":0,"y":0,"w":8,"h":6}; left out, the type's size below the other widgets`)),
		additive(false),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("board")
		if err != nil {
			return toolErr(err), nil
		}
		typ, err := req.RequireString("type")
		if err != nil {
			return toolErr(err), nil
		}
		params, err := objectArg(req, "params")
		if err != nil {
			return toolErr(err), nil
		}
		at, err := objectArg(req, "at")
		if err != nil {
			return toolErr(err), nil
		}
		body := map[string]any{"type": typ, "title": req.GetString("title", ""), "params": params}
		if at != nil {
			body["at"] = at
		}
		var out struct {
			Widget string `json:"widget"`
			Board  board  `json:"board"`
		}
		if err := d.sendJSON(ctx, "widget_add", http.MethodPost, client.Research, boardPath(ref)+"/widgets", body, &out); err != nil {
			return toolErr(err), nil
		}
		return toolText(boardResult(fmt.Sprintf("Added %s (%s).", out.Widget, typ), out.Board)), nil
	})
}

func addWidgetUpdate(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("widget_update",
		mcp.WithDescription("Change a widget: what you pass replaces that part (params whole, as in widget_types). Checked like a new widget."),
		mcp.WithString("board", mcp.Required(), mcp.Description("The board")),
		mcp.WithString("widget", mcp.Required(), mcp.Description("The widget's id (board_open)")),
		mcp.WithObject("params", mcp.Description("New params, whole")),
		mcp.WithString("title", mcp.Description("New title")),
		mcp.WithObject("at", mcp.Description(`New place {"x","y","w","h"}`)),
		additive(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("board")
		if err != nil {
			return toolErr(err), nil
		}
		wid, err := req.RequireString("widget")
		if err != nil {
			return toolErr(err), nil
		}
		body := map[string]any{}
		for _, name := range []string{"params", "at"} {
			v, err := objectArg(req, name)
			if err != nil {
				return toolErr(err), nil
			}
			if v != nil {
				body[name] = v
			}
		}
		if t, ok := req.GetArguments()["title"].(string); ok {
			body["title"] = t
		}
		if len(body) == 0 {
			return toolErr(fmt.Errorf("widget_update: pass params, title or at")), nil
		}
		var b board
		if err := d.sendJSON(ctx, "widget_update", http.MethodPatch, client.Research, boardPath(ref)+"/widgets/"+url.PathEscape(wid), body, &b); err != nil {
			return toolErr(err), nil
		}
		return toolText(boardResult(fmt.Sprintf("Changed %s.", wid), b)), nil
	})
}

func addWidgetRemove(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("widget_remove",
		mcp.WithDescription("Remove a widget from a board."),
		mcp.WithString("board", mcp.Required(), mcp.Description("The board")),
		mcp.WithString("widget", mcp.Required(), mcp.Description("The widget's id (board_open)")),
		destructive(false),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("board")
		if err != nil {
			return toolErr(err), nil
		}
		wid, err := req.RequireString("widget")
		if err != nil {
			return toolErr(err), nil
		}
		var b board
		if err := d.sendJSON(ctx, "widget_remove", http.MethodDelete, client.Research, boardPath(ref)+"/widgets/"+url.PathEscape(wid), nil, &b); err != nil {
			return toolErr(err), nil
		}
		return toolText(boardResult(fmt.Sprintf("Removed %s.", wid), b)), nil
	})
}

func addBoardSetLayout(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("board_set_layout",
		mcp.WithDescription("Move and resize several widgets at once on the 12-column grid; widgets not named stay where they are."),
		mcp.WithString("board", mcp.Required(), mcp.Description("The board")),
		mcp.WithObject("places", mcp.Required(), mcp.Description(`widget id → place, e.g. {"w1":{"x":0,"y":0,"w":8,"h":6},"w2":{"x":8,"y":0,"w":4,"h":6}}`)),
		additive(true),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		ref, err := req.RequireString("board")
		if err != nil {
			return toolErr(err), nil
		}
		places, err := objectArg(req, "places")
		if err != nil || places == nil {
			return toolErr(fmt.Errorf("board_set_layout: places is required")), nil
		}
		var b board
		if err := d.sendJSON(ctx, "board_set_layout", http.MethodPut, client.Research, boardPath(ref)+"/layout",
			map[string]json.RawMessage{"places": places}, &b); err != nil {
			return toolErr(err), nil
		}
		return toolText(boardResult("Moved.", b)), nil
	})
}
