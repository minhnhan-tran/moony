package githost

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/netrc"
)

// envGithubBaseURL/envGitlabBaseURL are the self-hosted API base URL
// overrides. Vars (not direct os.Getenv calls) so tests can stub them.
var (
	envGithubBaseURL = func() string { return os.Getenv("MOONY_GITHUB_BASE_URL") }
	envGitlabBaseURL = func() string { return os.Getenv("MOONY_GITLAB_BASE_URL") }
)

// runCLIToken runs `<cli> auth token` and returns its trimmed stdout. Var so
// tests can stub it without a real gh/glab binary on PATH.
var runCLIToken = func(ctx context.Context, cli string, args ...string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, cli, args...).Output()
	return string(out), err
}

// Token resolves credentials for repo.Kind in order: the primary env var
// (MOONY_GITHUB_TOKEN/MOONY_GITLAB_TOKEN), the CLI-conventional fallback
// (GITHUB_TOKEN/GITLAB_TOKEN), the host's CLI (`gh auth token`/`glab auth
// token`) if installed and logged in, then a ~/.netrc entry for RemoteHost.
func Token(ctx context.Context, repo *Repo) (string, error) {
	switch repo.Kind {
	case GitHub:
		return resolveToken(ctx, repo, "MOONY_GITHUB_TOKEN", "GITHUB_TOKEN", "gh")
	case GitLab:
		return resolveToken(ctx, repo, "MOONY_GITLAB_TOKEN", "GITLAB_TOKEN", "glab")
	default:
		return "", fmt.Errorf("unsupported git host %q", repo.Kind)
	}
}

func resolveToken(ctx context.Context, repo *Repo, primaryEnv, fallbackEnv, cli string) (string, error) {
	if v := strings.TrimSpace(os.Getenv(primaryEnv)); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(os.Getenv(fallbackEnv)); v != "" {
		return v, nil
	}
	if out, err := runCLIToken(ctx, cli, "auth", "token"); err == nil {
		if v := strings.TrimSpace(out); v != "" {
			return v, nil
		}
	}
	if f, err := netrc.Load(); err == nil {
		if b := f.Bearer(repo.RemoteHost); b != "" {
			return strings.TrimPrefix(b, "Bearer "), nil
		}
	}
	return "", fmt.Errorf(
		"no %s token found for %s — set %s (or %s), run `%s auth login`, or add a %s entry to ~/.netrc",
		repo.Kind.Label(), repo.RemoteHost, primaryEnv, fallbackEnv, cli, repo.RemoteHost)
}
