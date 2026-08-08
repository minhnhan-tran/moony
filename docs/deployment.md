# Deployment

The **relay** is the only thing you deploy. It is a small, stateless reverse proxy:

- Browsers connect to it over HTTP (**:8770**) and get the full web UI.
- Your machine — laptop, dev box, CI runner — dials **out** to it over raw TCP (**:8771**) running `moony`, and *that* process is what holds the agent, reads your files and runs your commands.

Nothing about your code or your sessions lives on the relay. It holds no disk state, which is why it runs happily with a read-only root filesystem and can be restarted at will.

```
    browser ──HTTP :8770──▶  relay  ◀──TCP :8771──  moony (your machine)
                          (stateless)               agent · files · shell
```

> [!IMPORTANT]
> **The relay is unauthenticated by default.** Anyone who can reach port 8770 can drive the connected machine's agent — including running shell commands on it. That is fine bound to `localhost`. Before exposing it anywhere else, turn on auth (see [Authentication](#authentication)).

## Run it locally

```sh
make build-relay
./bin/relay -addr localhost:8770 -tunnel localhost:8771
```

In another terminal, point the agent at it:

```sh
MN_RELAY=localhost:8771 ./bin/moony
```

Open <http://localhost:8770>. Flags:

| Flag | Default | Purpose |
|---|---|---|
| `-addr` | `127.0.0.1:8770` | where browsers connect |
| `-tunnel` | `127.0.0.1:8771` | where connectors dial in (raw TCP) |
| `-tunnel-public` | _(unset)_ | **public** `host:port` connectors reach the tunnel at; advertised at `/api/relay` and baked into `/install.sh`. Also settable as `MN_RELAY_TUNNEL` |
| `-downloads` | _(unset)_ | directory of prebuilt CLI binaries to serve at `/download` and `/install.sh` |
| `-tls-cert` / `-tls-key` | _(unset)_ | PEM cert/key to serve HTTPS directly. Also `MN_RELAY_TLS_CERT` / `MN_RELAY_TLS_KEY` |
| `-healthcheck` | _(unset)_ | probe mode: `GET /healthz` at `host:port`, exit 0/1, then quit |

Note the defaults bind to `127.0.0.1`, so a relay in a container must be told to listen on all interfaces (`-addr 0.0.0.0:8770`) — which is what the image's entrypoint does.

## Docker

```sh
docker compose up -d --build
docker compose logs -f relay
```

The image is multi-stage and ends on `gcr.io/distroless/static-debian12:nonroot` — no shell, no package manager, no root user. It also cross-compiles the CLI for macOS and Linux so the relay's `/install.sh` can hand out matching binaries. That is four extra builds; skip them while iterating:

```sh
docker build --build-arg WITH_DOWNLOADS=0 -t moony-relay:dev .
```

Because the image has no shell, the container healthcheck re-execs the relay itself in `-healthcheck` mode rather than calling `curl`.

### `MN_RELAY_TUNNEL` is the setting people get wrong

It is the **public** address connectors should dial (equivalently, the `-tunnel-public` flag). The relay advertises it at `/api/relay` and bakes it into the `/install.sh` one-liner, so if it is wrong, every machine that runs the installer gets a daemon that cannot connect — and the UI just sits at *"waiting for a connector"* with nothing obviously broken. It must be reachable **from the connector's machine**, so `localhost:8771` only works when the connector runs on the same host.

## Kubernetes

```sh
kubectl apply -k deploy/k8s
```

| File | What it is |
|---|---|
| `deployment.yaml` | 1 replica, non-root (65532), read-only rootfs, all capabilities dropped, probes on `/healthz` |
| `service.yaml` | `ClusterIP` for :8770 **and** a separate `LoadBalancer` for :8771 |
| `ingress.yaml` | Browser side only, with SSE-safe timeouts |
| `pdb.yaml` | `maxUnavailable: 1` so node drains aren't blocked |
| `secret.example.yaml` | Template for the auth variables — **not** applied by the kustomization |

Three things worth understanding before you adapt these.

**The two ports need two different front doors.** :8770 is HTTP and can sit behind an Ingress. :8771 is *not* HTTP — it is a raw TCP tunnel, so it cannot share an L7 ingress and needs its own L4 path (`LoadBalancer`, `NodePort`, or MetalLB). Trying to route both through one Ingress is the most common way to deploy this wrong: the browser side works, connectors silently fail to dial.

**Scaling past 1 replica needs thought.** A connector's tunnel is one long-lived TCP connection to *one* pod, and only that pod can serve that connector's sessions. With several replicas and no affinity, a browser request can land on a pod that has no connector attached. Either stay at 1 replica, or run one connector per replica with `sessionAffinity: ClientIP` (already set on both Services).

