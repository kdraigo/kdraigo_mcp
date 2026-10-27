package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
	"github.com/kdraigo/kdraigo_mcp/internal/render"
)

type (
	workspaceParam struct {
		Name    string   `json:"name"`
		Kind    string   `json:"kind"`
		Default string   `json:"default"`
		Min     *float64 `json:"min"`
		Max     *float64 `json:"max"`
		Choices []string `json:"choices"`
		Help    string   `json:"help"`
		File    string   `json:"file"`
		Line    int      `json:"line"`
	}
	workspaceProblem struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Msg  string `json:"msg"`
	}
	workspaceParams struct {
		Params   []workspaceParam   `json:"params"`
		Problems []workspaceProblem `json:"problems"`
	}
)

func addWorkspaceParams(s *server.MCPServer, d Deps) {
	tool := mcp.NewTool("workspace_params",
		mcp.WithDescription("The parameters a workspace's strategy declares with dev_sdk/params: name, type, default, range or choices, help, and where. "+
			"The website shows them as a form, so the user can run the backtest again with other values without you; "+
			"workspace_run's env sets them by name, and a value outside its type or range is refused before the run starts. "+
			"Problems list declarations the platform cannot read (a name, default or option that is not a literal)."),
		mcp.WithString("workspace_id", mcp.Required(), mcp.Description("Workspace UUID")),
		readOnly(),
	)
	add(s, tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("workspace_id")
		if err != nil {
			return toolErr(err), nil
		}
		var out workspaceParams
		if err := d.getJSON(ctx, "workspace_params", client.Workspace, wsPath(id)+"/params", nil, &out); err != nil {
			return toolErr(err), nil
		}
		doc := render.New()
		if len(out.Params) == 0 {
			doc.Line("No declared parameters. Declare the strategy's settings with dev_sdk/params (get_sdk_docs section=\"parameters\"), so the user can change them on the website.")
		} else {
			rows := make([][]string, 0, len(out.Params))
			for _, p := range out.Params {
				allowed := render.Dash
				switch {
				case len(p.Choices) > 0:
					allowed = strings.Join(p.Choices, " | ")
				case p.Min != nil && p.Max != nil:
					allowed = render.Num(*p.Min, -1) + ".." + render.Num(*p.Max, -1)
				}
				rows = append(rows, []string{p.Name, p.Kind, p.Default, allowed, p.Help, fmt.Sprintf("%s:%d", p.File, p.Line)})
			}
			doc.Table(fmt.Sprintf("parameters (%d)", len(out.Params)), []string{"name", "kind", "default", "allowed", "help", "where"}, rows, nil)
		}
		if len(out.Problems) > 0 {
			doc.Line("Problems (the run is refused until they are fixed):")
			for _, p := range out.Problems {
				doc.Line("- %s:%d: %s", p.File, p.Line, p.Msg)
			}
		}
		return toolText(doc.String()), nil
	})
}
