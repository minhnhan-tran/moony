package awsdeploy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// convergeInputs are the deploy-time settings apply.sh needs on the instance.
type convergeInputs struct {
	Bucket        string
	Key           string
	TunnelHost    string
	SiteAddress   string
	AcmeEmail     string
	WithDownloads string
	EnvParameter  string
}

// convergeScript is the script SSM runs on the instance after a stack update.
//
// It exists because CloudFormation updates an instance's user-data in place - stop,
// modify, start - and cloud-init runs user-data once per instance. Without this, adding
// --domain to a live stack changes the security group and nothing else, and Caddy keeps
// serving plain HTTP while the template claims otherwise.
func convergeScript(in convergeInputs) []string {
	return []string{
		"set -eux",
		"aws s3 cp " + shellQuote("s3://"+in.Bucket+"/"+in.Key) + " /opt/moony/src.tar.gz",
		"rm -rf " + instanceSrcDir,
		"install -d -m 0755 " + instanceSrcDir,
		"tar xzf /opt/moony/src.tar.gz -C " + instanceSrcDir,
		fmt.Sprintf(
			"TUNNEL_HOST=%s SITE_ADDRESS=%s ACME_EMAIL=%s WITH_DOWNLOADS=%s ENV_PARAMETER=%s bash %s",
			shellQuote(in.TunnelHost), shellQuote(in.SiteAddress), shellQuote(in.AcmeEmail),
			shellQuote(in.WithDownloads), shellQuote(in.EnvParameter), applyPath(),
		),
	}
}

// sendCommandInput builds the JSON for `aws ssm send-command --cli-input-json`.
//
// A file plus real JSON encoding rather than shorthand parameters on the command line:
// shorthand splits list values on commas, and any hand-rolled escaping has to survive
// JSON and two shells. Passing the script as one array element per line also means the
// script shows up readable in the SSM console.
func sendCommandInput(instanceID string, lines []string) ([]byte, error) {
	type params struct {
		Commands []string `json:"commands"`
	}
	return json.MarshalIndent(struct {
		InstanceIds  []string `json:"InstanceIds"`
		DocumentName string   `json:"DocumentName"`
		Comment      string   `json:"Comment"`
		Parameters   params   `json:"Parameters"`
	}{
		InstanceIds:  []string{instanceID},
		DocumentName: "AWS-RunShellScript",
		Comment:      "moony relay converge",
		Parameters:   params{Commands: lines},
	}, "", " ")
}

// writeTempJSON writes b to a temp file and returns a file:// reference for the AWS
// CLI. The path stays native, which is the whole point of doing this from Go rather
// than a shell: under MSYS an argument that looks like an absolute Unix path is
// rewritten before aws.exe sees it.
func writeTempJSON(prefix string, b []byte) (ref string, cleanup func(), err error) {
	f, err := os.CreateTemp("", prefix+"-*.json")
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(name)
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", func() {}, err
	}
	return "file://" + filepathToURL(name), func() { os.Remove(name) }, nil
}

// filepathToURL normalises a Windows path for the AWS CLI's file:// handling.
func filepathToURL(p string) string { return strings.ReplaceAll(p, "\\", "/") }

// shellQuote wraps a value in single quotes for the remote shell.
//
// Not %q: Go's quoting leaves $ alone, so a value like $(curl ...) inside a
// double-quoted assignment would be expanded by bash on the instance. Single quotes
// suppress every expansion, and an embedded quote is closed, escaped and reopened the
// POSIX way.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