**SSE needs generous proxy timeouts.** A turn can legitimately stream for many minutes. The Ingress sets `proxy-read-timeout: 3600` and `proxy-buffering: off`; with a default 60-second timeout, long turns die mid-stream, and with buffering on, tokens arrive in clumps instead of live.

## AWS (single EC2 instance)

`deploy/aws/ec2/` deploys the relay to one EC2 instance for about $15/month: Caddy
terminates TLS in front of port 8770 and the connector tunnel is published straight on
an Elastic IP.

```sh
deploy/aws/ec2/deploy.sh --domain relay.example.com --email you@example.com \
  --env-parameter /moony/relay-env
```

It packages the working tree with `git archive`, uploads it to a private S3 bucket, and
creates a CloudFormation stack that builds and runs it on the box, so the repo can stay
private and no deploy key is needed. See [../deploy/aws/ec2/README.md](../deploy/aws/ec2/README.md).

The reason it is one instance rather than a load-balanced service: the relay needs an
L7 listener for the browser (8770) *and* an L4 one for the tunnel (8771), which on AWS
means an ALB plus an NLB, roughly triple the cost of the compute they front. Use the
Kubernetes manifests above on EKS when you need more than one box; `service.yaml`
already carries the NLB annotations.

## Vercel and other serverless hosts

The relay cannot run on Vercel, Netlify, Cloudflare Pages, or Lambda. It needs a raw TCP
listener for connectors to dial into (length-prefixed JSON frames over a `net.Conn`, not
HTTP) and one long-lived process holding the connector registry and every open SSE
stream. Serverless platforms offer neither.

Vercel can only sit in front as a reverse proxy, with the relay running on a VM: see
[../deploy/vercel/README.md](../deploy/vercel/README.md). Worth it only if you must
serve the UI from a domain Vercel already manages.

## Authentication

Set `MN_AUTH=on` and the relay requires the full OIDC set below, performing discovery against the issuer at startup. A missing variable or an unreachable issuer is **fatal by design** — it fails closed rather than starting half-secured, so a typo can't quietly leave the gate open.

| Variable | Required | Purpose |
|---|---|---|
| `MN_AUTH` | — | `on`/`1`/`true`/`yes` enables the gate. Unset = open relay |
| `MN_AUTH_SECRET` | yes | signs the browser cookie **and** the connector credential. Every replica must share the same value. `openssl rand -hex 32` |
| `MN_OIDC_ISSUER` | yes | e.g. `https://accounts.example.com` |
| `MN_OIDC_CLIENT_ID` | yes | |
| `MN_OIDC_CLIENT_SECRET` | yes | |
| `MN_OIDC_REDIRECT_URL` | yes | the relay's public URL + `/auth/callback`, registered verbatim with the provider |
| `MN_AUTH_ADMINS` | no | comma-separated accounts granted the admin surface |
| `MN_AUTH_TTL` | no | session lifetime, e.g. `24h` |
| `MN_OIDC_ACCOUNT_CLAIM` | no | which claim identifies the account |

Create the Secret without committing values:

```sh
kubectl create secret generic moony-relay-auth \
  --from-literal=MN_AUTH=on \
  --from-literal=MN_AUTH_SECRET="$(openssl rand -hex 32)" \
  --from-literal=MN_OIDC_ISSUER=https://accounts.example.com \
  --from-literal=MN_OIDC_CLIENT_ID=moony-relay \
  --from-literal=MN_OIDC_CLIENT_SECRET=... \
  --from-literal=MN_OIDC_REDIRECT_URL=https://relay.example.com/auth/callback \
  --from-literal=MN_AUTH_ADMINS=you@example.com
```

The Deployment references it with `optional: true`, so the relay starts (open) until the Secret exists.

## TLS

Terminate TLS at your ingress or load balancer — that is the usual setup, and what `ingress.yaml` assumes. The relay can also do it directly:

```sh
MN_RELAY_TLS_CERT=cert.pem MN_RELAY_TLS_KEY=key.pem ./bin/relay -addr 0.0.0.0:8443
```

## Health checks

`GET /healthz` returns `200 ok`. It is served by the relay itself rather than proxied to a connector, so it answers even when no connector is attached — which is the right readiness signal: the relay *is* ready to accept one. Don't health-check :8771; it is a bare TCP listener with no handshake to speak, and "no connector attached" is a normal idle state.

## Serving CLI downloads

Point `-downloads` at a directory of `moony_<os>_<arch>` binaries (plus an optional `SHA256SUMS`) and the relay serves `/download` and an `/install.sh` one-liner for onboarding new machines. The Docker image populates this for you unless you build with `WITH_DOWNLOADS=0`.
