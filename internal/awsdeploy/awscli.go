package awsdeploy

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// instanceSrcDir is where the instance unpacks the source tarball.
const instanceSrcDir = "/opt/moony/src"

// runner runs a command and returns its trimmed stdout. Stderr is folded into the
// error, because the AWS CLI reports everything interesting there and a bare exit
// status is useless for the caller.
type runner func(ctx context.Context, name string, args ...string) (string, error)

func execRunner(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var errBuf strings.Builder
	cmd.Stderr = &errBuf
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return string(out), fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), msg)
	}
	return strings.TrimSpace(string(out)), nil
}

// aws runs the AWS CLI with --region and --output text already applied.
func (d *Deployer) aws(ctx context.Context, args ...string) (string, error) {
	full := append([]string{"--region", d.opts.Region, "--output", "text"}, args...)
	return d.run(ctx, "aws", full...)
}

// Problem classifies the AWS failures worth reacting to differently. Anything else is
// reported as-is; guessing at unfamiliar errors is how misleading advice gets printed.
type Problem int

const (
	// ProblemNone means the error is not one of the recognised cases.
	ProblemNone Problem = iota
	// ProblemNotActivated is a brand new account whose sign-up was never finished.
	// STS and IAM answer normally while every regional service refuses, which reads
	// like a permissions problem and is not one.
	ProblemNotActivated
	// ProblemNoCredentials is nobody signed in, or an expired session.
	ProblemNoCredentials
	// ProblemAccessDenied is a real authorisation failure.
	ProblemAccessDenied
	// ProblemFreeTier is a service the account's plan excludes, such as domain
	// registration on the free plan.
	ProblemFreeTier
)

// classify maps an AWS CLI error message onto a Problem.
func classify(msg string) Problem {
	m := strings.ToLower(msg)
	switch {
	case strings.Contains(m, "free tier accounts are not supported"):
		return ProblemFreeTier
	case strings.Contains(m, "optinrequired"),
		strings.Contains(m, "notsignedup"),
		strings.Contains(m, "subscriptionrequired"),
		strings.Contains(m, "not subscribed to this service"):
		return ProblemNotActivated
	case strings.Contains(m, "unable to locate credentials"),
		strings.Contains(m, "expiredtoken"),
		strings.Contains(m, "invalidclienttokenid"),
		strings.Contains(m, "the security token included in the request is expired"):
		return ProblemNoCredentials
	case strings.Contains(m, "accessdenied"),
		strings.Contains(m, "unauthorizedoperation"),
		strings.Contains(m, "not authorized to perform"):
		return ProblemAccessDenied
	}
	return ProblemNone
}

// advise turns a classified failure into an error a user can act on.
func advise(p Problem, account, region string, err error) error {
	switch p {
	case ProblemNotActivated:
		return fmt.Errorf(`this AWS account is not activated for services yet, so nothing can be launched.
  account: %s
  region : %s

Finish sign-up at https://console.aws.amazon.com/billing/home#/account (payment method
plus phone verification), then run this again. Activation is usually minutes but can
take a few hours; check with: aws ec2 describe-vpcs --region %s`, account, region, region)
	case ProblemNoCredentials:
		return errors.New("not signed in to AWS: run `aws login` (or set a profile) and try again")
	case ProblemAccessDenied:
		return fmt.Errorf("these credentials are not allowed to do that: %w", err)
	case ProblemFreeTier:
		return fmt.Errorf("this account's plan excludes that service (switch to the paid plan in Billing): %w", err)
	}
	return err
}
