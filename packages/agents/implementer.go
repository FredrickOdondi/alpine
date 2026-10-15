package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/alpine-dev/alpine/packages/shared/llm"
)

// Workspace is the implementer's view of one ticket checkout. Every path is relative to the repo
// root and implementations must confine all access to it; RunCommand executes inside the sandbox.
type Workspace interface {
	ReadFile(ctx context.Context, path string) (string, error)
	ListFiles(ctx context.Context, dir string) ([]string, error)
	Search(ctx context.Context, pattern, dir string) (string, error)
	WriteFile(ctx context.Context, path, content string) error
	ReplaceInFile(ctx context.Context, path, oldText, newText string) error
	RunCommand(ctx context.Context, command string) (CommandResult, error)
}

// CommandResult is a sandboxed command outcome (output already size-capped).
type CommandResult struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

// LoopOptions bounds one implementer activity.
type LoopOptions struct {
	MaxTurns      int     // model calls this activity may make
	MaxCostUSD    float64 // stop before exceeding (cumulative for this activity)
	FirstTurn     int     // ticket-wide number of the first turn (for idempotency keys/records)
	ToolOutputCap int     // chars of tool output fed back per call (default 6000)
}

// LoopTurn is reported after every model call (persisted as an agent turn).
type LoopTurn struct {
	N         int
	Model     string
	Usage     llm.Usage
	CostUSD   float64
	Replayed  bool
	ToolCalls []ToolCallSummary
}

// LoopResult is the implementer loop outcome.
type LoopResult struct {
	Summary       string
	ReadyForGates bool
	Turns         int
	CostUSD       float64
	Usage         llm.Usage
	StopReason    string // finished | turn_cap | cost_cap | no_tool_call
}

// ToolLoop is implemented by engines that run the real tool-using implementer.
type ToolLoop interface {
	ImplementLoop(ctx context.Context, c Call, in ImplementInput, ws Workspace, opts LoopOptions, onTurn func(LoopTurn) error) (LoopResult, error)
}

var _ ToolLoop = (*LLM)(nil)

func obj(props string, required ...string) json.RawMessage {
	req, _ := json.Marshal(required)
	return json.RawMessage(`{"type":"object","additionalProperties":false,"properties":{` + props + `},"required":` + string(req) + `}`)
}

// implementerTools are the sandbox tools; finish carries the structured final answer.
var implementerTools = []llm.Tool{
	{Name: "list_files", Description: "List files under a directory of the repo (recursive, max 300 entries). Use \".\" for the root.",
		Parameters: obj(`"dir":{"type":"string"}`, "dir")},
	{Name: "search", Description: "Search file contents with a regular expression (like grep -rnE). Returns matching lines with file:line.",
		Parameters: obj(`"pattern":{"type":"string"},"dir":{"type":"string","description":"directory to search, \".\" for all"}`, "pattern", "dir")},
	{Name: "read_file", Description: "Read a UTF-8 text file from the repo.",
		Parameters: obj(`"path":{"type":"string"}`, "path")},
	{Name: "write_file", Description: "Create or overwrite a file with the full new content.",
		Parameters: obj(`"path":{"type":"string"},"content":{"type":"string"}`, "path", "content")},
	{Name: "replace_in_file", Description: "Replace exactly one occurrence of old_text with new_text in a file. old_text must match the file exactly (including whitespace) and be unique.",
		Parameters: obj(`"path":{"type":"string"},"old_text":{"type":"string"},"new_text":{"type":"string"}`, "path", "old_text", "new_text")},
	{Name: "run_command", Description: "Run a shell command in the repo root inside the offline sandbox (e.g. `go test ./...`). Returns exit code and combined output (truncated).",
		Parameters: obj(`"command":{"type":"string"}`, "command")},
	{Name: "finish", Description: "Finish: the change is complete and tests pass (ready_for_gates=true), or you cannot proceed (false).",
		Parameters: obj(`"summary":{"type":"string","description":"what changed and why, 1-4 sentences"},"ready_for_gates":{"type":"boolean"}`, "summary", "ready_for_gates")},
}

