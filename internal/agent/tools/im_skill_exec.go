package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/agent/skills"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

const ToolExecuteApprovedSkill = "execute_approved_skill"

type ApprovedSkillExecutor interface {
	ExecApprovedSkill(context.Context, string, string, string, time.Duration, map[string]string) (*sandbox.ExecuteResult, error)
}

type imSkillExecTool struct {
	manager   *skills.Manager
	executor  ApprovedSkillExecutor
	authorize func(context.Context, string) error
}

func NewIMSkillExecTool(manager *skills.Manager, executor ApprovedSkillExecutor, authorize func(context.Context, string) error) types.Tool {
	return &imSkillExecTool{manager: manager, executor: executor, authorize: authorize}
}
func (*imSkillExecTool) Name() string { return ToolExecuteApprovedSkill }
func (*imSkillExecTool) Description() string {
	return "Run one listed approved Skill script in this session's isolated sandbox. Read skill://<name>/SKILL.md first. Only scripts from that Skill package may run; arguments are passed as separate values. Network and stored credentials are unavailable."
}
func (*imSkillExecTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"skill_name":{"type":"string"},"script":{"type":"string","description":"Relative path to a listed .py, .sh, .js, .mjs or .cjs script"},"args":{"type":"array","items":{"type":"string"}}},"required":["skill_name","script"]}`)
}
func (t *imSkillExecTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	fail := func(err error) (*types.ToolResult, error) {
		return &types.ToolResult{Success: false, Error: err.Error()}, nil
	}
	var input struct {
		SkillName string   `json:"skill_name"`
		Script    string   `json:"script"`
		Args      []string `json:"args"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return fail(err)
	}
	if t.manager == nil || t.executor == nil || t.authorize == nil {
		return fail(errors.New("approved Skill execution unavailable"))
	}
	if err := t.authorize(ctx, input.SkillName); err != nil {
		return fail(err)
	}
	sid, ok := types.SessionIDFromContext(ctx)
	if !ok || sid == "" {
		return fail(errors.New("session required"))
	}
	script := input.Script
	if script == "" || len(script) > 1024 || path.IsAbs(script) || path.Clean(script) != script || script == ".." || strings.HasPrefix(script, "../") || strings.ContainsAny(script, "\\\x00") {
		return fail(errors.New("invalid Skill script path"))
	}
	if len(input.Args) > 64 {
		return fail(errors.New("too many Skill arguments"))
	}
	files, err := t.manager.ListSkillFiles(ctx, input.SkillName)
	if err != nil {
		return fail(err)
	}
	found := false
	for _, file := range files {
		if file == script {
			found = true
			break
		}
	}
	if !found {
		return fail(errors.New("script is not part of the approved Skill"))
	}
	var interpreter string
	switch strings.ToLower(path.Ext(script)) {
	case ".py":
		interpreter = "python3"
	case ".sh":
		interpreter = "bash"
	case ".js", ".mjs", ".cjs":
		interpreter = "node"
	default:
		return fail(errors.New("unsupported Skill script type"))
	}
	command := interpreter + ` "$WEKNORA_SKILL_DIR"/` + sandbox.ShellQuote(script)
	for _, arg := range input.Args {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return fail(errors.New("invalid Skill argument"))
		}
		command += " " + sandbox.ShellQuote(arg)
	}
	command, env, err := t.manager.PrepareShellEnvironment(ctx, sid, input.SkillName, command, nil)
	if err != nil {
		return fail(err)
	}
	result, err := t.executor.ExecApprovedSkill(ctx, sid, command, sandbox.SessionWorkspaceRoot, sandbox.IMMaxCommandTime, env)
	if err != nil {
		return fail(err)
	}
	if result == nil {
		return fail(errors.New("sandbox returned no result"))
	}
	output := result.Stdout
	if result.Stderr != "" {
		output += "\n" + result.Stderr
	}
	return &types.ToolResult{Success: result.IsSuccess(), Output: output, Error: result.Error, Data: map[string]interface{}{"exit_code": result.ExitCode, "skill_name": input.SkillName}}, nil
}
