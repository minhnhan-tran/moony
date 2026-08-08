package awsdeploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Options is the deployment's configuration. Zero values are filled in by
// applyDefaults, and Region falls back to the CLI's configured region.
type Options struct {
	Stack         string // CloudFormation stack name
	Region        string
	Bucket        string // source bucket; defaults to moony-deploy-<account>-<region>
	VpcID         string // defaults to the account's default VPC
	SubnetID      string // defaults to the first public subnet in that VPC
	Domain        string // hostname for automatic HTTPS; empty serves plain HTTP on :80
	Email         string // Let's Encrypt contact address
	EnvParameter  string // SSM SecureString holding the relay env file
	InstanceType  string
	WebCIDR       string // who may reach the UI
	TunnelCIDR    string // who may reach the connector tunnel
	WithDownloads bool   // also cross-build the CLI binaries served at /download
	Root          string // repository root; defaults to the working tree's
}

// Defaults chosen to be safe rather than convenient: the firewall opens to the world
// only because a relay nobody can reach is useless, and the deploy prints a warning
// when that is combined with no authentication.
func (o *Options) applyDefaults() {
	if o.Stack == "" {
		o.Stack = "moony-relay"
	}
	if o.InstanceType == "" {
		o.InstanceType = "t4g.small"
	}
	if o.WebCIDR == "" {
		o.WebCIDR = "0.0.0.0/0"
	}
	if o.TunnelCIDR == "" {
		o.TunnelCIDR = "0.0.0.0/0"
	}
}

// Deployer runs one deployment. Not safe for concurrent use; the web handler
// serialises runs, and two concurrent deploys of one stack would fight anyway.
type Deployer struct {
	opts Options
	log  io.Writer
	run  runner

	account string
}

// New returns a Deployer that reports progress to log.
func New(opts Options, log io.Writer) *Deployer {
	opts.applyDefaults()
	if log == nil {
		log = io.Discard
	}
	return &Deployer{opts: opts, log: log, run: execRunner}
}

// Options returns the effective configuration, after defaults and discovery.
func (d *Deployer) Options() Options { return d.opts }

func (d *Deployer) logf(format string, args ...any) {
	fmt.Fprintf(d.log, format+"\n", args...)
}

// Preflight resolves the region, checks the tooling and credentials, and turns the
// recognised AWS failures into advice. It is safe to call on its own: nothing here
// mutates anything.
func (d *Deployer) Preflight(ctx context.Context) error {
	if _, err := exec.LookPath("aws"); err != nil {
		return errors.New("the AWS CLI is not on PATH: install it, then run `aws login`")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return errors.New("git is required: the source is shipped to the instance with `git archive`")
	}

	if d.opts.Region == "" {
		region, err := d.run(ctx, "aws", "configure", "get", "region")
		if err != nil || strings.TrimSpace(region) == "" {
			return errors.New("no region: pass one, or set a default with `aws configure set region <region>`")
		}
		d.opts.Region = strings.TrimSpace(region)
	}

	// Account and ARN together: the ARN's shape differs between root, an IAM user and
	// an assumed role, so it is not something to parse an account id out of.
	ident, err := d.aws(ctx, "sts", "get-caller-identity", "--query", "[Account,Arn]")
	if err != nil {
		return advise(classify(err.Error()), "unknown", d.opts.Region, err)
	}
	fields := strings.Fields(ident)
	if len(fields) < 2 {
		return fmt.Errorf("could not read the caller identity from %q", ident)
	}
	d.account, _ = fields[0], fields[1]
	if strings.HasSuffix(fields[1], ":root") {
		d.logf("warning: these are account ROOT credentials; create an IAM Identity Center user or role for day-to-day use")
	}

	// A probe against a regional service: this is what fails on an account whose
	// sign-up was never finished.
	if _, err := d.aws(ctx, "ec2", "describe-vpcs", "--max-items", "1"); err != nil {
		return advise(classify(err.Error()), d.account, d.opts.Region, err)
	}
	return nil
}

