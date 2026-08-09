package config

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// GlobalConfigDir returns the directory holding moony's user-level
// ("global") configuration, e.g. mcp.json, lsp.json, config.json, sessions/.
//
// It always resolves to ~/.config/moony (honoring $XDG_CONFIG_HOME
// when set) on every platform. This is deliberate: the docs and the rest of
// the app refer to ~/.config/moony, but Go's os.UserConfigDir() maps
// to ~/Library/Application Support on macOS, which silently put the global
// config somewhere the docs never mention. Routing every global-config lookup
// through this helper keeps the location consistent across macOS and Linux.
//
// Resolution order:
//  1. $XDG_CONFIG_HOME/moony  (when XDG_CONFIG_HOME is set)
//  2. ~/.config/moony         (via the user's home directory)
//  3. os.UserConfigDir()/moony (last-resort fallback if home is unknown)
func GlobalConfigDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "moony"), nil
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "moony"), nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "moony"), nil
}

// GlobalConfigPath joins elem onto GlobalConfigDir(), e.g.
// GlobalConfigPath("mcp.json") -> ~/.config/moony/mcp.json.
func GlobalConfigPath(elem ...string) (string, error) {
	dir, err := GlobalConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(append([]string{dir}, elem...)...), nil
}

// UserProfileFilename is the global user-profile memory file: durable facts about
// the USER (coding style, preferred output/formats, workflow habits, standing
// requests) that apply to every session across all projects. Kept separate from
// AGENTS.md, which holds project/repo rules and conventions.
const UserProfileFilename = "USER.md"

// UserProfilePath returns the absolute path of the global user-profile memory
// file, <GlobalConfigDir>/USER.md.
func UserProfilePath() (string, error) {
	return GlobalConfigPath(UserProfileFilename)
}

// MigrateLegacyConfigDir relocates the global config dir from the location this
// app used before it pinned GlobalConfigDir to ~/.config/moony — Go's
// os.UserConfigDir(), which on macOS is ~/Library/Application Support/
// moony. Without this, upgrading on macOS would strand a user's saved
// sessions, config.json, mcp.json/lsp.json, schedules, permissions, etc. in the
// old, now-unread location.
//
// It is best-effort and safe to call once on every startup: it only acts when
// the new dir does not exist yet and the legacy dir does, and it is a no-op on
// Linux (where the two paths coincide) and after the first migration.
func MigrateLegacyConfigDir() error {
	newDir, err := GlobalConfigDir()
	if err != nil {
		return err
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return nil // can't locate the legacy dir — nothing to migrate
	}
	return migrateConfigDir(filepath.Join(base, "moony"), newDir)
}

// MigrateRenamedConfigDir relocates the global config dir from its pre-rename
// name (sqe-code-agent) to the current one (moony) — same
// $XDG_CONFIG_HOME/~/.config resolution as GlobalConfigDir, just the old
// product name. Best-effort and safe to call once on every startup: a no-op
// once migrated, and it never clobbers an existing new-named dir.
func MigrateRenamedConfigDir() error {
	newDir, err := GlobalConfigDir()
	if err != nil {
		return err
	}
	var legacy string
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		legacy = filepath.Join(xdg, "sqe-code-agent")
	} else if home, err := os.UserHomeDir(); err == nil && home != "" {
		legacy = filepath.Join(home, ".config", "sqe-code-agent")
	} else {
		return nil // can't locate the legacy dir — nothing to migrate
	}
	return migrateConfigDir(legacy, newDir)
}

