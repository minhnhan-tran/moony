# Spec: local login/register for the relay (password + keyfile)

Status: **implemented**. Passwords shipped in v2.30.0 (with a token-confusion fix
found while reviewing it); keyfile credentials shipped in v2.31.0. Deviations from
this spec, and why, are noted inline below.
Scope: `cmd/relay` + `internal/auth` + `internal/web/static`.

## Problem

The relay supports exactly one way to prove who you are: OIDC (`MN_AUTH=on`, which
performs discovery at boot and fails closed if any OIDC variable is missing). An
operator who does not run an identity provider therefore has no way to require a
login, and the only remaining control is a network allowlist. The deployed relay
today runs with `EnvParameterName` empty — no authentication at all — protected
solely by `WebCidr`/`TunnelCidr` pinned to a single `/32`.

Goal: let accounts be created in the app itself, authenticated by a password or by
an encrypted keyfile, so the relay can require a login without an external IdP.

## What this reuses

Authentication ends at one point that already exists:

    authGate.Sign(account, audWeb, ttl) -> "moony_session" cookie

Downstream machinery is unchanged: the `Wrap` gate, `/connect` minting
`audConnector` tokens, `SetOwnerResolver`, `SetConnectorAuth`, `IsAdmin`. This spec
adds credential *sources*, not a second session system.

## Decisions

| Question | Decision |
| --- | --- |
| Surface | The deployed relay |
| Who may register | Open self-registration |
| Credential | Password primary; ECDSA P-256 keyfile as an optional extra |
| Account store | JSON file on the instance, like `announce.OpenAt` |
| Password hash | argon2id (`golang.org/x/crypto`, already a dependency) |

Open registration is acceptable *because of per-owner isolation*: with auth on, a
stranger who registers sees no connectors and cannot reach anyone else's, because
links are keyed by owner and a connector's owner comes from its verified token.
That containment is real only once §"Isolation must fail closed" holds.

### Why ECDSA P-256 and not Ed25519

The browser must generate the key and sign the challenge with no extra JavaScript,
which means WebCrypto. Ed25519 support there is still uneven across browsers; P-256
is universal and verifies with stdlib `crypto/ecdsa`. Keyfile encryption uses only
WebCrypto primitives: PBKDF2-SHA256 over the passphrase, AES-GCM over the PKCS#8
private key.

## Findings from review that changed this design

Six defects were found by reviewing the *existing* code against the first draft of
this spec; four of them were bugs in the draft itself. They are recorded here
because each one is a requirement now, and because two are fail-open.

### 1. Local names must be namespaced, or they impersonate OIDC users

`accountFromClaims` returns a bare email (`internal/auth/oidc.go:126`), and
`linkKey(owner, host)` lowercases and trims that string
(`internal/relay/relay.go`). A local account named `alice@corp.com` would land in
the *same* ownership namespace as the OIDC user of that address and could proxy to
their machine.

**Requirement:** local identities are signed as `local:<canonical-name>`; `local:`
is a reserved prefix that registration rejects in a submitted username. This must
be settled before first deploy, because the account string is embedded in
`linkKey` and in connector tokens that live for weeks.

### 2. `MN_AUTH=local` would have disabled authentication entirely

`enabled()` accepts only `on|1|true|yes` and returns false for anything else
(`internal/auth/oidc.go:38-44`), and `cmd/relay/main.go` wires the gate,
`SetOwnerResolver` and `SetConnectorAuth` *only* when that is true. The new mode
string from the first draft would have silently produced an open relay — the exact
outcome this feature exists to prevent.

**Requirement:** `enabled()` learns `local` and `on,local`; any non-empty
unrecognised `MN_AUTH` value is a **fatal boot error**, never a downgrade.

### 3. Authenticated routes cannot live under the blanket public prefix

`isPublicPath` returns true for everything matching `strings.HasPrefix(p, "/auth/")`
(`internal/auth/middleware.go:73-75`). The first draft put `POST /auth/keys` and
`POST /auth/password` there, which would have made key enrolment and password
change reachable **without a session** — account takeover by design.

**Requirement:** replace the prefix rule with an exact-match allowlist of the
public routes (`/auth/login`, `/auth/register`, `/auth/challenge`,
`/auth/login-keyfile`, `/auth/callback`, `/auth/logout`). Every other `/auth/*`
route is gated. A test asserts an unauthenticated request to each mutating route
gets 401.

### 4. The session cookie loses `Secure` in local-only mode

`secure` is computed solely as `strings.HasPrefix(lower(redirect), "https://")`
(`internal/auth/oidc.go:104`). With no OIDC redirect URL configured that is false,
so in `MN_AUTH=local` the session — which is shell access — would travel over
plain HTTP without the `Secure` flag, and the `SameSite=Lax` CSRF defence weakens
with it.

