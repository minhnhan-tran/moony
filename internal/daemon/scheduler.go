package daemon

import (
	"context"
	"os"
	"time"

	"github.com/pysty1995/moony/internal/schedule"
)

// schedulerTick is how often the daemon checks for due schedules. Schedules fire
// at minute-or-coarser granularity, so a sub-minute poll keeps drift small
// without busy-waiting.
const schedulerTick = 30 * time.Second

// runScheduler drives time-based schedules while the daemon runs. It resolves
// this executable (so each due schedule re-invokes `moony -p ... --json`
// as a normal, web-browsable headless session), then evaluates due schedules at
// startup and every schedulerTick until ctx is canceled. All persistence and
// run accounting lives in the schedule package; this is purely the timer half.
func runScheduler(ctx context.Context, logf func(string, ...any)) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	exe, err := os.Executable()
	if err != nil {
		logf("scheduler: cannot resolve executable, scheduling disabled: %v", err)
		return
	}
	st, err := schedule.Open()
	if err != nil {
		logf("scheduler: cannot open schedule store, scheduling disabled: %v", err)
		return
	}

	// Fire anything already due before the first tick.
	schedule.RunDue(ctx, st, exe, logf)

	t := time.NewTicker(schedulerTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			schedule.RunDue(ctx, st, exe, logf)
		}
	}
}
