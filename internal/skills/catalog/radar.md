---
name: radar
description: Query and manage Apple's internal Radar bug tracking system via MCP. Use for counting or listing radars assigned to you, searching by priority/state/component/title, fetching radar details, filing new radars, or updating radar problems.
---

# Radar MCP

Radar tools are exposed by a connected Radar MCP server. In this agent, MCP tool
names are prefixed `mcp.<server>.<tool>` — this skill assumes the server is named
`radar`, so tools are e.g. `mcp.radar.findProblems`. (Adjust the prefix if you
named the server differently.)

> If a tool call fails with "Tool not found" or the server is not connected, the
> Radar MCP isn't wired up yet — see **Setup** below — then retry.

## Setup (one-time, until the token expires)

The Radar MCP server lives at `https://radar-mcp.corp.apple.com/mcp` and needs an
OIDC **access** token scoped to `radar-webservices.apple.com` (a different token
from the model gateway's ID token).

1. Install the bundled `radar` MCP server (writes the `mcp.json` entry with an
   `Authorization: Bearer ${RADAR_MCP_TOKEN}` header that reads the token from
   your environment):

   ```
   /mcps install radar
   ```

   (Or add it by hand to `~/.config/moony/mcp.json` / `./.sqe/mcp.json`:
   `{"mcpServers":{"radar":{"url":"https://radar-mcp.corp.apple.com/mcp","headers":{"Authorization":"Bearer ${RADAR_MCP_TOKEN}"}}}}`.)

2. Mint the access token and export it (macOS):

   ```bash
   export RADAR_MCP_TOKEN=$(moony radar-token)
   ```

3. Reconnect MCP (`/mcps reload`) or restart. The access token is short-lived;
   when calls start returning 401, re-run step 2 and reconnect.

## ⚠️ Common mistakes — read before calling any tool

| Mistake | Correct form |
|---|---|
| `mcp.radar.searchProblems` | **does not exist** — use `mcp.radar.findProblems` |
| `findProblems({title: "foo", limit: 3})` | **wrong** — the filter goes inside `queryJson` as a JSON-encoded string: `findProblems({queryJson: "{\"title\": \"foo\"}", limit: 3})` |
| Setting `priority` or `milestone` in `createProblem` | **rejected with 400** — always a two-step: `createProblem` then `updateProblem` |
| Using `assignee`, `assigneeId`, `assigneeDsid` in create/update body | **rejected** — the only valid key is `assigneeID` (capital `ID`) |
| Sending `substate` when `state` is not `Analyze` | **rejected with 400** — `substate` is only valid alongside `state: "Analyze"` |

## Authoritative rules live on the server — read them before create/update

The server ships its own machine-readable rule files. **When filing or updating a radar, read the relevant rule file first** instead of guessing field names (guessing was historically the #1 source of wasted iterations). Retrieve them with `mcp.radar.getResourceFile` (NOT `get_rulesfile`, which is broken):

```
mcp.radar.getResourceFile  →  { "resource": "create_problem_rules.md" }
mcp.radar.getResourceFile  →  { "resource": "update_problem_rules.md" }
mcp.radar.getResourceFile  →  { "resource": "problem_enum_values.md" }
mcp.radar.getResourceFile  →  { "resource": "find_rules.md" }
```

The parameter key is `resource` (a string). The summaries below capture the essentials, but the rule files are the source of truth if anything conflicts.

## Priority values (memorize — easy to get wrong)

Priority is an integer. The semantic mapping is:

| # | Meaning |
|---|---------|
| 1 | Show Stopper |
| 2 | Expected |
| 3 | Important |
| 4 | Nice to Have |
| 5 | Not Set |

Note: Radar's web UI / history log sometimes prints different labels for the same number. The numbers above are the canonical meanings — set the number, not a label string. "High priority" = `{"any": [1, 2]}`.

## Default: exclude closed radars

Unless the user explicitly asks for closed radars, always add `"state": {"neq": "Closed"}` to every query. The only terminal state is `"Closed"`; all other states (`New Problem`, `Analyze`, `Integrate`, `Build`, `Verify`) are active.

## Find / count radars — `mcp.radar.findProblems`

`queryJson` is a JSON-**encoded** filter object (a string). The token `"me"` is replaced server-side with the current user's DSID — no DSID lookup needed.

Count (bypasses the 500-item cap), set `idsOnly: true`:
```json
{ "queryJson": "{\"assigneeId\": \"me\", \"state\": {\"neq\": \"Closed\"}}", "idsOnly": true }
```

Full objects:
```json
{ "queryJson": "{\"assigneeId\": \"me\", \"state\": {\"neq\": \"Closed\"}}" }
```

| Goal | queryJson value |
|---|---|
| High priority, assigned to me | `{"assigneeId": "me", "priority": {"any": [1, 2]}, "state": {"neq": "Closed"}}` |
| Specific state | `{"assigneeId": "me", "state": "Analyze"}` |
| Title contains word | `{"assigneeId": "me", "title": {"like": "%crash%"}, "state": {"neq": "Closed"}}` |
| Created after date | `{"assigneeId": "me", "createdAt": {"gt": "2024-01-01T00:00:00+0000"}, "state": {"neq": "Closed"}}` |
| Assigned to another person (by DSID) | `{"assigneeId": 2000187654, "state": {"neq": "Closed"}}` |
| Originated by me | `{"originatorId": "me", "state": {"neq": "Closed"}}` |

The `"me"` token works in any DSID field: `assigneeId`, `originatorId`, `resolvedById`, `lastModifiedById`, `driId`, `epmId`, `ccListIndividual`.

> Field-name casing differs by API surface. In **findProblems queries** the assignee field is `assigneeId` (lowercase `d`). In **create/update request bodies** it is `assigneeID` (capital `ID`). This inconsistency is real — don't "fix" it.

## Get the current user's DSID

Only needed when querying for other people by name (to look up their DSID):
```
mcp.radar.getCurrentUserDSID  →  (no parameters)
```

## Get details of one or more radars — `mcp.radar.getProblemByIds`

The parameter is `problemIds`, an **array of integers** (not a string named `ids`):
```
mcp.radar.getProblemByIds  →  { "problemIds": [12345678] }
mcp.radar.getProblemByIds  →  { "problemIds": [12345678, 87654321] }
```

## Discover a component — `mcp.radar.getSuggestions`

Component values reported by `findProblems`/`getProblemByIds` look like `"1019779: Maps Neutron DevOps | Gradle"`. That string is `"<componentID>: <name> | <version>"`. For create you need the `name` and `version` **separately**:
- name = `Maps Neutron DevOps`
- version = `Gradle`

`componentID` is NOT accepted on create — only the `{name, version}` object is. If unsure of exact name/version, use `getSuggestions` or copy them from an existing radar in the same component.

## File a new radar — `mcp.radar.createProblem`

Parameter: `createProblemRequestJson` (a JSON-encoded string).

**Required:** `title`, `description`, `classification`, `component` (object with `name` AND `version`).

**NOT accepted on create** (will 400): `priority`, `milestone`, `componentID`, and the various plausible-but-wrong assignee aliases (`assignee`, `assigneeId`, `assigneeDsid`, `assignTo`, `reassignTo`, ...). The ONLY valid assignee key on create is `assigneeID` (integer DSID) — and even then a newly created radar is assigned to the component's default screener unless you set `assigneeID`.

Minimal working example:
```json
{
  "title": "[my-component] Short imperative summary",
  "classification": "Enhancement",
  "component": { "name": "Maps Neutron DevOps", "version": "Gradle" },
  "description": "Full problem statement...",
  "assigneeID": 973597403
}
```

`classification` enum: `Security`, `Crash/Hang/Data Loss`, `Power`, `Performance`, `UI/Usability`, `Serious Bug`, `Other Bug`, `Feature (New)`, `Enhancement`, `Task`.

`reproducible` enum (optional): `Always`, `Sometimes`, `Rarely`, `Unable`, `I Didn't Try`, `Not Applicable`.

### Recommended filing flow (avoids the create/update split causing surprises)

Because **priority and milestone cannot be set at create time**, filing a fully-specified radar is a two-step operation:

1. `createProblem` with title, description, classification, component, and `assigneeID`. Capture the returned `id`.
2. `updateProblem` to set `priority`, `milestone`, and (if desired) advance `state`/`substate` (see below).

Confirm component name/version with the user (or `getSuggestions`) before step 1 — do not guess components.

## Update a radar — `mcp.radar.updateProblem`

TWO separate parameters (this is the most common mistake):
- `problemId` — integer (the radar id)
- `updateRequestJson` — JSON-encoded string containing ONLY the fields to change

```
mcp.radar.updateProblem  →  {
  "problemId": 12345678,
  "updateRequestJson": "{\"priority\": 2, \"milestone\": {\"id\": 398749}}"
}
```

Common fields inside `updateRequestJson`:
- `title` (string)
- `description` (string) — server auto-prefixes "(Generated by AI) "
- `diagnosis` (string) — the radar "discussion"; auto-prefixed too
- `assigneeID` (integer DSID) — **capital `ID`**; lowercase/other aliases are rejected
- `componentID` (integer)
- `priority` (integer 1–5; must be a number, not a string)
- `state` (string enum) and `substate` (string enum)
- `milestone` (object): `{"id": 398749}` (name optional)
- `resolution`, `classification`, `reproducible` (string enums)
- collection fields (`keywords`, `CC`, `relatedProblems`, …) use `{"insert": [...], "remove": [...]}`

### State / substate constraints (cause 400s if violated)

- `state` enum: `New Problem`, `Analyze`, `Integrate`, `Build`, `Verify`, `Closed`.
- `substate` enum (only valid when state is `Analyze`): `Screen`, `Investigate`, `Fix`, `Review`, `Prepare`, `Nominate`. Do NOT send `substate` when state is anything other than `Analyze`.
- When `substate` is `Investigate`/`Fix`/`Review`/`Prepare`/`Nominate`, **both `milestone` and `priority` (not 5) are required in the same update**, or it fails with `CV.stateRequiresMilestoneAndPriority`.
- Allowed transitions are restricted (e.g. `Analyze → Integrate|Verify` only; `Closed → Analyze|Verify` only). `Analyze → Integrate` additionally requires a "changed" `resolution` (e.g. `Software Changed`) and `priority` 1–4. See `update_problem_rules.md` for the full matrix.
- Resolution `Duplicate` requires `duplicateOfProblemID` and is only valid targeting `Analyze`/`Verify`/`Closed`.

A freshly created radar typically lands in `Analyze` / `Screen`. To move it to `Analyze`/`Investigate` with full metadata, send `state`, `substate`, `milestone`, and `priority` together.

## Quick reference: parameter shapes per tool

| Tool | Key parameter(s) | Shape |
|---|---|---|
| findProblems | `queryJson` (+ `idsOnly`) | JSON string |
| getProblemByIds | `problemIds` | array of ints |
| createProblem | `createProblemRequestJson` | JSON string |
| updateProblem | `problemId`, `updateRequestJson` | int, JSON string |
| getResourceFile | `resource` | string filename |
| getCurrentUserDSID | (none) | — |
| getSuggestions | `searchTerm` | string |
