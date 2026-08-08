#!/usr/bin/env bash
# Deploy (or update) the moony relay on a single EC2 instance.
#
#   deploy/aws/ec2/deploy.sh --vpc vpc-123 --subnet subnet-456 [--domain relay.example.com]
#
# What it does: packages the working tree with `git archive`, uploads it to a private
# S3 bucket, then creates or updates the CloudFormation stack that builds and runs it.
# The repo can stay private: the instance reads the tarball with its own IAM role, so
# no deploy key or token is involved.
#
# Re-running it is the update path. A new tarball key forces the instance to be
# replaced, which is the simplest correct way to redeploy a box whose whole state is
# the image it built. The Elastic IP is kept, so the tunnel address does not change.
set -euo pipefail

STACK=moony-relay
REGION="${AWS_REGION:-$(aws configure get region)}"
BUCKET=""
VPC=""
SUBNET=""
DOMAIN=""
EMAIL=""
ENV_PARAM=""
INSTANCE_TYPE="t4g.small"
TUNNEL_CIDR="0.0.0.0/0"
WEB_CIDR="0.0.0.0/0"
WITH_DOWNLOADS="0"
ACCOUNT=""

usage() {
	sed -n '2,14p' "$0" | sed 's/^# \{0,1\}//'
	cat <<'EOF'

Optional:
  --vpc VPC_ID              VPC to launch into (default: the account's default VPC)
  --subnet SUBNET_ID        a PUBLIC subnet in it (default: the first public subnet found)
  --stack NAME              stack name (default: moony-relay)
  --region REGION           AWS region (default: your configured region)
  --bucket NAME             source bucket (default: moony-deploy-<account>-<region>, created if absent)
  --domain HOST             DNS name for the UI; enables automatic HTTPS
  --email ADDR              Let's Encrypt contact address
  --env-parameter NAME      SSM SecureString holding the relay env file (auth settings)
  --instance-type TYPE      default t4g.small
  --tunnel-cidr CIDR        who may reach port 8771 (default: the world)
  --web-cidr CIDR           who may reach ports 80/443 (default: the world)
  --with-downloads          also cross-build the CLI binaries served at /download
  --delete                  tear the stack down
EOF
}

DELETE=0
while [ $# -gt 0 ]; do
	case "$1" in
	--stack) STACK="$2"; shift 2 ;;
	--region) REGION="$2"; shift 2 ;;
	--bucket) BUCKET="$2"; shift 2 ;;
	--vpc) VPC="$2"; shift 2 ;;
	--subnet) SUBNET="$2"; shift 2 ;;
	--domain) DOMAIN="$2"; shift 2 ;;
	--email) EMAIL="$2"; shift 2 ;;
	--env-parameter) ENV_PARAM="$2"; shift 2 ;;
	--instance-type) INSTANCE_TYPE="$2"; shift 2 ;;
	--tunnel-cidr) TUNNEL_CIDR="$2"; shift 2 ;;
	--web-cidr) WEB_CIDR="$2"; shift 2 ;;
	--with-downloads) WITH_DOWNLOADS="1"; shift ;;
	--delete) DELETE=1; shift ;;
	-h | --help) usage; exit 0 ;;
	*) echo "unknown option: $1" >&2; usage; exit 2 ;;
	esac
done

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
root="$(cd "$here/../../.." && pwd)"
: "${REGION:?set --region or configure a default region}"

die() {
	echo "error: $*" >&2
	exit 1
}

# Fail with the actual reason rather than a wall of CloudFormation output. The
# not-signed-up case in particular looks like a permissions problem and is not one: a
# brand new account has credentials that work for IAM and STS while every regional
# service still rejects it until sign-up is finished.
preflight() {
	command -v aws >/dev/null || die "the AWS CLI is not on PATH"
	command -v git >/dev/null || die "git is required (the source is shipped with git archive)"

	# Account and ARN in one call: the ARN shape differs between root, an IAM user and
	# an assumed role, so it is not something to parse an account id out of.
	local ident caller
	ident="$(aws sts get-caller-identity --query '[Account,Arn]' --output text 2>&1)" ||
		die "not signed in: run 'aws login' first ($ident)"
	ACCOUNT="$(printf '%s
' "$ident" | awk '{print $1}')"
	caller="$(printf '%s
' "$ident" | awk '{print $2}')"
	[ -n "$ACCOUNT" ] || die "could not determine the account id from: $ident"

	case "$caller" in
	*:root)
		echo "warning: these are account ROOT credentials." >&2
		echo "         create an IAM Identity Center user or role for day-to-day use." >&2
		;;
	esac

	local probe
	if ! probe="$(aws ec2 describe-vpcs --region "$REGION" --max-items 1 --output text 2>&1)"; then
		case "$probe" in
		*OptInRequired* | *NotSignedUp* | *SubscriptionRequiredException*)
			die "$(
				cat <<EOF