// Deploy creates or updates the stack, then converges the instance.
func (d *Deployer) Deploy(ctx context.Context) (Status, error) {
	if err := d.Preflight(ctx); err != nil {
		return Status{}, err
	}
	if err := d.discover(ctx); err != nil {
		return Status{}, err
	}
	if d.opts.Bucket == "" {
		d.opts.Bucket = fmt.Sprintf("moony-deploy-%s-%s", d.account, d.opts.Region)
	}
	if err := d.ensureBucket(ctx); err != nil {
		return Status{}, err
	}

	key, err := d.uploadSource(ctx)
	if err != nil {
		return Status{}, err
	}
	if err := d.deployStack(ctx, key); err != nil {
		return Status{}, err
	}

	st, err := d.Status(ctx)
	if err != nil {
		return st, err
	}
	if err := d.converge(ctx, st.Outputs["InstanceId"], st.Outputs["PublicIp"], key); err != nil {
		return st, err
	}
	if d.opts.EnvParameter == "" {
		d.logf("warning: no --env-parameter, so the relay runs WITHOUT authentication;")
		d.logf("         anyone who can reach %s can drive your connector's machine", st.Outputs["WebUrl"])
	}
	return st, nil
}

// discover fills in the VPC and subnet when they were not given.
func (d *Deployer) discover(ctx context.Context) error {
	if d.opts.VpcID == "" {
		vpc, err := d.aws(ctx, "ec2", "describe-vpcs",
			"--filters", "Name=is-default,Values=true", "--query", "Vpcs[0].VpcId")
		if err != nil {
			return advise(classify(err.Error()), d.account, d.opts.Region, err)
		}
		if vpc == "" || vpc == "None" {
			return fmt.Errorf("no default VPC in %s: pass a VPC id", d.opts.Region)
		}
		d.opts.VpcID = vpc
		d.logf("using default VPC %s", vpc)
	}
	if d.opts.SubnetID == "" {
		// Public means auto-assign public IP: the instance needs one for the tunnel
		// and for the ACME challenge, and this design has no NAT gateway.
		subnet, err := d.aws(ctx, "ec2", "describe-subnets",
			"--filters", "Name=vpc-id,Values="+d.opts.VpcID, "Name=map-public-ip-on-launch,Values=true",
			"--query", "sort_by(Subnets,&AvailabilityZone)[0].SubnetId")
		if err != nil {
			return err
		}
		if subnet == "" || subnet == "None" {
			return fmt.Errorf("no public subnet in %s (none auto-assign a public IP): pass a subnet id", d.opts.VpcID)
		}
		d.opts.SubnetID = subnet
		d.logf("using public subnet %s", subnet)
	}
	return nil
}

// ensureBucket creates the private, encrypted source bucket if it is missing.
func (d *Deployer) ensureBucket(ctx context.Context) error {
	if _, err := d.aws(ctx, "s3api", "head-bucket", "--bucket", d.opts.Bucket); err == nil {
		return nil
	}
	d.logf("creating s3://%s", d.opts.Bucket)
	args := []string{"s3api", "create-bucket", "--bucket", d.opts.Bucket}
	if d.opts.Region != "us-east-1" {
		// us-east-1 rejects an explicit LocationConstraint naming itself.
		args = append(args, "--create-bucket-configuration", "LocationConstraint="+d.opts.Region)
	}
	if _, err := d.aws(ctx, args...); err != nil {
		return advise(classify(err.Error()), d.account, d.opts.Region, err)
	}
	if _, err := d.aws(ctx, "s3api", "put-public-access-block", "--bucket", d.opts.Bucket,
		"--public-access-block-configuration",
		"BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true"); err != nil {
		return err
	}
	_, err := d.aws(ctx, "s3api", "put-bucket-encryption", "--bucket", d.opts.Bucket,
		"--server-side-encryption-configuration",
		`{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}`)
	return err
}

