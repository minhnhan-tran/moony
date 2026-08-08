# moony relay on a single EC2 instance

The cheapest way to put the web UI on AWS: one instance, Caddy for TLS, no load
balancers. About **$15/month** in `ap-southeast-1` (t4g.small ~$12, 20 GB gp3 ~$1.60,
public IPv4 ~$3.60, minus whatever the free tier covers).

Only the **relay** runs on AWS. The connector (`moony` on your laptop, or on whatever
machine the agent should work on) dials *out* to the tunnel, so AWS never needs inbound
access to your machine.

```
browser ──HTTPS 443──▶ Caddy ──▶ relay:8770 ┐
                                            │  one EC2 instance, one Elastic IP
laptop  ──TCP 8771───────────────▶ relay:8771 ┘
  moony -relay=<eip>:8771
```

## Why not a load balancer

The relay needs both an HTTP listener (8770, with long-lived SSE) and a raw TCP
listener (8771). Doing that properly means an ALB *and* an NLB, roughly $35/month of
load balancer in front of $12 of compute. On one box, Caddy covers the first and the
Elastic IP covers the second. The cost is that this is a pet: single AZ, no rolling
deploy, a reboot is downtime. Fine for a relay that stores nothing. If you outgrow it,
`deploy/k8s/` has the ALB/NLB shape ready for EKS.

## Deploy

Prerequisites: AWS CLI signed in (`aws login`), an **activated** account, and a DNS name
you can point at an IP. The VPC and subnet default to the account's default VPC and its
first public subnet, so there is usually nothing to look up.

```sh
deploy/aws/ec2/deploy.sh --domain relay.example.com --email you@example.com
```

The script preflights and says what is wrong instead of letting CloudFormation fail:
not signed in, root credentials in use, or the one that looks like a permissions problem
and is not - an account whose sign-up was never finished, where STS and IAM answer
normally while every regional service returns `OptInRequired`.

The script packages the working tree with `git archive`, uploads it to a private S3
bucket, and creates the CloudFormation stack. The repo can stay private: the instance
reads the tarball with its own IAM role, so there is no deploy key anywhere.

Then:

1. Point `relay.example.com` (an A record) at the `PublicIp` output.
2. Wait a few minutes after `CREATE_COMPLETE` while the box installs Docker and builds
   the relay. Follow it with
   `aws ssm start-session --target <InstanceId>` then `sudo tail -f /var/log/moony-bootstrap.log`.
3. Open the `WebUrl` output. Caddy will have issued a certificate once DNS resolves.
4. On your machine: `moony -relay=<PublicIp>:8771` (the `ConnectorCommand` output).

Re-run the same command to update: a fresh tarball key replaces the instance and
rebuilds. The Elastic IP is kept, so `MN_RELAY_TUNNEL` and every connector config stay
valid.

## Turn on authentication before you use it

Without it, anyone who reaches the UI gets a shell on whatever machine your connector
runs on. The relay reads its auth settings from an env file that the instance pulls
from SSM Parameter Store at boot, so no secret is ever in the template, in user-data,
or in this repo.

```sh
# Any OIDC provider works; Cognito is the local one.
aws ssm put-parameter --name /moony/relay-env --type SecureString --value "$(cat <<EOF
MN_AUTH=on
MN_AUTH_SECRET=$(openssl rand -hex 32)
MN_OIDC_ISSUER=https://cognito-idp.<region>.amazonaws.com/<user-pool-id>
MN_OIDC_CLIENT_ID=...
MN_OIDC_CLIENT_SECRET=...
MN_OIDC_REDIRECT_URL=https://relay.example.com/auth/callback
MN_AUTH_ADMINS=you@example.com
EOF
)"

deploy/aws/ec2/deploy.sh --domain relay.example.com --env-parameter /moony/relay-env
```

`MN_RELAY_TUNNEL` is set by the instance itself from the Elastic IP, so leave it out of
the parameter; anything you put there is overwritten. The full variable list is in
[../../../docs/deployment.md](../../../docs/deployment.md).

Narrow the firewall while you are at it:

```sh
deploy/aws/ec2/deploy.sh ... --tunnel-cidr 203.0.113.4/32 --web-cidr 203.0.113.4/32
```

Port 80 is opened to the world **only when you pass `--domain`**, because Let's Encrypt
validates from addresses that cannot be predicted; all it serves there is the challenge
and a redirect to HTTPS, and `--web-cidr` still controls who can use the UI over 443.
Without a domain, port 80 is the UI itself and `--web-cidr` applies to it.

## Options

| Flag | Default | Notes |
|---|---|---|
| `--domain` | none | Without it the box serves plain HTTP on port 80. Development only. |
| `--env-parameter` | none | SSM SecureString with the auth settings. Without it the relay runs open. |
| `--instance-type` | `t4g.small` | arm64. For x86 also change `AmiId` to the `x86_64` SSM alias. |
| `--with-downloads` | off | Cross-builds the CLI binaries served at `/download` and `/install.sh`. Slow on a small box. |
| `--tunnel-cidr` / `--web-cidr` | `0.0.0.0/0` | Lock these down. |
| `--vpc` / `--subnet` | default VPC, first public subnet | Only needed when there is no default VPC, or several to choose from. |
| `--delete` | | Tears the stack down. The S3 bucket is left behind. |

## Operating it

```sh
aws ssm start-session --target <InstanceId>      # no SSH key, no open port 22
sudo tail -f /var/log/moony-bootstrap.log        # first boot
cd /opt/moony/src && sudo docker compose --env-file /etc/moony/compose.env \
  -f deploy/aws/ec2/compose.aws.yml logs -f      # running logs
```

The relay is stateless. Nothing on the box needs backing up except the Caddy volume
(certificates), and losing that only costs a re-issue.

## What the template creates

Security group (80, 443, 8771 in; no SSH), IAM role limited to SSM Session Manager plus
`GetObject` on the one tarball and `GetParameter` on the one env parameter, an Elastic
IP, and the instance itself (IMDSv2 required, encrypted gp3 root volume).
