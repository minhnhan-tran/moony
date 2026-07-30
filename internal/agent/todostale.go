package agent

// Stale-todo guard. todo_write is purely declarative: its Execute calls
// store.Set(todos), which replaces the list verbatim and never infers progress
// from anything the agent does afterwards. So a turn that marks a task
// in_progress, does the work, and then ends without sending the closing
// todo_write leaves that task displayed as unfinished forever — the user's live
// progress view silently disagrees with reality.
//
// Nothing else catches this: the plan/verify/self-review loops all look at files
// and diffs, not at the todo list. This check closes that gap by looking at the
// list once the turn is over and telling the agent when it left a task open, so
// the omission surfaces immediately (and in the transcript the user can see)
// instead of going unnoticed until someone asks about it.

import (
	"fmt"
	"strings"

	"github.com/pysty1995/moony/internal/tools"
)

// staleTodoLimit caps how many task titles the notice names, so a long list
// doesn't flood the transcript.
const staleTodoLimit = 3

// maybeWarnStaleTodos emits a notice when the turn ends with tasks still marked
// in_progress. It's advisory only: it never edits the list, because only the
// agent knows whether a task actually finished — auto-completing it would be a
// lie in exactly the case where the work was genuinely abandoned or blocked.
//
// Gated on the turn having changed files: a read-only turn that's mid-way
// through a long task legitimately leaves it in_progress, and nagging there
// would train the agent to ignore the notice.
func (a *Agent) maybeWarnStaleTodos(emit func(Event)) {
	if a == nil || a.todos == nil || emit == nil {
		return
	}
	if a.snapshots == nil || !a.snapshots.mutatedThisTurn() {
		return
	}
	titles, total := staleTodoTitles(a.Todos())
	if total == 0 {
		return
	}
	emit(Info{Text: fmt.Sprintf(
		"Todo list still shows %s in progress after this turn: %s. "+
			"If it's done, close it out with `todo_write` — the list is declarative and never completes a task on its own.",
		pluralTasks(total), strings.Join(titles, ", "))})
}

// staleTodoTitles returns display titles for the tasks left in_progress — capped
// at staleTodoLimit, with a "+N more" marker appended when truncated — alongside
// the TOTAL number of open tasks. The count is returned separately because the
// caller needs the real total for its message: using len(titles) would report the
// truncated length (and count the "+N more" marker as a task).
func staleTodoTitles(items []tools.Todo) (titles []string, total int) {
	for _, t := range items {
		if t.Status != tools.TodoInProgress {
			continue
		}
		title := strings.TrimSpace(t.Content)
		if title == "" {
			continue
		}
		total++
		if len(titles) < staleTodoLimit {
			titles = append(titles, "**"+title+"**")
		}
	}
	if extra := total - len(titles); extra > 0 {
		titles = append(titles, fmt.Sprintf("+%d more", extra)) // a marker, not a title — left unbolded
	}
	return titles, total
}

func pluralTasks(n int) string {
	if n == 1 {
		return "a task"
	}
	return fmt.Sprintf("%d tasks", n)
}