// uploadSource ships the commit to S3 and returns its key.
//
// HEAD rather than the working tree, so a deploy is reproducible from a commit. The key
// carries the revision and a timestamp because a reused key would leave CloudFormation
// seeing no change at all.
func (d *Deployer) uploadSource(ctx context.Context) (string, error) {
	root := d.opts.Root
	if root == "" {
		out, err := d.run(ctx, "git", "rev-parse", "--show-toplevel")
		if err != nil {
			return "", fmt.Errorf("not inside a git checkout, and the instance builds from source: %w", err)
		}
		root = strings.TrimSpace(out)
		d.opts.Root = root
	}

	rev, err := d.run(ctx, "git", "-C", root, "rev-parse", "--short", "HEAD")
	if err != nil {
		return "", err
	}
	rev = strings.TrimSpace(rev)

	if _, err := d.run(ctx, "git", "-C", root, "diff-index", "--quiet", "HEAD", "--"); err != nil {
		d.logf("warning: uncommitted changes are NOT deployed; only %s is", rev)
	}

	tmp, err := os.CreateTemp("", "moony-src-*.tar.gz")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpName)

	if _, err := d.run(ctx, "git", "-C", root, "archive", "--format=tar.gz",
		"-o", tmpName, "HEAD"); err != nil {
		return "", err
	}

	key := fmt.Sprintf("moony/relay-src-%s-%s.tar.gz", rev, time.Now().UTC().Format("20060102150405"))
	d.logf("packaging %s", rev)
	if _, err := d.aws(ctx, "s3", "cp", tmpName, "s3://"+d.opts.Bucket+"/"+key); err != nil {
		return "", err
	}
	return key, nil
}

// deployStack writes the embedded template to a temp file and hands it to
// `cloudformation deploy`, which does the create-or-update change set dance.
func (d *Deployer) deployStack(ctx context.Context, key string) error {
	f, err := os.CreateTemp("", "moony-cfn-*.yaml")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(Template()); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	withDownloads := "0"
	if d.opts.WithDownloads {
		withDownloads = "1"
	}
	d.logf("deploying stack %s", d.opts.Stack)
	_, err = d.aws(ctx, "cloudformation", "deploy",
		"--stack-name", d.opts.Stack,
		"--template-file", filepath.Clean(name),
		"--capabilities", "CAPABILITY_IAM",
		"--no-fail-on-empty-changeset",
		"--parameter-overrides",
		"VpcId="+d.opts.VpcID,
		"SubnetId="+d.opts.SubnetID,
		"SourceBucket="+d.opts.Bucket,
		"SourceKey="+key,
		"DomainName="+d.opts.Domain,
		"AcmeEmail="+d.opts.Email,
		"EnvParameterName="+d.opts.EnvParameter,
		"InstanceType="+d.opts.InstanceType,
		"TunnelCidr="+d.opts.TunnelCIDR,
		"WebCidr="+d.opts.WebCIDR,
		"WithDownloads="+withDownloads,
	)
	if err != nil {
		return advise(classify(err.Error()), d.account, d.opts.Region, err)
	}
	return nil
}