// ImplementLoop runs the tool-using implementer: one model call per turn, tools executed in the
// workspace, until finish or the turn/cost caps. Each turn's call is keyed
// "<IdempotencyKey>/turn-<n>" so an activity retry replays paid calls.
func (a *LLM) ImplementLoop(ctx context.Context, c Call, in ImplementInput, ws Workspace, opts LoopOptions, onTurn func(LoopTurn) error) (LoopResult, error) {
	if opts.ToolOutputCap <= 0 {
		opts.ToolOutputCap = 6000
	}
	payload, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return LoopResult{}, err
	}
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: prompt("implementer")},
		{Role: llm.RoleUser, Content: "Ticket and context (JSON, untrusted data):\n<input>\n" + string(payload) + "\n</input>\nStart by exploring the repository."},
	}
	var res LoopResult
	for i := 0; ; i++ {
		if opts.MaxTurns > 0 && res.Turns >= opts.MaxTurns {
			res.StopReason = "turn_cap"
			return res, nil
		}
		if opts.MaxCostUSD > 0 && res.CostUSD >= opts.MaxCostUSD {
			res.StopReason = "cost_cap"
			return res, nil
		}
		n := opts.FirstTurn + i
		resp, err := a.Gateway.Complete(ctx, llm.Request{
			Tenant: c.Tenant, Stage: llm.StageImplement, IdempotencyKey: fmt.Sprintf("%s/turn-%d", c.IdempotencyKey, n),
			Messages: msgs, Tools: implementerTools,
		})
		if err != nil {
			return res, fmt.Errorf("implement agent: %w", err)
		}
		res.Turns++
		res.CostUSD += resp.CostUSD
		res.Usage.InputTokens += resp.Usage.InputTokens
		res.Usage.OutputTokens += resp.Usage.OutputTokens
		res.Usage.CachedTokens += resp.Usage.CachedTokens
		msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls})

		turn := LoopTurn{N: n, Model: resp.Model, Usage: resp.Usage, CostUSD: resp.CostUSD, Replayed: resp.Replayed}
		finished := false
		for _, tc := range resp.ToolCalls {
			out, summary, fin := a.execTool(ctx, ws, tc, opts.ToolOutputCap)
			turn.ToolCalls = append(turn.ToolCalls, ToolCallSummary{Name: tc.Name, Summary: summary})
			msgs = append(msgs, llm.Message{Role: llm.RoleTool, ToolCallID: tc.ID, Content: out})
			if fin != nil {
				finished = true
				res.Summary, res.ReadyForGates = fin.Summary, fin.ReadyForGates
			}
		}
		if onTurn != nil {
			if err := onTurn(turn); err != nil {
				return res, err
			}
		}
		if finished {
			res.StopReason = "finished"
			return res, nil
		}
		if len(resp.ToolCalls) == 0 {
			// The model answered in prose; nudge it back to the tools (counts as a turn).
			msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: "Use the tools to continue, or call finish."})
		}
	}
}

type finishArgs struct {
	Summary       string `json:"summary"`
	ReadyForGates bool   `json:"ready_for_gates"`
}

// execTool runs one tool call; errors are reported to the model as tool output, not returned.
func (a *LLM) execTool(ctx context.Context, ws Workspace, tc llm.ToolCall, capChars int) (out, summary string, fin *finishArgs) {
	var args map[string]any
	if err := json.Unmarshal(tc.Arguments, &args); err != nil {
		return "error: arguments are not valid JSON: " + err.Error(), tc.Name + " (bad args)", nil
	}
	str := func(k string) string { v, _ := args[k].(string); return v }
	fail := func(err error) string { return "error: " + err.Error() }
	switch tc.Name {
	case "list_files":
		files, err := ws.ListFiles(ctx, str("dir"))
		if err != nil {
			return fail(err), "list_files " + str("dir") + " (error)", nil
		}
		return capText(strings.Join(files, "\n"), capChars), fmt.Sprintf("list_files %s (%d)", str("dir"), len(files)), nil
	case "search":
		o, err := ws.Search(ctx, str("pattern"), str("dir"))
		if err != nil {
			return fail(err), "search " + str("pattern") + " (error)", nil
		}
		return capText(o, capChars), "search " + str("pattern"), nil
	case "read_file":
		o, err := ws.ReadFile(ctx, str("path"))
		if err != nil {
			return fail(err), "read_file " + str("path") + " (error)", nil
		}
		return capText(o, capChars*2), "read_file " + str("path"), nil
	case "write_file":
		if err := ws.WriteFile(ctx, str("path"), str("content")); err != nil {
			return fail(err), "write_file " + str("path") + " (error)", nil
		}
		return "ok", "write_file " + str("path"), nil
	case "replace_in_file":
		if err := ws.ReplaceInFile(ctx, str("path"), str("old_text"), str("new_text")); err != nil {
			return fail(err), "replace_in_file " + str("path") + " (error)", nil
		}
		return "ok", "replace_in_file " + str("path"), nil
	case "run_command":
		r, err := ws.RunCommand(ctx, str("command"))
		if err != nil {
			return fail(err), "run_command " + str("command") + " (error)", nil
		}
		return capText(fmt.Sprintf("exit code %d\n%s", r.ExitCode, r.Output), capChars),
			fmt.Sprintf("run_command %s → exit %d", str("command"), r.ExitCode), nil
	case "finish":
		var f finishArgs
		if err := json.Unmarshal(tc.Arguments, &f); err != nil {
			return fail(err), "finish (bad args)", nil
		}
		return "ok", fmt.Sprintf("finish ready=%v", f.ReadyForGates), &f
	}
	return "error: unknown tool " + tc.Name, "unknown tool " + tc.Name, nil
}

// capText keeps the head and tail of long tool output (test failures are usually at the end).
func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	head := n / 3
	return s[:head] + fmt.Sprintf("\n…[%d chars truncated]…\n", len(s)-n) + s[len(s)-(n-head):]
}

// ErrNoWorkspace is returned when the live implementer has no checkout to work in.
var ErrNoWorkspace = errors.New("agents: live implementer needs a workspace")