**Requirement:** `MN_AUTH_COOKIE_SECURE` defaults to **on** in local mode, and is
also inferred from `-tls-cert`/`MN_RELAY_TLS_CERT`. Local mode over plain HTTP
refuses to boot unless `MN_AUTH_INSECURE_COOKIES=1` is set explicitly. (The
deployed relay is fronted by HTTPS via Caddy, so this is satisfied there.)

### 5. Isolation must fail closed

`resolveOwner` returns `("", false)` when no resolver or no session is present, and
the owner checks in `lookup` are guarded by `if filter && …` — so an anonymous
request that ever reaches the relay handlers enumerates every connector and can
proxy to any of them.

**Requirement:** when auth is enabled, an unresolved owner yields `filter = true`
with an account that matches nothing (or a 403) — never "no filtering". Tests
assert anonymous `/api/laptops` is empty and anonymous `/l/<host>/` is refused.

### 6. Disabling an account must revoke its connector too

`defaultConnectorTTL` is 30 days (`internal/auth/oidc.go:33`), and a connector
token is the connector's only credential. Revocation scoped to browser sessions
would leave a disabled account's machine attached — and re-attachable — for a
month.

**Requirement:** the revocation mechanism covers `audConnector` as well as
`audWeb`; disable and delete also drop that owner's live tunnels;
`MN_AUTH_CONNECTOR_TTL` lets an operator shorten the window.

## Revocation: per-account key derivation

`Verify` checks the HMAC and expiry only — it never consults a store, because
until now there was none. So without a change, disabling an account, changing a
password, or deleting an account would not end any existing session.

Local tokens are therefore signed with a per-account key,
`HMAC(secret, account || epoch)`, where `epoch` is a counter in the account
record. Bumping it invalidates every live token for that account — both
audiences — with no change to the `account|exp|aud` wire format, so OIDC sessions
and existing connector tokens are untouched. Verification looks the epoch up in
the in-memory store: O(1), no disk read on the hot path.

Rejected: adding an `epoch` field to the payload (`Verify` parses it with two
`LastIndexByte` splits, so a fourth field changes the format for connector tokens
too).

Considered and deferred: a server-side session table. It gives real per-device
logout, which the epoch does not — but it does not cover connector tokens without
a second mechanism, whereas the epoch covers both. Per-device revocation is a
follow-up, not a v1 requirement.

## Account store

`MN_AUTH_USERS_FILE` (default: alongside the announcements file). JSON, mutex
guarded, written temp-file-then-rename, mode **0600** (not `announce`'s 0644 — this
file holds password hashes).

    Account{
      Account      string   // "local:<canonical>", as signed and as seen by linkKey
      Username     string   // as submitted, for display
      PasswordHash string   // argon2id, encoded with its parameters
      PubKeys      []PubKey // {ID, Alg, Key (SPKI), Label, AddedAt}
      CreatedAt    time.Time
      Disabled     bool
      IsAdmin      bool
      Epoch        uint64
    }

A corrupt or unreadable store is a **fatal** error when local mode is on — never
"start with zero accounts", which would reopen registration of existing names.
Registration is serialised under the store lock so two concurrent requests cannot
both create the same name.

Canonicalisation happens once, on write and on lookup: trim, lowercase, NFKC, and
reject names that are not `[a-z0-9._-]{2,64}` after normalising. Anything that
canonicalises to an existing account is a duplicate.

## Routes

| Route | Public | Purpose |
| --- | --- | --- |
| `GET /auth/login` | yes | HTML page: local form, plus SSO when OIDC is configured |
| `POST /auth/login` | yes | argon2id verify -> session cookie |
| `GET/POST /auth/register` | yes | Create account (404 when closed) |
| `GET /auth/challenge` | yes | Single-use nonce for keyfile login |
| `POST /auth/login-keyfile` | yes | Verify nonce + ECDSA signature -> session |
| `POST /auth/keys` | **no** | Register a public key |
| `DELETE /auth/keys/{id}` | **no** | Revoke a key |
| `POST /auth/password` | **no** | Change password (requires the old one); bumps Epoch |
| `POST /auth/logout-all` | **no** | Bump Epoch |
| `GET /auth/logout` | yes | Clear the cookie |

Every post-login redirect goes through the existing `sameHostRedirect`; `return`
is never interpolated into HTML outside `html/template`.

### Keyfile flows

Enrolment (authenticated): browser `generateKey(ECDSA P-256)`, POSTs only the
public key to `/auth/keys`, then builds the keyfile locally —
PBKDF2-SHA256(passphrase, random salt) -> AES-GCM over the exported PKCS#8 private
key -> JSON download. The private key never reaches the server.

Login: `GET /auth/challenge` returns a challenge blob and the nonce is recorded in
an in-memory single-use set evicted at expiry.