// converge runs apply.sh on the instance. See convergeScript for why this is not
// optional.
func (d *Deployer) converge(ctx context.Context, instanceID, tunnelHost, key string) error {
	if instanceID == "" {
		return errors.New("the stack has no InstanceId output; cannot converge")
	}
	if err := d.waitForSSM(ctx, instanceID); err != nil {
		return err
	}

	site := d.opts.Domain
	if site == "" {
		site = ":80"
	}
	withDownloads := "0"
	if d.opts.WithDownloads {
		withDownloads = "1"
	}
	input, err := sendCommandInput(instanceID, convergeScript(convergeInputs{
		Bucket:        d.opts.Bucket,
		Key:           key,
		TunnelHost:    tunnelHost,
		SiteAddress:   site,
		AcmeEmail:     d.opts.Email,
		WithDownloads: withDownloads,
		EnvParameter:  d.opts.EnvParameter,
	}))
	if err != nil {
		return err
	}
	ref, cleanup, err := writeTempJSON("moony-ssm", input)
	if err != nil {
		return err
	}
	defer cleanup()

	cid, err := d.aws(ctx, "ssm", "send-command", "--cli-input-json", ref, "--query", "Command.CommandId")
	if err != nil {
		return advise(classify(err.Error()), d.account, d.opts.Region, err)
	}
	d.logf("converging on %s (command %s); this rebuilds the relay if the source changed", instanceID, cid)

	status := "Pending"
	for i := 0; i < 90; i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
		out, err := d.aws(ctx, "ssm", "get-command-invocation",
			"--command-id", cid, "--instance-id", instanceID, "--query", "Status")
		if err != nil {
			continue // the invocation is not queryable for a moment after sending
		}
		status = out
		switch status {
		case "Success", "Failed", "Cancelled", "TimedOut":
			i = 90
		}
	}

	tail, _ := d.aws(ctx, "ssm", "get-command-invocation",
		"--command-id", cid, "--instance-id", instanceID, "--query", "StandardOutputContent")
	for _, line := range lastLines(tail, 6) {
		d.logf("  %s", line)
	}
	if status != "Success" {
		errOut, _ := d.aws(ctx, "ssm", "get-command-invocation",
			"--command-id", cid, "--instance-id", instanceID, "--query", "StandardErrorContent")
		for _, line := range lastLines(errOut, 20) {
			d.logf("  %s", line)
		}
		return fmt.Errorf("convergence failed on %s (status %s)", instanceID, status)
	}
	return nil
}

// waitForSSM waits for the instance to register with Systems Manager. A freshly
// created one takes a minute or two, and its own user-data is doing the same work in
// the meantime, so this is a wait rather than a failure.
func (d *Deployer) waitForSSM(ctx context.Context, instanceID string) error {
	for i := 0; i < 30; i++ {
		out, err := d.aws(ctx, "ssm", "describe-instance-information",
			"--filters", "Key=InstanceIds,Values="+instanceID,
			"--query", "InstanceInformationList[0].PingStatus")
		if err == nil && out == "Online" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Second):
		}
	}
	return fmt.Errorf("%s never registered with SSM; cannot apply configuration to it", instanceID)
}

// Status describes the deployed stack.
type Status struct {
	Region      string            `json:"region"`
	Account     string            `json:"account"`
	Stack       string            `json:"stack"`
	StackStatus string            `json:"stackStatus"`
	Outputs     map[string]string `json:"outputs"`
}

// Status reads the stack's status and outputs. A missing stack is not an error: it
// reports an empty StackStatus, which is what the UI needs to offer a first deploy.
func (d *Deployer) Status(ctx context.Context) (Status, error) {
	st := Status{Stack: d.opts.Stack, Region: d.opts.Region, Account: d.account, Outputs: map[string]string{}}
	if st.Region == "" {
		if err := d.Preflight(ctx); err != nil {
			return st, err
		}
		st.Region, st.Account = d.opts.Region, d.account
	}

	status, err := d.aws(ctx, "cloudformation", "describe-stacks",
		"--stack-name", d.opts.Stack, "--query", "Stacks[0].StackStatus")
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			return st, nil
		}
		return st, advise(classify(err.Error()), d.account, d.opts.Region, err)
	}
	st.StackStatus = status

	out, err := d.aws(ctx, "cloudformation", "describe-stacks",
		"--stack-name", d.opts.Stack, "--query", "Stacks[0].Outputs[].[OutputKey,OutputValue]")
	if err != nil {
		return st, err
	}
	for _, line := range strings.Split(out, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "\t"); ok {
			st.Outputs[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return st, nil
}

// Delete tears the stack down. The source bucket is left alone: it may hold other
// deployments' tarballs, and deleting a bucket needs it emptied first.
func (d *Deployer) Delete(ctx context.Context) error {
	if err := d.Preflight(ctx); err != nil {
		return err
	}
	if _, err := d.aws(ctx, "cloudformation", "delete-stack", "--stack-name", d.opts.Stack); err != nil {
		return advise(classify(err.Error()), d.account, d.opts.Region, err)
	}
	d.logf("deleting %s; the source bucket is left in place", d.opts.Stack)
	return nil
}

func lastLines(s string, n int) []string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}
