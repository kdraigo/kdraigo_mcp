package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Prompts are the platform's workflows as the user starts them: a client
// that shows MCP prompts (Claude Code lists them as slash commands) offers
// each one ready to run. Each says what to call and what not to do without
// asking, the same rules the guide gives.

type promptArg struct {
	name, about string
	required    bool
}

type workflow struct {
	name, about string
	args        []promptArg
	text        func(args map[string]string) string
}

var workflows = []workflow{
	{
		name:  "get_started",
		about: "What kdraigo can do for me: my plan, what I have, where to begin",
		text: func(map[string]string) string {
			return "Call get_started, then tell me in plain words what I can do on kdraigo with my plan, what I already have (labs, workspaces, live bots, boards), " +
				"and the one next step you would suggest. Keep it short; don't run or create anything."
		},
	},
	{
		name:  "continue_research",
		about: "Pick up a research lab where the last session stopped",
		args:  []promptArg{{"lab", "The lab's slug; empty: the most recently updated", false}},
		text: func(a map[string]string) string {
			lab := "my most recently updated lab (lab_list)"
			if a["lab"] != "" {
				lab = "lab " + a["lab"]
			}
			return fmt.Sprintf("Open %s with lab_open and check it with lab_lint. Tell me where the last session stopped (its handoff), the experiments under test and their verdicts so far, "+
				"what lab_lint flags, and the next step you propose. Don't start a run until I agree.", lab)
		},
	},
	{
		name:  "new_strategy_idea",
		about: "Turn a trading idea into a pre-registered experiment",
		args:  []promptArg{{"idea", "The idea in a sentence or two", true}},
		text: func(a map[string]string) string {
			return fmt.Sprintf("My idea: %s\n\nFirst read get_guide topic=labs and topic=loop. Search the library (library_search) for published results on it, above all null or failed ones, "+
				"and check my labs for earlier tries. Then propose an experiment: hypothesis, mechanism (why it should work), the train, test and hold-out windows, "+
				"a pass rule decided now (e.g. return/DD against a baseline on the test window and x of N instruments positive), and the scope. "+
				"Register nothing and run nothing until I agree.", a["idea"])
		},
	},
	{
		name:  "validate_strategy",
		about: "Is this backtest result real? Monte Carlo, significance, luck from the search, overfitting",
		args: []promptArg{
			{"session_id", "The backtest session", true},
			{"trials", "How many variants or versions were tried to find it", false},
			{"workspace_id", "Its cloud workspace, for a sensitivity sweep of its settings", false},
			{"run_id", "The sweep it came from, to measure overfitting", false},
		},
		text: func(a map[string]string) string {
			var b strings.Builder
			fmt.Fprintf(&b, "Validate backtest %s. Read get_guide topic=validation first. Then call validate_strategy with session_id=%s", a["session_id"], a["session_id"])
			if a["trials"] != "" {
				fmt.Fprintf(&b, " and trials=%s", a["trials"])
			} else {
				b.WriteString("; if you don't know how many variants were tried to find it, count them from its lab's runs or ask me, and pass that as trials")
			}
			if a["workspace_id"] != "" {
				fmt.Fprintf(&b, " and workspace_id=%s", a["workspace_id"])
			}
			if a["run_id"] != "" {
				fmt.Fprintf(&b, " and run_id=%s (then get_overfitting_report run_id=%s for the folds and the grid)", a["run_id"], a["run_id"])
			}
			b.WriteString(". Explain every weak and caution line in plain words, with its figures. Give the drawdown to plan for (the Monte Carlo p95, not the backtest's own). " +
				"Say what would make the result convincing, and propose the sensitivity sweep if there is one, but don't run it until I agree.")
			return b.String()
		},
	},
	{
		name:  "check_my_bots",
		about: "How my live bots are doing, and anything that needs me",
		text: func(map[string]string) string {
			return "Check my live bots: list_live_sessions, then get_live_session for each one running. For each, give the wallet now against the start and the open positions with what protects them. " +
				"Flag plainly any bot that is DISCONNECTED and any position without a stop. Read only: change nothing, stop nothing."
		},
	},
	{
		name:  "chart_together",
		about: "Look at my chart with me: read it, check my drawings against the data, mark it up",
		args:  []promptArg{{"question", "What I want to know about the chart; empty: your reading of it", false}},
		text: func(a map[string]string) string {
			q := "Give me your reading of it"
			if a["question"] != "" {
				q = "My question: " + a["question"]
			}
			return "I am sharing my chart with you (kdraigo.com/chart, \"Share with my AI\"). Read get_guide topic=chart, then look with chart_view. " + q + ". " +
				"Quote the prices, times and indicator values you rely on, check my drawings against what the bars did at them, and draw only what helps (a few labelled drawings), saying what you drew. " +
				"If we find an idea worth keeping, note it in the lab the session logs to and propose how to test it; run nothing until I agree."
		},
	},
	{
		name:  "chart_trade_review",
		about: "Before I trade: the chart, my plan and my live bots, looked at together",
		args:  []promptArg{{"plan", "The trade I am thinking of, in a sentence; empty: ask me", false}},
		text: func(a map[string]string) string {
			plan := "Ask me what trade I am thinking of."
			if a["plan"] != "" {
				plan = "My plan: " + a["plan"]
			}
			return "I am sharing my chart with you (kdraigo.com/chart). Read get_guide topic=chart and topic=live, look with chart_view, and check my live bots (list_live_sessions) for positions already open on this market. " + plan + " " +
				"Draw the plan as a position (entry, stop, target) with its risk to reward, tell me what on the chart supports it and what speaks against it, and where it is wrong (the invalidation). " +
				"You place no orders and change no bot: the decision is mine. Save the chart with chart_snapshot and note the plan in the session's lab, so we can look back at it."
		},
	},
	{
		name:  "chart_lesson",
		about: "Teach me a trading topic on my chart, with past examples and replay",
		args:  []promptArg{{"topic", "What to learn, e.g. market structure, support and resistance, reading RSI divergence", true}},
		text: func(a map[string]string) string {
			return fmt.Sprintf("Teach me on my chart: %s. I am sharing it with you (kdraigo.com/chart). Read get_guide topic=chart, then look with chart_view. "+
				"Go in small steps: explain one thing in a few sentences, show it on a real past example (chart_focus to go there, a few labelled drawings), then test me: "+
				"start a replay before the next example (chart_replay), ask what I expect, step the bars and look again. Say plainly when a pattern failed, and how often such things work is a question for a backtest, not for an example. "+
				"Keep a short note of what we covered in the session's lab.", a["topic"])
		},
	},
	{
		name:  "build_a_desk",
		about: "A board of charts and market data for a market or a theme",
		args:  []promptArg{{"theme", "What the desk is for, e.g. BTC futures intraday, or US tech stocks", true}},
		text: func(a map[string]string) string {
			return fmt.Sprintf("Build me a board for: %s. Read widget_types and get_guide topic=boards, propose the widgets and their layout in a few lines, "+
				"then board_create and widget_add one widget at a time. Tell me where to open it (kdraigo.com/boards).", a["theme"])
		},
	},
}

// RegisterPrompts adds the workflows as MCP prompts.
func RegisterPrompts(s *server.MCPServer) {
	for _, w := range workflows {
		opts := []mcp.PromptOption{mcp.WithPromptDescription(w.about)}
		for _, a := range w.args {
			aopts := []mcp.ArgumentOption{mcp.ArgumentDescription(a.about)}
			if a.required {
				aopts = append(aopts, mcp.RequiredArgument())
			}
			opts = append(opts, mcp.WithArgument(a.name, aopts...))
		}
		s.AddPrompt(mcp.NewPrompt(w.name, opts...), func(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			args := map[string]string{}
			for _, a := range w.args {
				v := strings.TrimSpace(req.Params.Arguments[a.name])
				if a.required && v == "" {
					return nil, fmt.Errorf("%s needs %s", w.name, a.name)
				}
				args[a.name] = v
			}
			return mcp.NewGetPromptResult(w.about, []mcp.PromptMessage{
				mcp.NewPromptMessage(mcp.RoleUser, mcp.NewTextContent(w.text(args))),
			}), nil
		})
	}
}
