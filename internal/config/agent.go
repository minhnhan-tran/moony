package config

import "strings"

// Agent is a named behavior profile (à la opencode/Optimus "build" and "plan"
// agents): it carries a per-tool permission map that gates what the model may do
// this session. Two built-ins ship out of the box — "build" (full access, the
// default) and "plan" (read-only). Switching agents swaps the active profile.
//
// The Permission map is opencode's model: each tool name maps to one of
// PermAllow / PermAsk / PermDeny.
//
//   - PermDeny  — the tool is hidden from the model and any call is rejected.
//   - PermAsk   — the tool always prompts for confirmation before running
//     (overriding the approval policy, even "auto").
//   - PermAllow — the profile imposes no extra restriction; the call still goes
//     through the normal approval policy + permission rules + hard gates.
//
// A tool absent from the map defaults to PermAllow (unrestricted by the
// profile). This layers on top of the existing ApprovalPolicy and the persisted
// permissions rules rather than replacing them.
type Agent struct {
	Name       string
	Permission map[string]string
}

// Permission values for Agent.Permission.
const (
	PermAllow = "allow"
	PermAsk   = "ask"
	PermDeny  = "deny"
)

// Agent names for the two built-ins.
const (
	AgentBuild = "build"
	AgentPlan  = "plan"
)

// Rule returns the profile's permission for a tool: PermAllow, PermAsk, or
// PermDeny. A tool with no explicit entry defaults to PermAllow (the profile
// adds no restriction).
func (a *Agent) Rule(tool string) string {
	if a == nil || a.Permission == nil {
		return PermAllow
	}
	switch a.Permission[tool] {
	case PermDeny:
		return PermDeny
	case PermAsk:
		return PermAsk
	default:
		return PermAllow
	}
}

// ReadOnly reports whether the profile denies every mutating built-in — i.e. it
// behaves like a pure read-only "plan" agent. Used as the compatibility bridge
// for the legacy plan-mode flag and the plan-artifact gate.
func (a *Agent) ReadOnly() bool {
	if a == nil {
		return false
	}
	for _, t := range mutatingBuiltins {
		if a.Permission[t] != PermDeny {
			return false
		}
	}
	return true
}

// mutatingBuiltins is the set of built-in tools that change files, run shell
// commands, spawn sub-agents, dispatch to external workers, or write shared
// memory — the ones a read-only profile must deny. (bash is handled separately:
// a plan profile asks for it rather than denying it.)
var mutatingBuiltins = []string{"write_file", "edit_file", "multi_edit", "apply_patch", "task", "delegate", "remember"}

// BuildAgent is the default full-access profile: no per-tool restrictions, so
// every tool falls through to the normal approval policy and permission rules.
func BuildAgent() *Agent { return &Agent{Name: AgentBuild} }

// PlanAgent is the read-only profile, mirroring opencode's "plan": anything that
// mutates files, the repo, or shared memory — write/edit/patch, task, the
// out-of-process `delegate` worker, and `remember` (which appends to AGENTS.md)
// — is denied outright, and bash must ask first (so the model can still run
// read-only inspection commands with the user's OK).
func PlanAgent() *Agent {
	return &Agent{
		Name: AgentPlan,
		Permission: map[string]string{
			"write_file":  PermDeny,
			"edit_file":   PermDeny,
			"multi_edit":  PermDeny,
			"apply_patch": PermDeny,
			"task":        PermDeny,
			"delegate":    PermDeny,
			"remember":    PermDeny,
			"bash":        PermAsk,
		},
	}
}

// BuiltinAgent returns the built-in profile for a name ("build"/"plan", any
// case), or nil when the name isn't a built-in.
func BuiltinAgent(name string) *Agent {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case AgentBuild:
		return BuildAgent()
	case AgentPlan:
		return PlanAgent()
	}
	return nil
}
