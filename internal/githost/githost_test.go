package githost

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	cases := []struct {
		name        string
		remote      string
		ghBase      string
		glBase      string
		wantKind    HostKind
		wantOwner   string
		wantName    string
		wantProject string
		wantAPIBase string
		wantErr     bool
	}{
		{name: "github https", remote: "https://github.com/acme/widget.git", wantKind: GitHub, wantOwner: "acme", wantName: "widget", wantProject: "acme/widget", wantAPIBase: "https://api.github.com"},
		{name: "github https no .git", remote: "https://github.com/acme/widget", wantKind: GitHub, wantOwner: "acme", wantName: "widget", wantProject: "acme/widget", wantAPIBase: "https://api.github.com"},
		{name: "github ssh", remote: "git@github.com:acme/widget.git", wantKind: GitHub, wantOwner: "acme", wantName: "widget", wantProject: "acme/widget", wantAPIBase: "https://api.github.com"},
		{name: "github ssh:// form", remote: "ssh://git@github.com/acme/widget.git", wantKind: GitHub, wantOwner: "acme", wantName: "widget", wantProject: "acme/widget", wantAPIBase: "https://api.github.com"},
		{name: "gitlab https", remote: "https://gitlab.com/acme/widget.git", wantKind: GitLab, wantOwner: "acme", wantName: "widget", wantProject: "acme/widget", wantAPIBase: "https://gitlab.com/api/v4"},
		{name: "gitlab ssh", remote: "git@gitlab.com:acme/widget.git", wantKind: GitLab, wantOwner: "acme", wantName: "widget", wantProject: "acme/widget", wantAPIBase: "https://gitlab.com/api/v4"},
		{name: "gitlab nested group", remote: "https://gitlab.com/acme/platform/widget.git", wantKind: GitLab, wantOwner: "acme", wantName: "widget", wantProject: "acme/platform/widget", wantAPIBase: "https://gitlab.com/api/v4"},
		{name: "github enterprise via override", remote: "https://git.acme.internal/acme/widget.git", ghBase: "https://git.acme.internal/api/v3", wantKind: GitHub, wantOwner: "acme", wantName: "widget", wantProject: "acme/widget", wantAPIBase: "https://git.acme.internal/api/v3"},
		{name: "self-hosted gitlab via override", remote: "git@gitlab.acme.internal:acme/platform/widget.git", glBase: "https://gitlab.acme.internal/api/v4", wantKind: GitLab, wantOwner: "acme", wantName: "widget", wantProject: "acme/platform/widget", wantAPIBase: "https://gitlab.acme.internal/api/v4"},
		{name: "unsupported host, no override", remote: "https://bitbucket.org/acme/widget.git", wantErr: true},
		{name: "github path with wrong segment count", remote: "https://github.com/acme/widget/extra.git", wantErr: true},
		{name: "empty remote", remote: "", wantErr: true},
		{name: "garbage remote", remote: "not a url", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old1, old2 := envGithubBaseURL, envGitlabBaseURL
			envGithubBaseURL = func() string { return tc.ghBase }
			envGitlabBaseURL = func() string { return tc.glBase }
			defer func() { envGithubBaseURL, envGitlabBaseURL = old1, old2 }()

			repo, err := Detect(tc.remote)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got repo=%+v", repo)
				}
				return
			}
			if err != nil {
				t.Fatalf("Detect(%q): %v", tc.remote, err)
			}
			if repo.Kind != tc.wantKind || repo.Owner != tc.wantOwner || repo.Name != tc.wantName ||
				repo.ProjectPath != tc.wantProject || repo.APIBase != tc.wantAPIBase {
				t.Errorf("Detect(%q) = %+v, want kind=%s owner=%s name=%s project=%s apiBase=%s",
					tc.remote, repo, tc.wantKind, tc.wantOwner, tc.wantName, tc.wantProject, tc.wantAPIBase)
			}
		})
	}
}

func writeNetrc(t *testing.T, content string) {
	t.Helper()
	path := t.TempDir() + "/netrc"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NETRC", path)
}

func TestTokenPrecedence(t *testing.T) {
	repo := &Repo{Kind: GitHub, RemoteHost: "github.com"}

	t.Run("primary env wins", func(t *testing.T) {
		t.Setenv("MOONY_GITHUB_TOKEN", "primary-tok")
		t.Setenv("GITHUB_TOKEN", "fallback-tok")
		tok, err := Token(context.Background(), repo)
		if err != nil || tok != "primary-tok" {
			t.Fatalf("Token = %q, %v; want primary-tok", tok, err)
		}
	})

	t.Run("fallback env used when primary unset", func(t *testing.T) {
		t.Setenv("MOONY_GITHUB_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "fallback-tok")
		tok, err := Token(context.Background(), repo)
		if err != nil || tok != "fallback-tok" {
			t.Fatalf("Token = %q, %v; want fallback-tok", tok, err)
		}
	})

	t.Run("CLI used when no env vars", func(t *testing.T) {
		t.Setenv("MOONY_GITHUB_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		old := runCLIToken
		runCLIToken = func(ctx context.Context, cli string, args ...string) (string, error) {
			if cli != "gh" {
				t.Errorf("expected gh CLI, got %q", cli)
			}
			return "cli-tok\n", nil
		}
		defer func() { runCLIToken = old }()
		tok, err := Token(context.Background(), repo)
		if err != nil || tok != "cli-tok" {
			t.Fatalf("Token = %q, %v; want cli-tok", tok, err)
		}
	})

	t.Run("netrc used when no env vars and CLI fails", func(t *testing.T) {
		t.Setenv("MOONY_GITHUB_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		old := runCLIToken
		runCLIToken = func(ctx context.Context, cli string, args ...string) (string, error) {
			return "", errors.New("not installed")
		}
		defer func() { runCLIToken = old }()
		writeNetrc(t, "machine github.com login x password netrc-tok\n")
		tok, err := Token(context.Background(), repo)
		if err != nil || tok != "netrc-tok" {
			t.Fatalf("Token = %q, %v; want netrc-tok", tok, err)
		}
	})

	t.Run("clear error naming env vars when nothing resolves", func(t *testing.T) {
		t.Setenv("MOONY_GITHUB_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")
		t.Setenv("NETRC", t.TempDir()+"/does-not-exist")
		old := runCLIToken
		runCLIToken = func(ctx context.Context, cli string, args ...string) (string, error) {
			return "", errors.New("not installed")
		}
		defer func() { runCLIToken = old }()
		_, err := Token(context.Background(), repo)
		if err == nil {
			t.Fatal("expected an error")
		}
		for _, want := range []string{"MOONY_GITHUB_TOKEN", "GITHUB_TOKEN", "gh auth login"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q should mention %q", err.Error(), want)
			}
		}
	})
}
