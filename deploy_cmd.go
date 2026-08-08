package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/pysty1995/moony/internal/awsdeploy"
)

// runDeployCmd implements `moony deploy aws`, which puts the relay on a single EC2
// instance: Caddy terminates TLS in front of the browser port and the raw connector
// tunnel is published on an Elastic IP. The CloudFormation template and the scripts the
// instance runs are embedded in this binary; AWS is reached through the `aws` CLI, so
// profiles, SSO and `aws login` work exactly as they do in a terminal.
func runDeployCmd(args []string) int {
	if len(args) == 0 {
		return deployUsage()
	}
	switch args[0] {
	case "aws":
		return runDeployAWS(args[1:])
	case "-h", "--help", "help":
		return deployUsage()
	default:
		fmt.Fprintf(os.Stderr, "deploy: unknown target %q\n", args[0])
		return deployUsage()
	}
}

func deployUsage() int {
	fmt.Fprintln(os.Stderr, `usage: moony deploy aws [flags]

Deploys the relay to one EC2 instance (about $15/month: no load balancers). Requires the
AWS CLI signed in (`+"`aws login`"+`) and a git checkout, since the instance builds the relay
from the commit you deploy.

  --domain HOST          hostname for the UI; turns on automatic HTTPS via Let's Encrypt
  --email ADDR           Let's Encrypt contact address
  --env-parameter NAME   SSM SecureString holding the relay env file (MN_AUTH=on, OIDC).
                         Without it the relay runs with NO authentication.
  --web-cidr CIDR        who may reach the UI (default 0.0.0.0/0)
  --tunnel-cidr CIDR     who may reach the connector tunnel (default 0.0.0.0/0)
  --instance-type TYPE   default t4g.small (arm64)
  --with-downloads       also cross-build the CLI binaries served at /download
  --stack NAME           stack name (default moony-relay)
  --region REGION        default: your configured region
  --vpc ID  --subnet ID  default: the account's default VPC and its first public subnet
  --status               print the stack's status and outputs, then exit
  --print-template       print the embedded CloudFormation template, then exit
  --delete               delete the stack

Re-running is the update path, including to add --domain or --env-parameter to a live
stack: the instance is reconfigured over SSM afterwards, because CloudFormation cannot
re-run user-data on an existing box.`)
	return 2
}

func runDeployAWS(args []string) int {
	fs := flag.NewFlagSet("deploy aws", flag.ContinueOnError)
	var (
		opts          awsdeploy.Options
		status        = fs.Bool("status", false, "print the stack's status and outputs, then exit")
		printTemplate = fs.Bool("print-template", false, "print the embedded CloudFormation template, then exit")
		del           = fs.Bool("delete", false, "delete the stack")
	)
	fs.StringVar(&opts.Stack, "stack", "", "CloudFormation stack name (default moony-relay)")
	fs.StringVar(&opts.Region, "region", "", "AWS region (default: your configured region)")
	fs.StringVar(&opts.Bucket, "bucket", "", "source bucket (default moony-deploy-<account>-<region>)")
	fs.StringVar(&opts.VpcID, "vpc", "", "VPC id (default: the account's default VPC)")
	fs.StringVar(&opts.SubnetID, "subnet", "", "public subnet id (default: the first one found)")
	fs.StringVar(&opts.Domain, "domain", "", "hostname for the UI; enables automatic HTTPS")
	fs.StringVar(&opts.Email, "email", "", "Let's Encrypt contact address")
	fs.StringVar(&opts.EnvParameter, "env-parameter", "", "SSM SecureString holding the relay env file")
	fs.StringVar(&opts.InstanceType, "instance-type", "", "instance type (default t4g.small)")
	fs.StringVar(&opts.WebCIDR, "web-cidr", "", "who may reach the UI (default 0.0.0.0/0)")
	fs.StringVar(&opts.TunnelCIDR, "tunnel-cidr", "", "who may reach the tunnel (default 0.0.0.0/0)")
	fs.BoolVar(&opts.WithDownloads, "with-downloads", false, "also build the CLI binaries served at /download")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return deployUsage()
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "deploy aws: unexpected argument %q\n", fs.Arg(0))
		return 2
	}

	if *printTemplate {
		os.Stdout.Write(awsdeploy.Template())
		return 0
	}

	// A deploy runs for minutes; Ctrl-C should stop waiting on AWS rather than be
	// ignored. The stack keeps converging on its own either way.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	d := awsdeploy.New(opts, os.Stdout)

	switch {
	case *del:
		if err := d.Delete(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "deploy aws:", err)
			return 1
		}
		return 0
	case *status:
		st, err := d.Status(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "deploy aws:", err)
			return 1
		}
		printStatus(st)
		return 0
	}

	st, err := d.Deploy(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "deploy aws:", err)
		return 1
	}
	printStatus(st)
	return 0
}

func printStatus(st awsdeploy.Status) {
	if st.StackStatus == "" {
		fmt.Printf("stack %s: not deployed (region %s)\n", st.Stack, st.Region)
		return
	}
	fmt.Printf("stack %s: %s (region %s)\n", st.Stack, st.StackStatus, st.Region)
	for _, k := range []string{"WebUrl", "TunnelAddress", "ConnectorCommand", "PublicIp", "InstanceId", "ShellCommand"} {
		if v := st.Outputs[k]; v != "" {
			fmt.Printf("  %-16s %s\n", k, v)
		}
	}
}
