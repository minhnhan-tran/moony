// Package app wires the shared agent stack — the on-disk session store plus a
// per-project agent factory — into a ready-to-serve web.Server. The terminal
// builds its relay-served app through here (see MN_RELAY in main.go) so the
// agent/tool/session wiring stays identical to the rest of the app.
package app

import (
	"context"
	"os"

	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/config"
	"github.com/pysty1995/moony/internal/domains"
	"github.com/pysty1995/moony/internal/format"
	"github.com/pysty1995/moony/internal/llm"
	"github.com/pysty1995/moony/internal/lsp"
	"github.com/pysty1995/moony/internal/manage"
	"github.com/pysty1995/moony/internal/permissions"
	"github.com/pysty1995/moony/internal/quickactions"
	"github.com/pysty1995/moony/internal/session"
	"github.com/pysty1995/moony/internal/skills"
	"github.com/pysty1995/moony/internal/subagents"
	"github.com/pysty1995/moony/internal/tools"
	"github.com/pysty1995/moony/internal/web"
	"github.com/pysty1995/moony/internal/worker"
)

// BuildServer opens the default session store and returns a multi-project
// web.Server whose sessions each run their own agent rooted at the session's
// working dir and persist back to disk. The returned store is the same one the
// server uses (handy for callers that want to inspect it).
func BuildServer(cfg *config.Config) (*web.Server, *session.Store, error) {
	// First-run: materialize the default global permission/domain rules so the
	// relay-served sessions and Tools/Domains menus start from the same defaults
	// as the terminal. Idempotent; never overwrites existing files.
	permissions.EnsureGlobalDefaults()
	domains.EnsureDefaults()
	// Same first-run treatment for the composer's quick actions, and the one place
	// a newly shipped built-in reaches an existing installation. Without this call
	// the built-ins were unreachable: nothing else creates the file, so the menu
	// stayed empty until someone used the panel's reset button.
	quickactions.EnsureGlobalDefaults()

	store, err := session.NewStore()
	if err != nil {
		return nil, nil, err
	}

	// The llm client is safe for concurrent use, so it's shared across sessions.
	client := llm.NewClient(cfg)

	factory := func(workingDir string, sess *session.Session) *agent.Agent {
		c := *cfg // per-project copy so the working dir (and thus tools/system prompt) is correct
		c.WorkingDir = workingDir
		// The relay serves sessions for arbitrary project dirs, which the process's own
		// startup migration never touched — without this, a project opened through the
		// web UI would keep reading its legacy config dir forever. Idempotent, so the
		// repeat cost for an already-migrated project is a single stat.
		config.MigrateProjectDirVerbose(workingDir, os.Stderr)
		registry := tools.Default(workingDir, c.MaxToolOutputBytes)
		todos := tools.NewTodoStore()
		registry.RegisterTodo(todos)
		plan := tools.NewPlanStore()
		registry.RegisterPlan(plan)
		mem := tools.NewMemorySink()
		registry.RegisterRemember(mem)
		userMem := tools.NewMemorySink()
		registry.RegisterRememberUser(userMem)
		ag := agent.New(&c, client, registry, store, sess)
		ag.SetInteractive(true)    // a browser user answers approval/question prompts
		ag.SetQuestionGroups(true) // the web UI renders ask_user questions[] as tabs
		ag.AttachTodoStore(todos)
		ag.AttachPlanStore(plan)
		ag.AttachMemory(mem)
		ag.AttachUserMemory(userMem)
		ag.AttachPermissions(permissions.Load(workingDir))
		mgmt := manage.Load(workingDir)
		_, _ = subagents.SeedGlobalDefaultAgents() // editable build/plan in /agents (best-effort, idempotent)
		ag.AttachSubAgents(manage.Filter(subagents.Load(workingDir), func(p subagents.Persona) string { return p.Name }, mgmt.AgentEnabled))
		ag.AttachSkills(manage.Filter(skills.Load(workingDir), func(s skills.Skill) string { return s.Name }, mgmt.SkillEnabled))

		// Code formatters run on changed files after each successful write
		// (before LSP diagnostics). Invalid config is skipped silently here so a
		// bad project file never breaks a served session.
		if fmtCfgs, err := format.LoadConfigs(workingDir); err == nil {
			if fmtMgr := format.New(workingDir, fmtCfgs); !fmtMgr.Empty() {
				ag.AttachFormatter(fmtMgr)
			}
		}

		// Language servers: diagnostics are appended after each successful write,
		// just like the terminal — so relay/web-served sessions get them too. One
		// manager per session rooted at its working dir, filtered by the manage
		// enable/disable state. Invalid config is skipped silently.
		if lspCfgs, err := lsp.LoadConfigs(workingDir); err == nil {
			lspCfgs = manage.Filter(lspCfgs, func(c lsp.Config) string { return c.Name }, mgmt.LSPEnabled)
			if lspMgr := lsp.Start(context.Background(), workingDir, lspCfgs, nil); !lspMgr.Empty() {
				ag.AttachLSP(lspMgr)
			}
		}

		// Worker agents (Claude Code / Optimus) behind the `delegate` tool.
		if workerCfgs, err := worker.Load(workingDir); err == nil {
			wm := worker.New(workingDir, workerCfgs)
			wm.Detect()            // auto-add installed engines
			go wm.ProbeReadiness() // background `--version` probe for /workers
			ag.AttachWorkers(wm)
		}
		return ag
	}

	return web.NewServer(store, cfg.WorkingDir, cfg, factory), store, nil
}
