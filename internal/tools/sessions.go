package tools

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

// RegisterSessions adds list_sessions, get_session_detail, delete_session, update_session_metadata.
// All call frontend_api/api/v1/dev/* via the kdraigo.com gateway.
func RegisterSessions(s *server.MCPServer, d Deps) {
	addListSessions(s, d)
	addGetSessionDetail(s, d)
	addDeleteSession(s, d)
	addUpdateSessionMetadata(s, d)
}

func addListSessions(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("list_sessions",
		mcp.WithDescription("List the caller's backtest sessions with pagination."),
		mcp.WithNumber("page", mcp.Description("1-indexed page number"), mcp.DefaultNumber(1)),
		mcp.WithNumber("per_page", mcp.Description("Page size"), mcp.DefaultNumber(20)),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		page := req.GetInt("page", 1)
		perPage := req.GetInt("per_page", 20)
		if page < 1 {
			page = 1
		}
		if perPage < 1 {
			perPage = 20
		}
		// frontend_api paginates by limit/offset, not page/per_page — translate.
		q := url.Values{}
		q.Set("limit", strconv.Itoa(perPage))
		q.Set("offset", strconv.Itoa((page-1)*perPage))
		body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodGet, client.FrontendAPI, "/api/v1/dev/sessions", q, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("list_sessions", status, body)), nil
		}
		return toolText(string(body)), nil
	})
}

func addGetSessionDetail(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("get_session_detail",
		mcp.WithDescription("Fetch a single session's metadata and wallet snapshots. Orders and analytics are served by separate endpoints/tools, not by this call."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodGet, client.FrontendAPI, "/api/v1/dev/sessions/"+id, nil, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("get_session_detail", status, body)), nil
		}
		return toolText(string(body)), nil
	})
}

func addDeleteSession(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("delete_session",
		mcp.WithDescription("Delete a session and all of its data."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodDelete, client.FrontendAPI, "/api/v1/dev/sessions/"+id, nil, nil)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("delete_session", status, body)), nil
		}
		return toolText(`{"deleted":true}`), nil
	})
}

func addUpdateSessionMetadata(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("update_session_metadata",
		mcp.WithDescription("Update a session's user-facing metadata (name, notes, tags, favorite). Only fields provided are changed."),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session UUID")),
		mcp.WithString("name", mcp.Description("Optional new session name")),
		mcp.WithString("notes", mcp.Description("Optional free-form notes")),
		mcp.WithArray("tags", mcp.Description("Optional tags array"), mcp.WithStringItems()),
		mcp.WithBoolean("favorite", mcp.Description("Optional favorite flag")),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("session_id")
		if err != nil {
			return toolErr(err), nil
		}
		payload := map[string]any{}
		if v := req.GetString("name", ""); v != "" {
			payload["name"] = v
		}
		if v := req.GetString("notes", ""); v != "" {
			payload["notes"] = v
		}
		if v := req.GetStringSlice("tags", nil); v != nil {
			payload["tags"] = v
		}
		if args := req.GetArguments(); args != nil {
			if _, ok := args["favorite"]; ok {
				// frontend_api's DTO field is is_favorite, not favorite.
				payload["is_favorite"] = req.GetBool("favorite", false)
			}
		}
		body, status, err := d.HTTP.Do(ctx, true, client.HeaderStyleStandard, http.MethodPatch, client.FrontendAPI, "/api/v1/dev/sessions/"+id+"/metadata", nil, payload)
		if err != nil {
			return toolErr(err), nil
		}
		if status < 200 || status >= 300 {
			return toolErr(httpErr("update_session_metadata", status, body)), nil
		}
		return toolText(string(body)), nil
	})
}
