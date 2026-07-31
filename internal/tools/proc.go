package tools

import (
	"os/exec"

	"github.com/pysty1995/moony/internal/procutil"
)

// configureProcessGroup makes cmd killable as a whole process TREE, not just the
// immediate child, and must be called before Start/Run/CombinedOutput.
//
// Thin alias over procutil.KillTree so the tools package and the rest of the
// codebase (verify, hooks) share ONE implementation of this — it's subtle enough
// (Setpgid + a group-signalling Cancel + a WaitDelay backstop) that a second copy
// is a liability. See procutil.KillTree for why the default cancel isn't enough.
func configureProcessGroup(cmd *exec.Cmd) { procutil.KillTree(cmd) }
