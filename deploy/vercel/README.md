# Vercel in front of the relay

**The relay cannot run on Vercel.** This directory is for one narrow case: you already
own a Vercel-managed domain and want the UI served from it. Vercel is then a reverse
proxy, and the relay still runs somewhere that can hold sockets.

If you don't have that constraint, point DNS straight at the relay. Caddy already
terminates TLS in `deploy/aws/ec2/`, and a proxy in front adds a hop, a timeout risk,
and a second bill for nothing.

## Why the relay itself can't be deployed here

| What the relay needs | Vercel |
|---|---|
| A raw TCP listener on 8771 for connectors to dial into. Frames are length-prefixed JSON over a `net.Conn` (`internal/relay/protocol.go`), not HTTP, so it cannot be a function. | HTTP(S) to functions only. No TCP ingress. |
| One long-lived process holding the registry of connected connectors and every open SSE stream. | Stateless per-request invocations. Each would see an empty registry. |
| Responses that stream for minutes (a turn). | Function duration caps; proxied responses are subject to gateway timeouts. |

There is no configuration that gets around the first row. Something must be listening
on a socket, continuously, for a connector to connect to.

## The split

```
browser ──▶ app.vercel.app ──rewrite──▶ relay.example.com:443 ──▶ relay:8770
laptop  ─────────────────────────────▶ relay.example.com:8771   (never touches Vercel)
```

1. Deploy the relay first: `deploy/aws/ec2/` (~$15/mo), Fly.io, Railway, or any VM.
   It needs a public hostname with TLS and its tunnel port reachable.
2. Copy `vercel.json` to the root of the Vercel project and replace `RELAY_HOST` with
   that hostname. There is nothing to build: the project is only rewrites.
3. `vercel deploy --prod`.
4. Set the relay's env accordingly:
   - `MN_OIDC_REDIRECT_URL=https://<your-vercel-domain>/auth/callback` — the browser
     lands on the Vercel domain, so the callback must too.
   - `MN_RELAY_TUNNEL=relay.example.com:8771` — the relay's **own** address. This is
     advertised at `/api/relay` and baked into `/install.sh`; pointing it at the Vercel
     domain hands every connector an address that cannot accept a TCP connection.

The catch-all rewrite is what makes this work at all: the browser only ever talks to
the Vercel origin, so the session cookie and the OIDC redirect stay same-origin. The
SPA has no notion of a remote API host anyway (`const api = (path) => apiBase + path`,
where `apiBase` is a path prefix like `/l/{host}`), and the relay sends no CORS
headers, so a browser-side split origin is not an option.

## What to check after deploying

- **Streaming.** Open a session and send a long prompt. Tokens must appear as they are
  produced. If output arrives only when the turn ends, or the stream dies after a fixed
  interval, Vercel's proxy is buffering or timing it out and you should drop the proxy.
- **`/install.sh`.** It is proxied too, so installer traffic is billed as Vercel
  bandwidth. The first two rewrite rules exist to make that easy to change: point
  `/download/*` and `/install.sh` at the relay's own hostname to bypass Vercel.
- **The relay stays publicly exposed.** Vercel does not offer stable egress IPs outside
  Enterprise, so you cannot firewall the relay to "only Vercel". The proxy is
  cosmetic, not a security boundary. Keep `MN_AUTH=on` and treat the relay's own
  hostname as public.
