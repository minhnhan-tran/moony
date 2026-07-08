package agent

// SetWorkingDir re-roots a live agent at a new directory — the engine behind the
// /cd command. It is deliberately in its own file so its dir-scoped imports
// (manage/skills) stay local.

import (
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
)

// SetWorkingDir re-roots this live agent at dir so the session operates on the
// right project: it points the tools registry (file ops, bash, grep/glob, the
// path sandbox) at dir IN PLACE — keeping every registered tool, including MCP —
// updates the config, and RELOADS the dir-scoped permissions, sub-agents, and
// skills, so @-mentions, the task/skill tools, and approval rules all resolve
// against the new project instead of the launch dir.
//
// Scope (v1): MCP servers, language servers, and formatters keep the root they
// were started with until the session restarts — re-spawning those external
// processes on a dir switch is out of scope here. The caller must ensure no turn
// is in flight (it mutates shared state without locking; /cd is idle-only) and
// pass an already-resolved, existing absolute directory.
func (a *Agent) SetWorkingDir(dir string) {
	a.cfg.WorkingDir = dir
	if a.tools != nil {
		a.tools.SetRoot(dir)
	}
	a.AttachPermissions(permissions.Load(dir))
	mgmt := manage.Load(dir)
	a.AttachSubAgents(manage.Filter(subagents.Load(dir), func(p subagents.Persona) string { return p.Name }, mgmt.AgentEnabled))
	a.AttachSkills(manage.Filter(skills.Load(dir), func(s skills.Skill) string { return s.Name }, mgmt.SkillEnabled))
}