// migrateConfigDir relocates legacy -> dst when dst is absent and legacy is an
// existing, different directory. It tries an atomic rename first, then falls
// back to a recursive copy (leaving legacy intact) for the rare cross-device
// case. It never clobbers an existing dst.
func migrateConfigDir(legacy, dst string) error {
	if legacy == "" || legacy == dst {
		return nil
	}
	if _, err := os.Stat(dst); err == nil {
		return nil // new location already exists — don't clobber it
	}
	info, err := os.Stat(legacy)
	if err != nil || !info.IsDir() {
		return nil // nothing to migrate
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	if err := os.Rename(legacy, dst); err == nil {
		return nil
	}
	// Cross-device (EXDEV) or other rename failure: copy the tree and leave the
	// legacy dir in place as a backup rather than risk losing data.
	return copyDir(legacy, dst)
}

// copyDir recursively copies the src tree to dst, preserving file modes (so
// e.g. schedules.json keeps its 0600).
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		// Recreate symlinks as symlinks. os.Open would dereference them (silently
		// turning a link into a copy of its target) and a BROKEN link would fail the
		// whole migration.
		if d.Type()&fs.ModeSymlink != 0 {
			link, lerr := os.Readlink(path)
			if lerr != nil {
				return lerr
			}
			if merr := os.MkdirAll(filepath.Dir(target), 0o700); merr != nil {
				return merr
			}
			_ = os.Remove(target) // a leftover from an earlier partial attempt
			return os.Symlink(link, target)
		}
		if d.IsDir() {
			mode := fs.FileMode(0o700)
			if info, ierr := d.Info(); ierr == nil {
				mode = info.Mode().Perm()
			}
			return os.MkdirAll(target, mode)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// Per-project configuration directory.
//
// ProjectDir is the directory, relative to a project's working dir, holding
// project-scoped configuration and state: permissions.json, mcp.json, lsp.json,
// hooks.json, format.json, manage.json, workers.json, quickactions.json,
// agents/, commands/, rag/ and worktrees/.
//
// LegacyProjectDir is the pre-rename name. It is still READ when ProjectDir is
// absent, so an un-migrated checkout (or one shared with an older build) keeps
// working, and MigrateProjectDir relocates it on startup.
const (
	ProjectDir       = ".moony"
	LegacyProjectDir = ".sqe"
)

// ProjectPath joins elements onto a project's config dir, preferring ProjectDir
// but falling back to LegacyProjectDir when only the legacy dir exists.
//
// Use this for READS. Writes should target ProjectPathNew so new state always
// lands in the current location, even if a legacy dir is still present.
func ProjectPath(workingDir string, elem ...string) string {
	base := filepath.Join(workingDir, ProjectDir)
	if _, err := os.Stat(base); err != nil && os.IsNotExist(err) {
		legacy := filepath.Join(workingDir, LegacyProjectDir)
		if info, lerr := os.Stat(legacy); lerr == nil && info.IsDir() {
			base = legacy
		}
	}
	return filepath.Join(append([]string{base}, elem...)...)
}

// ProjectPathNew joins elements onto a project's config dir, always using the
// current name. Use for writes and for creating new state.
func ProjectPathNew(workingDir string, elem ...string) string {
	return filepath.Join(append([]string{workingDir, ProjectDir}, elem...)...)
}

// MigrateProjectDir renames ./<LegacyProjectDir> to ./<ProjectDir>.
//
// Best-effort and safe to call once per startup: a no-op when the legacy dir is
// absent or the new one already exists.
//
// It deliberately does NOT merge when both exist. Combining two permissions.json
// allow-lists would grant a union the user never approved, so the legacy dir is
// left untouched and the caller is told, rather than guessing.
//
// Returns the number of git worktrees that need repairing (see RepairWorktrees):
// git stores ABSOLUTE paths for worktrees, so moving the directory strands any
// under <LegacyProjectDir>/worktrees until they are repaired.
func MigrateProjectDir(workingDir string) (migrated bool, bothPresent bool, err error) {
	if workingDir == "" {
		return false, false, nil
	}
	legacy := filepath.Join(workingDir, LegacyProjectDir)
	dst := filepath.Join(workingDir, ProjectDir)

	info, lerr := os.Stat(legacy)
	if lerr != nil || !info.IsDir() {
		return false, false, nil // nothing to migrate
	}
	if _, derr := os.Stat(dst); derr == nil {
		// Both exist: refuse to merge, and say so.
		return false, true, nil
	}
	if err := os.Rename(legacy, dst); err != nil {
		// Lost a race with another process that just renamed it: dst now exists and
		// legacy is gone, so the migration already happened. Not an error.
		if _, serr := os.Stat(legacy); os.IsNotExist(serr) {
			return false, false, nil
		}
		// Cross-device or permission trouble: copy instead, leaving legacy intact
		// so nothing is lost.
		//
		// Copy into a unique staging dir and rename it into place, so a concurrent
		// process never observes a half-populated ProjectDir (which ProjectPath would
		// immediately prefer over the complete legacy dir).
		staging, serr := os.MkdirTemp(workingDir, "."+ProjectDir+".migrating-")
		if serr != nil {
			return false, false, serr
		}
		if cerr := copyDir(legacy, staging); cerr != nil {
			_ = os.RemoveAll(staging)
			return false, false, cerr
		}
		if rerr := os.Rename(staging, dst); rerr != nil {
			_ = os.RemoveAll(staging)
			// Another process may have completed the migration meanwhile.
			if _, derr := os.Stat(dst); derr == nil {
				return false, false, nil
			}
			return false, false, rerr
		}
		// The copy succeeded and is now in place; drop the legacy dir so the
		// both-present guard does not trip on every later start.
		_ = os.RemoveAll(legacy)
		return true, false, nil
	}
	return true, false, nil
}

// MigrateProjectDirVerbose migrates workingDir's project config dir and reports what
// happened to w, including repairing git worktrees after a successful move.
//
// Wraps MigrateProjectDir so every entrypoint reports migration identically instead of
// duplicating the message text.
func MigrateProjectDirVerbose(workingDir string, w io.Writer) {
	if workingDir == "" {
		workingDir = "."
	}
	migrated, both, err := MigrateProjectDir(workingDir)
	switch {
	case err != nil:
		fmt.Fprintln(w, "warning: could not migrate project config dir:", err)
	case both:
		fmt.Fprintf(w, "warning: both %s/ and %s/ exist in %s; using %s/ and leaving %s/ untouched. "+
			"Merge anything you still need by hand — combining them automatically could grant "+
			"permission rules you never approved.\n",
			LegacyProjectDir, ProjectDir, workingDir, ProjectDir, LegacyProjectDir)
	case migrated:
		fmt.Fprintf(w, "note: moved %s/ to %s/ (project config).\n", LegacyProjectDir, ProjectDir)
		// git records ABSOLUTE worktree paths, so the move leaves any worktree under the
		// old path registered there and marked "prunable" — a later `git worktree prune`
		// would drop it. Re-point git at the new location.
		if wts := RepairWorktrees(workingDir); len(wts) > 0 {
			fmt.Fprintf(w, "note: repaired %d git worktree(s) after the move.\n", len(wts))
		}
	}
}
