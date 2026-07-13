package tools

// RegisterDefaults registers the standard tool set on the registry.
// maxOutputBytes caps bash output fed back to the model.
func (r *Registry) RegisterDefaults(maxOutputBytes int) {
	r.Register(&readTool{r: r})
	r.Register(&writeTool{r: r})
	r.Register(&editTool{r: r})
	r.Register(&multiEditTool{r: r})
	r.Register(&applyPatchTool{r: r})
	r.Register(&lsTool{r: r})
	r.Register(&globTool{r: r})
	r.Register(&grepTool{r: r})
	r.Register(&searchCodeTool{r: r})
	r.Register(&findSymbolTool{r: r})
	r.Register(&readSymbolTool{r: r})
	r.Register(&findReferencesTool{r: r})
	r.Register(&bashTool{r: r, maxOutputBytes: maxOutputBytes})
	r.Register(&bashOutputTool{r: r, max: maxOutputBytes})
	r.Register(&bashKillTool{r: r})
	wf := &webFetchTool{r: r}
	r.Register(wf)
	r.Register(&batchWebFetchTool{r: r, wf: wf})
	r.Register(&webSearchTool{})
	r.Register(&browserTool{r: r})
	r.Register(&gitStatusTool{r: r, max: maxOutputBytes})
	r.Register(&gitDiffTool{r: r, max: maxOutputBytes})
	r.Register(&gitLogTool{r: r, max: maxOutputBytes})
	r.Register(&gitCommitTool{r: r, max: maxOutputBytes})
	r.Register(&gitBranchTool{r: r})
	r.Register(&gitWorktreeTool{r: r, max: maxOutputBytes})
	r.Register(&prCreateTool{r: r})
	r.Register(&prListTool{r: r})
	r.Register(&prCommentsTool{r: r})
	r.Register(&prChecksTool{r: r})
	r.Register(&lintTool{r: r, max: maxOutputBytes})
	r.Register(&testRunTool{r: r, max: maxOutputBytes})
	r.Register(&askUserTool{})
	r.Register(&exitPlanTool{})
}

// RegisterAllowed registers only the standard tools whose name is in `allowed`.// Useful for sub-agents that should run with a restricted toolset (e.g. a
// read-only "explore" persona).
func RegisterAllowed(r *Registry, maxOutputBytes int, allowed map[string]bool) {
	all := []Tool{
		&readTool{r: r},
		&writeTool{r: r},
		&editTool{r: r},
		&multiEditTool{r: r},
		&applyPatchTool{r: r},
		&lsTool{r: r},
		&globTool{r: r},
		&grepTool{r: r},
		&searchCodeTool{r: r},
		&findSymbolTool{r: r},
		&bashTool{r: r, maxOutputBytes: maxOutputBytes},
		&bashOutputTool{r: r, max: maxOutputBytes},
		&bashKillTool{r: r},
		&webFetchTool{r: r},
		&batchWebFetchTool{r: r},
		&webSearchTool{},
		&browserTool{r: r},
		&gitStatusTool{r: r, max: maxOutputBytes},
		&gitDiffTool{r: r, max: maxOutputBytes},
		&gitLogTool{r: r, max: maxOutputBytes},
		&gitCommitTool{r: r, max: maxOutputBytes},
		&gitBranchTool{r: r},
		&gitWorktreeTool{r: r, max: maxOutputBytes},
		&lintTool{r: r, max: maxOutputBytes},
		&testRunTool{r: r, max: maxOutputBytes},
	}
	for _, t := range all {
		if allowed[t.Name()] {
			r.Register(t)
		}
	}
}

// RegisterTodo adds the todo_write tool, backed by the given store. Call this
// when the embedding application can render or otherwise observe the list.
func (r *Registry) RegisterTodo(store *TodoStore) {
	r.Register(&todoWriteTool{store: store})
}

// RegisterPlan adds the plan_write tool, backed by the given store. Call this
// when the embedding application can render and approve the plan artifact.
func (r *Registry) RegisterPlan(store *PlanStore) {
	r.Register(&planWriteTool{store: store})
}

// Default builds a registry rooted at workingDir with the standard tools.
func Default(workingDir string, maxOutputBytes int) *Registry {
	r := NewRegistry(workingDir)
	r.RegisterDefaults(maxOutputBytes)
	return r
}
