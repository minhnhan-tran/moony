package config

import (
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
