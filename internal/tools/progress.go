package tools

import "context"

// ProgressFn reports a single human-readable progress line from a tool while
// it's running (e.g. semantic_search's first-time workspace reindex), so a
// long-running call isn't indistinguishable from a hang in the UI.
type ProgressFn func(text string)

type progressCtxKey struct{}

// WithProgress stashes a progress emitter in ctx for the running tool call to
// report through via ProgressFrom. Mirrors WithUserAsker/WithChangeReviewer.
func WithProgress(ctx context.Context, fn ProgressFn) context.Context {
	return context.WithValue(ctx, progressCtxKey{}, fn)
}

// ProgressFrom returns the progress emitter stashed by WithProgress, or a
// no-op function when none is set (e.g. in tests) — safe to call unconditionally.
func ProgressFrom(ctx context.Context) ProgressFn {
	if fn, ok := ctx.Value(progressCtxKey{}).(ProgressFn); ok && fn != nil {
		return fn
	}
	return func(string) {}
}