this AWS account is not activated for services yet, so nothing can be launched.
  account: $ACCOUNT
  region : $REGION

Finish sign-up at https://console.aws.amazon.com/billing/home#/account
(payment method plus phone verification), then re-run this script. Activation is
usually minutes but can take a few hours; check with:

  aws ec2 describe-vpcs --region $REGION
EOF
			)"
			;;
		*UnauthorizedOperation* | *AccessDenied*)
			die "these credentials cannot describe VPCs in $REGION: $probe"
			;;
		*)
			die "cannot reach EC2 in $REGION: $probe"
			;;
		esac
	fi
}

if [ "$DELETE" = "1" ]; then
	aws cloudformation delete-stack --stack-name "$STACK" --region "$REGION"
	echo "deleting $STACK; watch with: aws cloudformation wait stack-delete-complete --stack-name $STACK --region $REGION"
	exit 0
fi

preflight

if [ -z "$VPC" ]; then
	VPC="$(aws ec2 describe-vpcs --region "$REGION" \
		--filters Name=is-default,Values=true --query 'Vpcs[0].VpcId' --output text)"
	[ "$VPC" != "None" ] && [ -n "$VPC" ] || die "no default VPC in $REGION; pass --vpc"
	echo "using default VPC $VPC"
fi

if [ -z "$SUBNET" ]; then
	# Public means auto-assign public IP: the instance needs one for the tunnel and
	# for the ACME challenge, and there is no NAT gateway in this design.
	SUBNET="$(aws ec2 describe-subnets --region "$REGION" \
		--filters "Name=vpc-id,Values=$VPC" Name=map-public-ip-on-launch,Values=true \
		--query 'sort_by(Subnets,&AvailabilityZone)[0].SubnetId' --output text)"
	[ "$SUBNET" != "None" ] && [ -n "$SUBNET" ] || die \
		"no public subnet in $VPC (none auto-assign a public IP); pass --subnet"
	echo "using public subnet $SUBNET"
fi

[ -n "$BUCKET" ] || BUCKET="moony-deploy-${ACCOUNT}-${REGION}"

# Private, versioned-off, encrypted bucket for a tarball nobody else should read.
if ! aws s3api head-bucket --bucket "$BUCKET" 2>/dev/null; then
	echo "creating s3://$BUCKET"
	if [ "$REGION" = "us-east-1" ]; then
		aws s3api create-bucket --bucket "$BUCKET" --region "$REGION" >/dev/null
	else
		aws s3api create-bucket --bucket "$BUCKET" --region "$REGION" \
			--create-bucket-configuration "LocationConstraint=$REGION" >/dev/null
	fi
	aws s3api put-public-access-block --bucket "$BUCKET" \
		--public-access-block-configuration \
		"BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true"
	aws s3api put-bucket-encryption --bucket "$BUCKET" \
		--server-side-encryption-configuration \
		'{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
fi

# The key includes the commit and a timestamp so every deploy is a distinct object:
# same key would mean CloudFormation sees no change and never replaces the instance.
rev="$(git -C "$root" rev-parse --short HEAD 2>/dev/null || echo nogit)"
key="moony/relay-src-${rev}-$(date -u +%Y%m%d%H%M%S).tar.gz"
tmp="$(mktemp -t moony-src-XXXXXX.tar.gz)"
trap 'rm -f "$tmp"' EXIT

echo "packaging $rev"
git -C "$root" archive --format=tar.gz -o "$tmp" HEAD
aws s3 cp "$tmp" "s3://$BUCKET/$key" --region "$REGION" >/dev/null

echo "deploying stack $STACK"
aws cloudformation deploy \
	--region "$REGION" \
	--stack-name "$STACK" \
	--template-file "$here/cloudformation.yaml" \
	--capabilities CAPABILITY_IAM \
	--parameter-overrides \
	"VpcId=$VPC" \
	"SubnetId=$SUBNET" \
	"SourceBucket=$BUCKET" \
	"SourceKey=$key" \
	"DomainName=$DOMAIN" \
	"AcmeEmail=$EMAIL" \
	"EnvParameterName=$ENV_PARAM" \
	"InstanceType=$INSTANCE_TYPE" \
	"TunnelCidr=$TUNNEL_CIDR" \
	"WebCidr=$WEB_CIDR" \
	"WithDownloads=$WITH_DOWNLOADS"

aws cloudformation describe-stacks --stack-name "$STACK" --region "$REGION" \
	--query 'Stacks[0].Outputs[].[OutputKey,OutputValue]' --output table

cat <<EOF

The instance still has to install Docker and build the relay, which takes a few
minutes after the stack reports CREATE_COMPLETE. Watch it with:

  aws ssm start-session --target <InstanceId> --region $REGION
  sudo tail -f /var/log/moony-bootstrap.log
EOF