> **As built (v2.31.0), two deliberate deviations.** (1) The challenge is *not*
> `Sign(...)` with a separate audience: it is its own construction
> (`b64(account   nonce   exp).b64(HMAC(challengeDomain+payload))`, TTL 90 s) under
> a third domain prefix. Reusing `Sign` for something handed to unauthenticated
> callers is precisely the token-confusion class that had to be fixed in v2.30.0, and
> a distinct construction makes the mistake unrepresentable rather than merely
> checked. (2) The nonce is consumed *before* the signature is verified, so a captured
> challenge cannot be retried with a different signature. The bounded set (8192) fails
> closed if it ever fills, since evicting a live nonce would make it replayable. The browser
decrypts the keyfile, signs the nonce, and POSTs the signature. The server:

1. verifies the nonce's signature and expiry, and that `aud == audChallenge` (this
   is what stops a challenge being replayed as a session);
2. **consumes** the nonce, rejecting any second use;
3. takes the account from the *signed* blob, never from a form field;
4. verifies the ECDSA signature against a stored public key for that account.

A stolen keyfile is inert without its passphrase; a stolen passphrase is inert
without the file. The single-use set is affordable because the relay is a single
instance that already keeps in-memory rate-limit state.

## Admin

`IsAdmin` currently consults only the env-derived `admins` map
(`internal/auth/middleware.go:222-225`), so "first account becomes admin" would be
a no-op on its own. Admin is the **union** of `MN_AUTH_ADMINS` and the store's
`IsAdmin` flag; the first account registered gets the flag; the last remaining
admin cannot be demoted, disabled, or deleted.

## Lifecycle

- **Disable / delete:** login and `/auth/challenge` responses stay
  indistinguishable from a wrong password; Epoch bumps; live tunnels for that
  owner are dropped, along with its `links` and `seen` entries.
- **Password change:** requires the current password (otherwise one stolen session
  is permanent takeover) and bumps Epoch.
- **Keyfile loss:** no email channel exists, so there is no self-service reset.
  Recovery is the password, or an operator reset (CLI subcommand against the store
  file). Stated plainly in the UI at enrolment.
- **`/auth/logout`** clears the cookie only; `/auth/logout-all` bumps Epoch and
  therefore also detaches that account's connectors — the UI must say so.

## Abuse controls

- Per-IP rate limits on `/auth/login`, `/auth/login-keyfile`, `/auth/register`
  **and `/auth/challenge`** (a nonce-minting oracle otherwise). The client IP is
  `RemoteAddr`; `X-Forwarded-For` is honoured only from a configured trusted-proxy
  CIDR, or the limiter is trivially bypassed.
- Per-account failed-attempt backoff applied **uniformly to unknown accounts** too,
  so the backoff itself is not an enumeration oracle.
- `MN_AUTH_MAX_ACCOUNTS` cap; `MN_AUTH_REGISTRATION=open|closed`.
- argon2id cost as the brute-force floor.
- CSRF: `SameSite=Lax` plus an `Origin`/`Sec-Fetch-Site` check on every mutating
  route.

## Consequences for the operator

- **The connector command changes.** With `SetConnectorAuth` active a connector's
  owner comes from a verified token, so plain `moony -relay=HOST:PORT` is rejected;
  the connector needs the token `/connect` issues.
- **Announcements become admin-only**, where today any visitor can author them.

## Rollout

1. Put `MN_AUTH=local`, `MN_AUTH_SECRET`, `MN_AUTH_USERS_FILE` in an SSM
   SecureString.
2. Re-deploy with `--env-parameter NAME` **and** `--domain`, `--email`,
   `--web-cidr`, `--tunnel-cidr`; omitting them resets those parameters to tool
   defaults, which opens `0.0.0.0/0` and drops HTTPS.
3. Register the owner account first (it becomes admin).
4. Re-launch the connector with its token; confirm the laptop appears.
5. Only then consider widening `WebCidr` beyond the current `/32`.

## Non-goals

Email verification and reset-by-email, account sync across relays, changes to the
OIDC flow, roles beyond the admin flag, per-device session revocation (deferred
with the session table).

## Test plan

Happy paths:

- argon2id hash/verify round-trip; a tampered encoded hash is rejected.
- A known P-256 key signs a real challenge and verifies.
- Store: reload after restart, canonicalisation, duplicate rejection.

Failure paths — each maps to a finding above:

- A local registration cannot collide with an OIDC account string (#1).
- Every unrecognised `MN_AUTH` value is a boot error; `local` and `on,local` start
  correctly (#2).
- Unauthenticated `POST /auth/keys`, `/auth/password`, `/auth/logout-all` all 401
  (#3).
- The session cookie carries `Secure` in local mode; plain HTTP refuses to boot
  without the explicit override (#4).
- Anonymous `/api/laptops` is empty and anonymous `/l/<host>/` is refused (#5).
- After disable, the account's *connector* token is rejected and its live tunnel is
  dropped (#6).
- A replayed challenge is rejected; a challenge signed by another key is rejected;
  an expired challenge is rejected; a challenge cannot be submitted for a different
  account.
- A session stops verifying after password change, disable, and delete.
- A corrupt store file is fatal, not silently empty.
- Concurrent registration of the same name yields exactly one account.
- Rate limits: N failures back off; the account cap refuses registration;
  `/auth/challenge` is limited.
