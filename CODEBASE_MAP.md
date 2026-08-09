# Codebase map

> Maintained map of this project. **Pinned notes** are hand-authored and never overwritten; the **structure** section is generated from the code and auto-refreshed on change; the **narrative** section (features → files) is written on demand via `@map`. The agent reads this before exploring.
>
> Only text INSIDE the `<!-- BEGIN … -->` / `<!-- END … -->` markers is preserved — content added outside them is dropped on the next refresh.

## Pinned notes

<!-- BEGIN PINNED -->
Hand-authored, authoritative notes. Generators never touch this section.

- **Web UI changes need the RELAY rebuilt, not just the connector.** `cmd/relay` serves the
  SPA shell and every static asset (`/` and `/static/`) from its own `//go:embed static`
  copy (`web.SPAHandler`); the `moony` connector serves only the per-session API, proxied
  through the relay. After editing anything under `internal/web/static/`, rebuild **both**
  (`make build` + `make build-relay`), restart the relay, and hard-refresh — a rebuilt
  connector alone will not change the CSS/JS the browser loads. This is why web fixes can
  appear "not applied". Backend/agent changes need only `make build`.
- **Version sync:** `const Version` in `internal/config/config.go` must match the top
  `## [x.y.z]` entry of `CHANGELOG.md` (enforced by `TestChangelogMatchesVersion`). Do the
  bump with `make release` (BUMP=patch/minor/major), never by hand; preview with
  `make release-dry`.
- **CI is gated:** `.github/workflows/ci.yml` only runs its test/windows-build/gofmt jobs
  when the triggering commit message contains "github build" (case-insensitive).
- **Approval gates:** git commit/push and destructive commands deliberately re-confirm on
  every call and ignore "always allow"; `ApprovalRequest.AlwaysDisabled` tells the UI to
  hide that button. Ordinary tools persist allow-rules to `.moony/permissions.json`.
- **Connector coexistence:** one connector per machine. The holder records `pid\nexecutable`
  in `~/.config/<cfg>/connector.lock` and publishes its relay state to `connector.status`;
  standby instances mirror that instead of dialing, or the two evict each other in a
  reconnect loop. Identity is checked from the lock's recorded name, never a hardcoded
  binary name (`go run .` is named `main`, debuggers `__debug_bin*`).
- **`PlanStore.Set` fires the plan_write notify** (forces Status=draft). Host-side mirror
  writes of plan status must use the silent `PlanStore.Sync`, or an approved plan reverts
  to draft and blocks mutating tools on later turns.
- **Per-project dir is `.moony`** (migrated from the older `.sqe`); `.moonyignore` is
  honoured with a `.sqeignore` fallback.
- **This file:** only text inside the BEGIN/END markers survives. The structure section is
  regenerated from the code and must not be hand-edited; write durable knowledge here.
<!-- END PINNED -->

## Structure

<!-- BEGIN AUTO:STRUCTURE -->
Generated from the codebase and refreshed on change. Per directory: the significant source files and each file's key definitions. Tests and build output are omitted. Consult this before exploring; it is ground truth for structure.

**Project:** `moony` · Go · 728 files · 305 source files mapped
**Key files:** go.mod, Makefile, Dockerfile, docker-compose.yml, main.go, README.md, CHANGELOG.md, AGENTS.md

_`kind:line` = where it's declared · `N refs` = files referencing it (`+` = more) · ⭐ = load-bearing file._

### (project root)
- `deploy_cmd.go` — runDeployCmd (func:19), TYPE (type:47), deployUsage (func:34), printStatus (func:129), runDeployAWS (func:62)
- `headless.go` — firstLine (func:148, 5 refs), runHeadless (func:72, 2 refs), summarizeInput (func:139, 2 refs), headlessPrompt (func:20), isFlagPassed (func:56), readPipedStdin (func:158)
- `main.go` ⭐ — Set (func:803, 47+ refs), scheduleList (func:708), standbyRelayState (func:846), daemonInstall (func:767), daemonRun (func:563), daemonSimple (func:551), +11 more

### cmd/relay/ — 2 files
- `main.go` — probeHealth (func:218), acceptConnectors (func:168), browsable (func:196), fatal (func:207)

### cmd/release/ — 2 files
- `main.go` — bumpVersion (func:124), categorize (func:181), changelogEntry (func:157), prependChangelogEntry (func:211), replaceVersionConst (func:148), boolFlag (func:289), +5 more

### cmd/sim-decide/ — 2 files
- `main.go` — turn (type:53, 4 refs), request (type:59, 2 refs), buildUserPrompt (func:159), extractJSONObject (func:230), parseAction (func:212), action (type:67), +2 more

### internal/agent/ — 132 files
_entry points: agent_
- `agent.go` ⭐ — New (func:319, 108+ refs), Run (func:994, 94+ refs), ListModels (func:884, 16 refs), PlanMode (func:681, 15 refs), Reset (func:464, 14 refs), Usage (func:891, 13 refs), +46 more
- `agent_approval.go` — requestApproval (func:96, 3 refs), parallelToolConfig (func:23, 2 refs), requestQuestion (func:130, 2 refs), turnHardStopReason (func:85, 2 refs), requestApprovalOnce (func:104), requestExitPlan (func:195), +3 more
- `agent_effort.go` — capOutput (func:82, 4 refs), appendTurnContext (func:55, 2 refs), autoReasoningEffort (func:238, 2 refs), modelThinkFloor (func:327, 2 refs), passiveEffort (func:313, 2 refs), passiveToolBatch (func:298, 2 refs), +11 more
- `agent_events.go` — Info (type:131, 7 refs), ApprovalRequest (type:167), BackendSwitched (type:137), BoardMessagePosted (type:199)
- `agent_persist.go` — persist (func:14, 11 refs), noteInfo (func:94, 5 refs), NoteInfo (func:112, 3 refs), FlushNotes (func:154, 2 refs), flushPendingNotes (func:128, 2 refs), recordBoardMessages (func:66, 2 refs), +2 more
- `agent_prompt.go` — buildSystemPrompt (func:16, 9 refs), loadAgentsMD (func:287, 3 refs), tildePath (func:333, 2 refs), agentsMDPrompt (func:181), cbmProjectName (func:237), userProfilePrompt (func:152), +5 more
- `agent_tools.go` — parallelSafe (func:273, 5 refs), approveToolCall (func:311, 3 refs), alwaysAllowRule (func:527, 2 refs), bashCommandField (func:561, 2 refs), hardApproval (func:574, 2 refs), needsApproval (func:542, 2 refs), +14 more
- `board.go` — withAgentIdentity (func:127, 4 refs), withBoardAdmin (func:155, 3 refs), post (func:64, 2 refs), registerBoardTools (func:203, 2 refs), withMessageBoard (func:112, 2 refs), agentIdentityFrom (func:134), +14 more
- `btw.go` — AskSide (func:21, 2 refs)
- `checkpoint.go` — onCompacted (func:215, 4 refs), ClearCheckpoint (func:192, 3 refs), WriteCheckpoint (func:100, 3 refs), CheckpointStatus (func:183, 2 refs), checkpointContext (func:170, 2 refs), maybeCheckpoint (func:72, 2 refs), +3 more
- `codebasemap.go` — codebaseMapEnabled (func:28, 2 refs), codebaseMapPromptSection (func:61), clipBytes (func:126)
- `codeoutline.go` — capToolOutput (func:143, 3 refs), outlineReadFile (func:73), isStructural (func:50), lineNum (func:129)
- `compact.go` — Compact (func:51, 6 refs), clip (func:458, 6 refs), calibrateTokens (func:38, 4 refs), pruneStaleToolResults (func:237, 3 refs), serialize (func:430, 3 refs), compactBoundary (func:385, 2 refs), +10 more
- `compact_overflow.go` — compactAndRetry (func:32, 2 refs), compactAtBoundary (func:91, 2 refs), compactedHead (func:113)
- `context.go` — ContextBreakdown (type:24, 6 refs), ContextSegment (type:14), mcpServerName (func:162), memoryFilesSegment (func:96), prefaceTokens (func:199), sortItemsDesc (func:172), +3 more
- `delegate.go` — Workers (func:172, 8 refs), AttachWorkers (func:161, 4 refs), withWorkerActivity (func:31, 2 refs), workerActivityFrom (func:35, 2 refs)
- `destructive.go` — bashEscapesRoot (func:254, 2 refs), destructiveCommand (func:34, 2 refs), recentUserTexts (func:422, 2 refs), userRequestedDestructive (func:463, 2 refs), classifyDestructive (func:41), absPathEscapes (func:327), +14 more
- `difficulty.go` — turnLooksHard (func:17, 2 refs)
- `goal.go` — Goal (func:100, 4 refs), SetGoal (func:82, 4 refs), SetGoalCriteria (func:111, 4 refs), SetGoalBudget (func:145, 3 refs), SplitCriteria (func:133, 3 refs), GoalStatus (func:357, 2 refs), +16 more
- `injection.go` — untrustedContentTool (func:30, 2 refs), wrapUntrustedToolOutput (func:46, 2 refs)
- `jobstart.go` — StartBackgroundJobApproved (func:34, 3 refs), ApprovalPolicyName (func:80, 2 refs), BackgroundJobNeedsApproval (func:95, 2 refs), SendJobInput (func:60, 2 refs), gateBackgroundJob (func:119, 2 refs)
- `jsonshrink.go` — compactJSON (func:27, 2 refs), pruneJSON (func:44, 2 refs), isEmptyJSON (func:93), looksLikeJSON (func:19), pruneEmpty (func:67)
- `memory.go` — RefreshSystemPrompt (func:299, 7 refs), Remember (func:190, 5 refs), AutoCaptureRule (func:227, 3 refs), ParseMemoryFlags (func:732, 3 refs), systemPrompt (func:287, 3 refs), AddBullet (func:399, 2 refs), +40 more
- `mentions.go` — expandFileMentions (func:339), expandMentionDirective (func:158), expandNotesMention (func:91), expandTurnMentions (func:57), mentionKind (type:33), notesDirective (func:108), +8 more
- `plan.go` — Plan (func:35, 9 refs), AttachPlanStore (func:26, 6 refs), ApprovePlan (func:92, 4 refs), SetPlan (func:72, 4 refs), hasDraftPlan (func:131, 4 refs), ClearPlan (func:106, 3 refs), +16 more
- `profile.go` — loadProjectProfile (func:76, 2 refs), maybeRefreshProfile (func:109, 2 refs), profileSlug (func:49), projectProfilePath (func:69), projectProfileSection (func:91), writeProjectProfile (func:138)
- `quotafallback.go` — MaybeQuotaFallback (func:259, 3 refs), QuotaFallbackInfo (func:450, 3 refs), MaybeQuotaRestore (func:372, 2 refs), ParseQuotaResetTime (func:538, 2 refs), QuotaFallbackSnapshot (func:113, 2 refs), RestoreQuotaFallback (func:123, 2 refs), +20 more
- `rehome.go` — SetWorkingDir (func:27, 4 refs)
- `relevance.go` — goalTerms (func:52, 3 refs), lastUserText (func:30, 2 refs), relevanceScore (func:65, 2 refs), relevantExcerpt (func:82, 2 refs)
- `repeat.go` — looksLikeCorrection (func:74, 2 refs), looksLikeRepeatedAsk (func:92, 2 refs), userMessageTextHelper (func:127)
- `scoped.go` — RunScoped (func:60), ScopedOptions (type:22), ScopedResult (type:49)
- `selfreview.go` — TurnChangesDiff (func:46, 4 refs), maybeSelfReview (func:211, 2 refs), shortModelName (func:286, 2 refs), clipTo (func:120), parseReview (func:185), reviewableDiff (func:41), +6 more
- `selfreview_gate.go` — turnIsNonReviewable (func:171, 2 refs), changedRelPaths (func:41), matchesSkipGlob (func:87), selfReviewSkipGlobs (func:68), versionBumpOnly (func:134), fileChangeIsVersionBump (func:203)
- `skill.go` — AttachSkills (func:109, 8 refs), newSkillTool (func:24), skillTool (type:19)
- `snapshot.go` — SnapshotPath (func:198, 6 refs), pop (func:154, 6 refs), Undo (func:234, 5 refs), startTurn (func:39, 5 refs), mutatedThisTurn (func:50, 4 refs), ModifiedFiles (func:219, 3 refs), +17 more
- `task.go` — AttachSubAgents (func:720, 9 refs), newTaskTool (func:46, 3 refs), clearTaskCache (func:566, 2 refs), contextItems (func:601, 2 refs), totals (func:820, 2 refs), ResolveModel (func:576), +23 more
- `taskstore.go` — newTaskStore (func:126, 3 refs), appendOutput (func:62, 2 refs), finalResult (func:107, 2 refs), handles (func:167)
- `tasktools.go` — decodeHandle (func:22), handleInput (type:18), taskCancelTool (type:128), taskOutputTool (type:49)
- `taskworktree.go` — cleanup (func:164, 5 refs), diff (func:142, 3 refs), isGitWorktreeRepo (func:60, 2 refs), newIsolatedWorktree (func:74, 2 refs), pruneStaleTaskWorktrees (func:192), sanitizeWorktreeLabel (func:258), +4 more
- `testgen.go` — maybeSuggestTests (func:42, 2 refs), eligibleTestTargets (func:81), renderTestSuggestion (func:220), siblingTestPath (func:128), draftOneTest (func:175), isExcludedPath (func:160), +2 more
- _… 7 more files_

### internal/announce/ — 2 files
- `announce.go` — normalizeLevel (func:136), saveLocked (func:118), sortLocked (func:114), wire (type:40)

### internal/app/ — 1 file
- `app.go` — BuildServer (func:32, 2 refs), app (package:5)

### internal/attach/ — 5 files
- `attach.go` — FromBytes (func:97, 4 refs), FromPath (func:80, 4 refs), Attachable (func:67, 3 refs), Block (func:196, 3 refs), Attachment (type:40), Kind (type:22), +2 more
- `iwork.go` — IsIWork (func:71, 2 refs), convertIWork (func:79, 2 refs), converterURL (func:61)
- `office.go` — attach (package:1), extractOffice (func:17), collapseBlankLines (func:125), xmlRunText (func:91)

### internal/auth/ — 4 files
- `middleware.go` — FromEnvBackground (func:270), IsAdmin (func:222), OwnerFromRequest (func:219), VerifyConnectorToken (func:229), Wrap (func:28), openBlob (func:241), +11 more
- `oidc.go` — enabled (func:38, 2 refs), FromEnv (func:51), accountFromClaims (func:128), sameHostRedirect (func:140), Auth (type:17), auth (package:1)
- `token.go` — now (func:95, 6 refs), Sign (func:40, 2 refs), Verify (func:49, 2 refs), mac (func:88)

### internal/awsdeploy/ — 9 files
- `assets.go` — applyPath (func:54, 2 refs), Asset (func:30), AssetNames (func:39), mustAsset (func:43)
- `awscli.go` — advise (func:85, 2 refs), classify (func:61, 2 refs), aws (func:35), Problem (type:42), execRunner (func:19), runner (type:17)
- `deploy.go` — Delete (func:448, 11 refs), Status (type:404, 6 refs), logf (func:73, 6 refs), Deploy (func:120, 3 refs), Options (type:17), Deployer (type:53), +9 more
- `ssm.go` — convergeScript (func:27, 2 refs), sendCommandInput (func:48, 2 refs), writeTempJSON (func:69, 2 refs), shellQuote (func:96)

### internal/bridge/ — 2 files
- `hub.go` — Command (type:48, 23 refs), PublishEvent (func:309, 5 refs), PublishState (func:323, 4 refs), PublishUser (func:316, 4 refs), SetActiveSession (func:262, 4 refs), SetController (func:232, 4 refs), +28 more

### internal/budget/ — 2 files
- `budget.go` — DailyBudget (func:30, 2 refs), MonthlyBudget (func:31, 2 refs), Record (func:56, 2 refs), envUSD (func:33)

### internal/config/ — 43 files
_entry points: config, prefs, paths_
- `agent.go` — Rule (func:43, 5 refs), BuildAgent (func:80, 4 refs), BuiltinAgent (func:105, 2 refs), PlanAgent (func:87, 2 refs)
- `approval.go` — NormalizeApprovalPolicy (func:30, 12 refs), SafeBashCommand (func:77, 2 refs), safeSimpleCommand (func:109)
- `codebasemap.go` — ReadCodebaseMap (func:52, 4 refs), WriteCodebaseMapStructure (func:142, 4 refs), CodebaseMapNarrative (func:84, 3 refs), CodebaseMapPinned (func:93, 3 refs), CodebaseMapStructure (func:76, 3 refs), section (func:61, 3 refs), +5 more
- `config.go` ⭐ — Load (func:1641, 103+ refs), Config (type:15, 20 refs), SetModel (func:763, 18 refs), NormalizeReasoningFor (func:1373, 8 refs), NormalizeReasoning (func:1123, 7 refs), ReasoningLevelsFor (func:1064, 7 refs), +57 more
- `context.go` — ContextWindowFor (func:57, 8 refs), ParseTokenCount (func:67, 5 refs), ContextWindow (func:47, 4 refs)
- `dotenv.go` — UpsertDotEnv (func:107, 4 refs), LoadDotEnv (func:28, 3 refs), DotEnvValues (func:180), parseEnvLine (func:67), PersistentDotEnvPath (func:93), dotEnvCandidates (func:36), +2 more
- `fallbackchain.go` — ParseFallbackChain (func:84, 3 refs), FallbackChain (func:150, 2 refs), FallbackChainProviders (func:134, 2 refs), FallbackChainSpec (func:124), FallbackChainSpecs (func:159), StrictReasoning (func:28), +3 more
- `headers.go` — ParseCustomHeaders (func:37, 2 refs), CustomHeader (type:6), isHeaderName (func:118), isHeaderNameChar (func:12), isSpace (func:115), splitRunTogether (func:71)
- `host.go` — MachineName (func:22, 6 refs), normalizeHostname (func:41), computeMachineName (func:27)
- `paths.go` ⭐ — GlobalConfigPath (func:41, 25+ refs), ProjectPath (func:211, 19 refs), GlobalConfigDir (func:25, 17 refs), MigrateProjectDirVerbose (func:296, 2 refs), MigrateRenamedConfigDir (func:88, 2 refs), ProjectPathNew (func:224, 2 refs), +6 more
- `prefs.go` ⭐ — Save (func:95, 43+ refs), loadPrefs (func:77), prefsPath (func:71), savedPrefs (type:18)
- `pricing.go` — CostUSD (func:142, 10 refs), OverlayRuntimePricing (func:102, 6 refs), CostUSDFull (func:170, 5 refs), ClearRuntimePricing (func:117, 4 refs), CostUSDFullFallback (func:188, 2 refs), PriceFor (func:126, 2 refs), +5 more
- `project_notes.go` — ProjectNotesFor (func:148, 3 refs), SetProjectNotes (func:119, 3 refs), LoadProjectNotes (func:29, 2 refs), SaveProjectNotes (func:64), canonicalProjectDir (func:103), loadProjectNotesLocked (func:35), +1 more
- `prompttoolong.go` — PromptTooLong (func:27, 2 refs), PromptTooLongNoteFor (func:65, 2 refs), FormatTokenCount (func:196), PromptTooLongNote (func:59), largerContextModels (func:146), ModelDisplayName (func:118), +2 more
- `sandbox.go` — NormalizeSandboxMode (func:16, 7 refs), SandboxLabel (func:26, 5 refs)
- `seed_agents.go` — SeedGlobalAGENTS (func:24, 2 refs)
- `verifydetect.go` — detectVerifyCommand (func:20, 2 refs), DetectVerifyCommand (func:130), fileContains (func:120), makefileCheckTarget (func:75), npmHasTestScript (func:98)
- `worktreerepair.go` — RepairWorktrees (func:29, 2 refs)

### internal/daemon/ — 14 files
- `connector.go` — TryHoldConnector (func:67, 5 refs), connectorHeldByOther (func:130, 4 refs), ConnectorStatus (func:336, 3 refs), PublishConnectorStatus (func:314, 3 refs), connectorLockPath (func:22, 3 refs), sameExecName (func:231, 2 refs), +10 more
- `daemon.go` — wait (func:167, 3 refs), Serve (func:58, 2 refs), relayAddrFor (func:44), checkAndSelfUpdate (func:189), daemon (package:11), dialRelay (func:161)
- `proclive_windows.go` — processAlive (func:24, 3 refs)
- `procname.go` — processName (func:23, 2 refs), lookupProcessName (func:29), procfsProcessName (func:48), psProcessName (func:70)
- `procname_windows.go` — platformProcessName (func:20, 2 refs)
- `scheduler.go` — runScheduler (func:21)
- `service.go` — Start (func:120, 18 refs), Stop (func:146, 8 refs), launchdPlistPath (func:239), renderLaunchd (func:272), renderSystemd (func:335), servicePATH (func:359), +12 more

### internal/dap/ — 5 files
- `dap.go` — Dial (func:237, 5 refs), Clear (func:583, 2 refs), DrainOutput (func:381, 2 refs), WaitStopped (func:324, 2 refs), ClearStopped (func:373), Terminated (func:390), +27 more
- `spawn.go` — Spawn (func:49, 4 refs), listenAddr (func:70), SpawnOptions (type:36), annotate (func:205), dap (package:17), killProcess (func:180), +4 more

### internal/diff/ — 2 files
- `diff.go` — Unified (func:43, 4 refs), Stats (type:18, 3 refs), collapse (func:126, 3 refs), coarse (func:169), lcsDiff (func:81), opKind (type:27)

### internal/doctor/ — 2 files
- `doctor.go` — Check (type:53, 11 refs), CheckEmbeddings (func:468), checkHooks (func:294), checkSandbox (func:400), checkSubagentsAndCommands (func:310), Counts (func:70), +15 more

### internal/domains/ — 2 files
- `domains.go` — EnsureDefaults (func:72, 3 refs), HostFromURL (func:138, 3 refs), normalizeDomain (func:183), hostMatches (func:202), normalizeHost (func:173)

### internal/eval/ — 3 files
- `eval.go` — MetricsFromTurnMetas (func:54, 2 refs), Passed (func:158, 2 refs), Scorecard (func:192, 2 refs), Total (func:169, 2 refs), TotalCostUSD (func:172), TotalSteps (func:181), +4 more
- `fixtures.go` — Fixtures (func:8, 2 refs), calc (module:14), doesn (package:23), greet (module:25)

### internal/format/ — 2 files
- `format.go` — FormatFile (func:133, 2 refs), format (package:17), rawFormatter (type:37)

### internal/githost/ — 6 files
_entry points: github_
- `auth.go` — Token (func:34, 3 refs), resolveToken (func:45)
- `githost.go` — Detect (func:137, 11 refs), CheckRun (type:98), Comment (type:84), CreatePROpts (type:55), HostKind (type:16), ListPROpts (type:64), +2 more
- `github.go` ⭐ — decode (func:48, 36+ refs), CheckStatus (func:239, 5 refs), CreatePR (func:93, 5 refs), GetPR (func:141, 5 refs), ListPRs (func:108, 5 refs), ListReviewComments (func:169, 5 refs), +7 more
- `gitlab.go` — newGitLabClient (func:22, 2 refs), gitlabClient (type:16), glDiscussion (type:169), glMR (type:65), glNote (type:154), glStatus (type:223), +1 more

### internal/hooks/ — 5 files
- `hooks.go` ⭐ — Count (func:110, 27+ refs), Empty (func:107, 18 refs), Context (type:67, 14 refs), log (func:254, 6 refs), Event (type:57, 4 refs), loadFile (func:262, 3 refs), +6 more

### internal/llm/ — 41 files
_entry points: types_
- `anthropic.go` — anthropicKey (func:61, 4 refs), usesAnthropic (func:39, 4 refs), anthropicBaseURL (func:46, 3 refs), listAnthropicModels (func:422, 2 refs), anthropicAdaptiveThinking (func:175), anthropicEffort (func:183), +18 more
- `bedrock.go` — usesBedrock (func:39, 4 refs), bedrockBody (func:113), checkBedrockAuth (func:208), listBedrockModels (func:47), streamBedrock (func:130), bedrockAWSConfig (func:72), +4 more
- `client.go` — NewClient (func:43, 19 refs), WithModel (func:216, 5 refs), modelFrom (func:224, 5 refs), Client (type:23, 4 refs), StreamMessagesNoThink (func:168, 4 refs), WithProvider (func:245, 4 refs), +26 more
- `copilot.go` — usesCopilot (func:33, 3 refs), checkCopilotAuth (func:250), listCopilotModels (func:260), streamCopilot (func:75), copilotEffort (func:192), copilotTurn (type:207), +5 more
- `embed.go` — doEmbedRequest (func:93), embedOllama (func:47), embedRemote (func:70)
- `embedcache.go` — Len (func:232, 19 refs), load (func:142, 5 refs), NewEmbedCache (func:53, 2 refs), key (func:59), EmbedCache (type:24), cacheEntry (type:36), +2 more
- `imagegen.go` — GenerateImage (func:47, 3 refs), decodeDataURL (func:198), GeneratedImage (type:25), ImageRef (type:19), decodeBase64Image (func:186), oaiRespAudio (type:175), +1 more
- `models.go` — ModelServableBy (func:23, 6 refs), knownModelName (func:97, 2 refs), ListImageModels (func:113), humanizeModelID (func:178), isImageGenModel (func:137), isNonChatModel (func:155), +1 more
- `ollama.go` — userAgent (func:107, 6 refs), checkOllamaAuth (func:219, 2 refs), listOllamaModels (func:174, 2 refs), ollamaBaseURL (func:38, 2 refs), ollamaModelResident (func:327), ollamaNumCtx (func:88), +11 more
- `ollama_native.go` — toOllamaMessages (func:62), ollamaNativeChunk (type:122), ollamaNativeMsg (type:39), ollamaNativeReq (type:29), ollamaNativeTC (type:47), ollamaNativeTCFn (type:53), +2 more
- `openai.go` — dbg (func:29, 4 refs), toOpenAIMessages (func:178, 3 refs), dataURI (func:272, 2 refs), toOpenAITools (func:280, 2 refs), parseOpenAIStream (func:333), oaiCacheControl (type:86), +17 more
- `quota_providers.go` — ProviderQuotaSupported (func:71), ProvidersWithQuota (func:85), parseDeepInfraUsage (func:344), parseDeepSeekUsage (func:268), parseGenericBalance (func:404), parseMoonshotUsage (func:303), +13 more
- `quotaerr.go` — quotaStopError (func:91, 2 refs), redactSecrets (func:125, 2 refs), isHardQuotaExceeded (func:68), Unwrap (func:114), quotaExhaustedError (type:111)
- `reachable.go` — ProviderReachable (func:27, 2 refs)
- `remote_openai.go` — remoteBackendFor (func:85, 5 refs), usesRemote (func:68, 3 refs), checkRemoteAuth (func:363, 2 refs), remoteBackend (type:26, 2 refs), setAuth (func:336, 2 refs), chatURL (func:301), +10 more
- `streamer.go` — Streamer (type:13)
- `tokens.go` — MessageChars (func:120, 3 refs), CharsPerToken (func:82, 2 refs), Estimate (func:65, 2 refs), Observe (func:47, 2 refs), TokenEstimator (type:21), fit (func:93)
- `toolname.go` — prepareToolNames (func:67, 2 refs), restoreToolNames (func:149, 2 refs), CleanToolName (func:50), wireToolName (func:29), wrapToolUseHandler (func:165), addSuffixlessAliases (func:113), +1 more
- `types.go` ⭐ — TextBlock (func:201, 46+ refs), UserText (func:228, 35+ refs), ToolResultBlock (func:206, 9 refs), ImageBlock (func:183, 8 refs), DocumentBlock (func:192, 3 refs), ToolCalls (func:322, 3 refs), +16 more
- `usage.go` — HasBalance (func:45, 2 refs), IsProject (func:110), UsageBudget (type:55), UsageData (type:88), UsageModelData (type:99), UsageModelQuota (type:71), +4 more
- `videogen.go` — GenerateVideo (func:69, 3 refs), str (func:238, 2 refs), isVideoGenModel (func:254), GeneratedVideo (type:19), ListVideoModels (func:269), VideoOptions (type:43), +7 more

### internal/lsp/ — 8 files
- `catalog.go` — InstallArgv (func:24, 2 refs), checker (type:32)
- `edit.go` — readRawCfg (func:67), writeRawCfg (func:77)
- `lsp.go` — write (func:300, 7 refs), handshake (func:164, 6 refs), pathToURI (func:457, 3 refs), HandlesExtension (func:189, 2 refs), NotifyChange (func:196, 2 refs), notify (func:295, 2 refs), +12 more
- `manager.go` — Running (func:163, 7 refs), Handles (func:148, 3 refs), DiagnosticsFor (func:187, 2 refs), formatDiagnostic (func:211), startOne (func:136)
- `nav.go` — Definition (func:30, 4 refs), Hover (func:50, 4 refs), References (func:39, 4 refs), Location (type:24, 2 refs), Range (type:17), hoverText (func:113), +6 more

### internal/manage/ — 2 files
- `manage.go` — Filter (func:89, 12 refs), SkillEnabled (func:67, 9 refs), AgentEnabled (func:77, 8 refs), MCPEnabled (func:72, 8 refs), LSPEnabled (func:82, 5 refs), SetMCP (func:108, 5 refs), +1 more

### internal/mcp/ — 13 files
- `adapter.go` — ConnectAll (func:279, 6 refs), Failures (func:237, 5 refs), Servers (func:229, 5 refs), RegisterTools (func:338, 4 refs), Failure (func:256, 3 refs), Prompts (func:198, 3 refs), +13 more
- `client.go` — CallTool (func:313, 2 refs), GetPrompt (func:289, 2 refs), ListPrompts (func:269, 2 refs), ListResources (func:225, 2 refs), ListTools (func:209, 2 refs), ReadResource (func:244, 2 refs), +16 more
- `config.go` — LoadConfigs (func:76, 23 refs), Enabled (func:292, 9 refs), readConfig (func:302, 2 refs), ReferencedEnvVars (func:147), claudeConfig (type:55), claudeDiscoveryEnabled (func:267), +5 more
- `edit.go` — AddServer (func:18, 10 refs), RemoveServer (func:43, 9 refs), ParseAddArgs (func:64, 3 refs), sqeConfigPath (func:94), readRawConfig (func:101), writeRawConfig (func:111)
- `transport.go` — Send (func:84, 13 refs), readLoop (func:117, 6 refs), Receive (func:91, 2 refs), drainStderr (func:284, 2 refs), newHTTPSSETransport (func:155), newStdioTransport (func:48), +4 more

### internal/netrc/ — 2 files
- `netrc.go` — Parse (func:63, 18 refs), Bearer (func:108, 3 refs), Path (func:31), Entry (type:20), File (type:27), netrc (package:11)

### internal/notify/ — 2 files
- `notify.go` — MinSeconds (func:37), WebhookURL (func:32), enabledEvents (func:51), postWebhook (func:88)

### internal/permissions/ — 3 files
- `permissions.go` — Allowed (func:336, 5 refs), Denied (func:339, 4 refs), Malformed (func:346, 3 refs), Lists (func:350), coveredByPrefix (func:299), exactCommandMatch (func:203), +8 more

### internal/plugins/ — 5 files
- `catalog.go` — componentCount (func:76, 2 refs)
- `marketplace.go` — InstallFromMarketplace (func:367, 5 refs), MarketplacePlugins (func:292, 5 refs), AddMarketplace (func:192, 4 refs), Marketplaces (func:151, 4 refs), RemoveFromMarketplace (func:463, 4 refs), RemoveMarketplace (func:242, 4 refs), +36 more
- `plugins.go` ⭐ — Remove (func:168, 49+ refs), Install (func:86, 14 refs), StatusAll (func:32, 6 refs), fileExists (func:256, 3 refs), commandExists (func:210), commandsBase (func:202), +3 more

### internal/procutil/ — 3 files
- `procutil_unix.go` — Terminate (func:40, 6 refs), Detach (func:29, 5 refs), KillTree (func:69, 5 refs), procutil (package:17)

### internal/provider/ — 2 files
- `provider.go` ⭐ — List (func:169, 32+ refs), Normalize (func:197, 26+ refs), Lookup (func:176, 16 refs), DefaultModelFor (func:270, 15 refs), Label (func:188, 15 refs), UsesAnthropicWire (func:208, 6 refs), +5 more

### internal/quickactions/ — 2 files
- `quickactions.go` — read (func:457, 10 refs), indexOf (func:344, 6 refs), EnsureGlobalDefaults (func:132, 5 refs), normalize (func:355, 3 refs), EffectiveScope (func:223, 2 refs), ListScope (func:231, 2 refs), +10 more

### internal/rag/ — 2 files
- `store.go` — Hash (func:75, 2 refs), Prune (func:94, 2 refs), Put (func:85, 2 refs), Search (func:126, 2 refs), ContentHash (func:208), Chunk (type:19), +4 more

### internal/relay/ — 6 files
- `connector.go` — ServeConnector (func:28, 3 refs), tunnelRW (type:93), writeHeadLocked (func:105)
- `download.go` — baseURL (func:28, 2 refs), flash (func:283), serveDownloadFile (func:40), serveDownloadPage (func:94), serveInstallScript (func:81), fallback (func:284), +2 more
- `protocol.go` — frame (type:45, 3 refs), newTunnel (func:68, 2 refs), readFrame (func:86, 2 refs), writeFrame (func:70, 2 refs), frameType (type:29), tunnel (type:62)
- `relay.go` — push (func:138, 11 refs), ServeHTTP (func:399, 13 refs), Connected (func:240, 4 refs), SetDownloads (func:217, 3 refs), SetTunnelAddr (func:223, 3 refs), SetVersion (func:211, 3 refs), +23 more

### internal/schedule/ — 4 files
- `schedule.go` ⭐ — Open (func:231, 27+ refs), OpenAt (func:240, 7 refs), ParseSpec (func:154, 5 refs), Store (type:228, 5 refs), Describe (func:93, 4 refs), FormatList (func:130, 4 refs), +13 more
- `schedule_unix.go` — lockFile (func:13, 2 refs), unlockFile (func:18, 2 refs)

### internal/selfupdate/ — 2 files
- `selfupdate.go` — LatestVersion (func:78, 4 refs), Announcement (type:101), Announcements (func:112), AssetName (func:72), SetHTTPClientForTest (func:42), checksumFor (func:196), +3 more

### internal/session/ — 6 files
- `backend.go` — Backend (type:11)
- `crypto.go` — isEncrypted (func:220, 2 refs), decodeKey (func:152), decrypt (func:250), encrypt (func:226), resolveCrypto (func:76), verifyKey (func:285), +7 more
- `session.go` ⭐ — Dir (func:277, 70+ refs), Rename (func:369, 28+ refs), NewStoreAt (func:262, 19 refs), ListByDir (func:397, 6 refs), Encrypted (func:474, 5 refs), Latest (func:412, 4 refs), +12 more

### internal/skills/ — 9 files
- `catalog.go` — Catalog (func:73, 16 refs), CatalogEntryByName (func:76, 16 refs), SkillMarkdown (func:86, 2 refs), CatalogEntry (type:65), skills (package:1)
- `edit.go` — renderSkill (func:50), skillsBase (func:133)
- `skills.go` — parseFrontmatter (func:166), Skill (type:23), readSkillDir (func:128)

### internal/subagents/ — 7 files
- `catalog.go` — Markdown (func:81, 2 refs), readOnly (func:25, 2 refs), BuiltinPersonas (func:52)
- `edit.go` — Create (func:43, 7 refs), Scaffold (func:15, 5 refs), SeedGlobalDefaultAgents (func:168, 4 refs), MaterializeExternals (func:208, 2 refs), SetProvider (func:262, 2 refs), SetReasoning (func:270, 2 refs), +7 more
- `subagents.go` — SubagentCapable (func:77, 5 refs), EnvVars (func:428, 4 refs), ReadOnly (func:204, 4 refs), Builtin (func:65, 2 refs), PrimaryCapable (func:70, 2 refs), ToAgent (func:86, 2 refs), +11 more

### internal/taskboard/ — 3 files
- `taskboard.go` ⭐ — Get (func:189, 53+ refs), finish (func:409, 8 refs), Apply (func:203, 7 refs), Discard (func:241, 2 refs), Enqueue (func:149, 2 refs), newBoard (func:101), +9 more
- `worktree.go` — applyPatch (func:107), createWorktree (func:51), gitDiff (func:95), removeWorktree (func:79), worktree (type:35), excludeFromStatus (func:121), +1 more

### internal/tools/ — 108 files
_entry points: files, tool, jobs_
- `ask.go` — WithUserAsker (func:61, 2 refs), WithUserBatchAsker (func:87, 2 refs), AskOption (type:30), AskRequest (type:39), UserAsker (type:53), UserBatchAsker (type:80), +11 more
- `bash.go` — truncate (func:285, 14 refs), stripRedundantLeadingCD (func:145), bashKillTool (type:233), bashOutputTool (type:174), bashTool (type:15), snapHeadToLine (func:301), +1 more
- `browser.go` — appendConsole (func:238), takeConsole (func:252), browserInput (type:69), browserTool (type:28), chromeExecPath (func:295), ensureStarted (func:191), +3 more
- `codebasemap.go` — CodebaseMapStructural (func:115, 4 refs), CodebaseMapDigest (func:134, 3 refs), CodebaseMapForDir (func:185, 2 refs), CodebaseMapDirs (func:201), cbmFile (type:99), cbmMeta (type:210), +8 more
- `codebasemap_tool.go` — codebaseMapInput (type:36), codebaseMapTool (type:18)
- `codenav.go` — SetLSP (func:29, 7 refs), locateSymbol (func:168), relOrAbs (func:148), utf16Len (func:235), wholeWordCols (func:194)
- `dap.go` — call (func:485, 9 refs), info (type:751, 6 refs), RegisterDAP (func:16, 2 refs), dapAttachTool (type:220), dapConnectTool (type:125), dapContinueTool (type:411), +19 more
- `defaults.go` ⭐ — Default (func:107, 30+ refs), RegisterDefaults (func:5, 12 refs), RegisterTodo (func:96, 4 refs), RegisterPlan (func:102, 3 refs), RegisterAllowed (func:52, 2 refs)
- `env.go` — scrubbedEnvFor (func:86, 4 refs), ScrubbedEnv (func:105, 2 refs), scrubbedEnv (func:66)
- `exit_plan.go` — WithExitPlanConfirmer (func:39), ExitPlanConfirmer (type:31), ExitPlanRequest (type:22), exitPlanConfirmerFrom (func:45)
- `filepicker.go` — queryTerms (func:97), acc (type:129), containsTerm (func:195), fileRank (type:121), findFilesInput (type:45), findFilesTool (type:25), +3 more
- `files.go` ⭐ — Execute (func:52, 92+ refs), Name (func:32, 77+ refs), Mutates (func:162, 63+ refs), Schema (func:40, 56+ refs), Description (func:34, 52+ refs), Preview (func:291, 11 refs), +18 more
- `git.go` — runGit (func:18, 5 refs), GitInfo (func:52, 4 refs), GitAheadBehind (func:77, 3 refs), GitCommitChanges (func:321, 2 refs), GitCommitDiffFor (func:237, 2 refs), GitDiffFor (func:224, 2 refs), +21 more
- `github.go` — parse (func:116, 8 refs), looksFailed (func:306), ciStatusTool (type:226), currentBranch (func:61), failedRunLogs (func:313), firstArg (func:52), +3 more
- `ignore.go` — Match (func:208, 10 refs), LoadIgnore (func:86, 3 refs), IgnoreSet (type:78), addScoped (func:187), appendFile (func:99), appendFileScoped (func:168), +5 more
- `image_gen.go` — NewImageGenTool (func:34, 2 refs), mediaTypeFor (func:141), ImageGenerator (type:18), extFor (func:155)
- `jobs.go` ⭐ — add (func:193, 40+ refs), get (func:231, 14 refs), snapshot (func:137, 14 refs), start (func:437, 12 refs), list (func:248, 10 refs), newID (func:238, 7 refs), +11 more
- `jobs_unix.go` — configureProcess (func:21, 2 refs)
- `jobscope.go` — JobStoreFor (func:39, 2 refs), KillAllProjectJobs (func:84, 2 refs), jobStoreKey (func:61)
- `jobspersist.go` — save (func:87, 13 refs), restore (func:178, 4 refs), record (func:152, 3 refs), isRestored (func:253, 2 refs), jobSeqOf (func:264), jobRecord (type:61), +2 more
- `jobsview.go` — Jobs (func:101, 9 refs), KillJob (func:143, 5 refs), JobTail (func:129, 4 refs), StartBackgroundJob (func:171, 4 refs), Job (func:114), JobInfo (type:18), +2 more
- `jobtty.go` ⭐ — Write (func:199, 40+ refs), Flush (func:295, 12 refs), newPTY (func:54, 2 refs), runTTY (func:67), tools (package:1), ttyFilter (type:178), +2 more
- `lint.go` — findMarker (func:153), orDot (func:85), planLintRuns (func:107), dedupLinterRuns (func:167), lintTool (type:19), linterRun (type:93), +1 more
- `lsp.go` — RegisterLSP (func:21, 2 refs), formatLocations (func:63), resolveLSPTarget (func:43), lspDefinitionTool (type:95), lspDiagnosticsTool (type:200), lspHoverTool (type:181), +4 more
- `memory.go` — NewMemorySink (func:26, 4 refs), RegisterRemember (func:105, 3 refs), RegisterRememberUser (func:113, 2 refs), SetList (func:34, 2 refs), SetSave (func:30, 2 refs)
- `mobile.go` — ExecuteImages (func:93, 7 refs), androidEscapeText (func:428), adbArgs (func:266), android (func:255), androidScreenshot (func:356), androidText (func:273), +9 more
- `patch.go` — applyUpdate (func:229), parsePatch (func:64), isPatchHeader (func:150), parseHunks (func:158), patchHunk (type:49), patchKind (type:40), +1 more
- `plan.go` — NewPlanStore (func:36, 7 refs), Sync (func:67, 3 refs), PlanStore (type:29), clonePlan (func:80)
- `pr.go` — resolve (func:57, 22 refs), detectDefaultBase (func:38), draftSuffix (func:140), formatPRDetail (func:215), parseInput (func:83), prChecksTool (type:344), +4 more
- `proc.go` — configureProcessGroup (func:16, 2 refs)
- `progress.go` — ProgressFrom (func:20, 2 refs), WithProgress (func:14, 2 refs), ProgressFn (type:8), progressCtxKey (type:10)
- `recall.go` — rankNotesBM25 (func:109), noteEmbedText (func:224), recallTool (type:31), scored (type:145), scoredNote (type:101)
- `repomap.go` — RepoMap (func:39, 3 refs), detectStack (func:200, 2 refs), presentMarkers (func:189), walkTreeCounts (func:102), computeRepoMap (func:50), lastSeg (func:164), +2 more
- `review_changes.go` — reviewChangesTool (type:16)
- `sandbox.go` — sandboxCommand (func:45, 3 refs), canonical (func:164, 2 refs), bwrapArgs (func:136), sandboxCommandLinux (func:73), sandboxEnabledEnv (func:21), sandboxNetworkEnv (func:31), +4 more
- `search.go` — isBinary (func:1176, 3 refs), includeMatch (func:1032, 2 refs), parseIncludes (func:987, 2 refs), search (func:365, 2 refs), symbolDefRegexp (func:1151, 2 refs), buildRgArgs (func:784), +36 more
- `searchcode.go` — semanticRerank (func:464, 2 refs), tokenizeCode (func:344, 2 refs), cosineSim (func:529), splitCamel (func:388), chunkAndScoreFile (func:208), chunkEmbedText (func:518), +8 more
- `semanticsearch.go` — NewSemanticSearchTool (func:43), embedBatched (func:270), embedInput (func:263), formatSemanticHits (func:304), normalizePrefix (func:292), pending (type:140), +3 more
- `symbolmap.go` — SymbolMap (type:264, 5 refs), SymbolDigest (func:695, 2 refs), isKeyword (func:593, 2 refs), CallersTruncated (func:260), truncateKeepingFileFloor (func:728), SymbolEntry (type:245), +22 more
- `symbolnav.go` — defHit (type:127), defRank (func:235), findReferencesInput (type:325), findReferencesTool (type:323), indentWidth (func:304), readSymbolInput (type:88), +5 more
- _… 9 more files_

### internal/tui/ — 44 files
- `agent_mentions.go` — agentMentionList (func:60, 2 refs), trailingMention (func:187, 2 refs), completeAtMention (func:128), completeMention (func:155), expandPrompt (func:116), agentMentionNames (func:28), +3 more
- `announce.go` — announceCmd (func:30), announceRefreshCmd (func:44), applyAnnouncements (func:53), announceMsg (type:18), announceTickMsg (type:21), renderAnnouncement (func:79)
- `attach.go` — stageAttachment (func:16, 2 refs), stagedAttachmentsLine (func:108, 2 refs), attachFullPath (func:37), pasteClipboardImage (func:50), PNGf (class:79), clipboardImagePNG (func:69)
- `bell.go` — ringBell (func:15), tuiBellEnabled (func:9)
- `btw.go` — btwCmd (func:15)
- `commands.go` — expandUserCommand (func:119, 4 refs), loadUserCommands (func:39, 4 refs), parseCommandFile (func:93, 2 refs), readCommandDir (func:60), userCommand (type:29)
- `mentions.go` — expandMentions (func:29, 3 refs), attachmentMentions (func:102, 2 refs), attachment (type:35)
- `plan.go` — planCmd (func:26)
- `seed.go` — seedEntries (func:12), extractedFileMarker (func:103)
- `styles.go` — logoGradientColors (func:315), tui (package:1)
- `transcript.go` — render (func:171, 12 refs), renderDiff (func:423, 4 refs), toggleCollapse (func:378, 3 refs), clipPath (func:713, 2 refs), completeTool (func:146, 2 refs), indent (func:570, 2 refs), +22 more
- `tui.go` — Update (func:647, 19 refs), View (func:2420, 9 refs), refresh (func:3147, 7 refs), submit (func:1301, 7 refs), command (func:1544, 6 refs), label (func:198, 5 refs), +115 more
- `tui_bridge.go` — SetHub (func:47, 2 refs), SetRelay (func:21, 2 refs), publishSessions (func:116, 2 refs), publishStatus (func:168, 2 refs), BindController (func:244), RelayReporter (func:33), +11 more
- `tui_cmds.go` — playDoneSoundCmd (func:188, 2 refs), checkpointCmd (func:152), compactCmd (func:140), doctorCmd (func:63), fetchMCPPromptCmd (func:165), fetchModelsCmd (func:22), +7 more
- `tui_format.go` — formatTokens (func:224, 3 refs), formatUSD (func:205, 3 refs), appendHistory (func:158, 2 refs), clampPct (func:235, 2 refs), commonPrefix (func:58, 2 refs), markdownStyle (func:77, 2 refs), +15 more
- `tui_slash.go` — saveConfig (func:29, 2 refs), agentsCmd (func:421), copyLast (func:350), domainsCmd (func:274), lspCmd (func:679), mcpsCmd (func:590), +11 more
- `tui_status.go` — addContextStatus (func:238, 2 refs), applyUsage (func:26, 2 refs), contextBarEnabled (func:142, 2 refs), contextStatus (func:255, 2 refs), contextStyle (func:153, 2 refs), formatQuotaShort (func:61, 2 refs), +11 more
- `tui_view.go` — statusView (func:501, 9 refs), answer (type:264, 5 refs), headerView (func:394, 3 refs), sidebarView (func:45, 3 refs), commandHints (func:605, 2 refs), menuView (func:332, 2 refs), +11 more

### internal/web/ — 108 files
_entry points: workflow_run, server_
- `access.go` — accessLists (type:26), handleAddDomain (func:83), handleAddPermission (func:45), handleGetDomains (func:78)
- `agents_market.go` — reattachSubAgents (func:37, 2 refs), agentCatalogItem (type:20), agentModelOption (type:185), agentOptions (type:264), handleAgentInstall (func:117), handleAgentModels (func:197), +7 more
- `announcements.go` — AnnouncementsHandler (func:22, 2 refs), announcementsAPI (type:16)
- `commands.go` — runCommand (func:151, 5 refs), commandsEvent (func:81, 4 refs), enabledWord (func:657, 3 refs), onOff (func:650, 3 refs), commandCatalog (func:73, 2 refs), runSelfUpdate (func:596), +9 more
- `commands_config.go` — connectMCP (func:715, 4 refs), ensureOllamaModel (func:894, 3 refs), connectLSP (func:810, 2 refs), mcpsCommand (func:656, 2 refs), agentsCommand (func:383), goalCommand (func:173), +14 more
- `commands_handlers.go` — mcpConnStatus (func:182, 2 refs), mcpStatusLabel (func:199, 2 refs), agentItem (type:27), handleListAgents (func:83), handleListLSP (func:143), handleListMCPs (func:120), +7 more
- `commands_menus.go` — emitAgentMenu (func:94), emitAgentsMenu (func:133), emitLspMenu (func:228), emitMcpsMenu (func:182), emitModelsMenu (func:46), emitPluginsMenu (func:208), +5 more
- `commands_user.go` — tokensSummary (func:16, 2 refs), formatQuota (func:55), sessionsSummary (func:63)
- `completion.go` — cleanCompletion (func:105), headBytes (func:133), tailBytes (func:125), buildFIMPrompt (func:85), handleComplete (func:39)
- `deploy.go` — stop (func:84, 4 refs), handleDeployCancel (func:209), handleDeployLog (func:199), handleDeployRun (func:153), handleDeployStatus (func:124), options (func:108), +4 more
- `doctor.go` — doctorStatusString (func:24), lspLiveCheck (func:64), mcpLiveCheck (func:81), doctorCheck (type:18), handleDoctor (func:36)
- `filesystem.go` — resolveNewSessionDir (func:122, 2 refs), dirEntry (type:25), expandHome (func:103), handleListDir (func:40), handleOpenDir (func:78)
- `fileview.go` — bytesLookBinary (func:216), resolveInWorkspace (func:76), fileTree (type:54), fileTreeEntry (type:46), fileView (type:35), handleFileTree (func:244), +3 more
- `git.go` — handleGitCommitDiff (func:51), handleGitDiff (func:33), handleGitOverview (func:21), handleGitWrite (func:77)
- `jobs.go` — sessionJobWires (func:75, 5 refs), jobsDigest (func:112, 2 refs), newJobTicket (func:301), handleJobInput (func:319), handleJobKill (func:168), handleJobStart (func:194), +6 more
- `lsp_market.go` — lastLines (func:208, 3 refs), handleLspCatalog (func:39), handleLspInstall (func:98), handleLspRemove (func:188), handleLspToggle (func:163), lspCatalogItem (type:23)
- `mcp_market.go` — configuredHas (func:87), handleMcpInstall (func:91), handleMcpRemove (func:147), handleMcpToggle (func:122), handleMcpsCatalog (func:37), mcpCatalogItem (type:21)
- `memory.go` — handleCodebaseMap (func:120), handleDeleteMemory (func:91), handleListMemory (func:18), handleReplaceMemorySection (func:69), handleSaveMemory (func:33)
- `mirror.go` — NewMirrorServer (func:36, 2 refs), mirrorWire (func:235), MirrorServer (type:30), handleList (func:75)
- `observability.go` — accessLogEnabled (func:108), looksLikeID (func:158), middleware (func:119), newMetrics (func:48), observe (func:50), routeLabel (func:142), +9 more
- `persona.go` — personaFields (func:828, 4 refs), applyPersonaEdit (func:615, 2 refs), envPersistSet (func:1146, 2 refs), applyCustomEnvEdits (func:1051), applyEnvTokenEdits (func:1107), applyEnvVarRemovals (func:1074), +24 more
- `plugins_market.go` — refreshAfterPlugin (func:228), handlePluginInstall (func:152), handlePluginMarketplaceAdd (func:98), handlePluginMarketplaceRemove (func:132), handlePluginRemove (func:187), handlePluginsCatalog (func:37), +4 more
- `quickactions.go` — handleAddQuickAction (func:75), handleGetQuickActions (func:51), handleMoveQuickAction (func:115), handleRemoveQuickAction (func:101)
- `replay.go` — replayMessages (func:21, 3 refs), rangeReplay (func:276, 2 refs), tailReplay (func:251, 2 refs), atoiDefault (func:306), extractedFileName (func:194), result (type:22), +2 more
- `schedules.go` — handleListSchedules (func:64), handleScheduleAdd (func:83), handleScheduleRemove (func:147), handleScheduleRun (func:175), handleScheduleToggle (func:120), scheduleItem (type:21), +1 more
- `search.go` — handleSearch (func:40), searchSnippet (func:107), messageSearchText (func:87), searchMatch (type:19)
- `server.go` ⭐ — writeJSON (func:1598, 26+ refs), Handler (func:270, 20 refs), lookup (func:1375, 17 refs), NewServer (func:124, 14 refs), WriteHeader (func:490, 13 refs), openSession (func:1381, 12 refs), +57 more
- `session.go` — emit (func:246, 22 refs), publishEvent (func:367, 11 refs), subscribe (func:499, 7 refs), clearQueue (func:932, 3 refs), queuedList (func:925, 3 refs), unsubscribe (func:511, 3 refs), +46 more
- `skills_market.go` — reattachSkills (func:32, 2 refs), handleSkillInstall (func:80), handleSkillRemove (func:125), handleSkillToggle (func:100), handleSkillsCatalog (func:39), skillCatalogItem (type:19)
- `status.go` — shortModel (func:120, 13 refs), contextPercent (func:107, 6 refs), statusEvent (func:22, 5 refs), prettyDir (func:134, 4 refs)
- `taskboard.go` — handleListTasks (func:16), handleTaskAdd (func:31), handleTaskApply (func:60), handleTaskDiscard (func:75), sessionAndExe (func:88), taskID (func:103)
- `wire.go` — toWire (func:343, 6 refs), attachWireFromAttachment (func:479, 2 refs), planWireFrom (func:446, 2 refs), cmdWire (type:192), contextSegmentsWire (func:171), wireEvent (type:17), +20 more
- `worker_market.go` — handleWorkerInstall (func:33)
- `workflow_approval.go` — pauseKey (func:36), approvalHub (type:29), handleResumeWorkflow (func:80), pauseResolution (type:21)
- `workflow_authoring.go` — handleSetWorkflowRuntime (func:201), llmText (func:318), normalizeCapName (func:31), runtimeSnapshot (func:173), firstJSONObject (func:333), handleCreateAgent (func:77), +9 more
- `workflow_handlers.go` — handleCreateWorkflow (func:54), handleDeleteWorkflow (func:131), handleGetWorkflow (func:85), handleListWorkflows (func:35), handleSaveWorkflow (func:104), newWorkflowID (func:28), +1 more
- `workflow_history.go` — firstSentence (func:215, 3 refs), capabilityItem (type:144), handleGetRun (func:40), handleListRuns (func:25), handleResumeRun (func:63), handleWorkflowCapabilities (func:163), +2 more
- `workflow_invoke.go` — attachWorkflowTool (func:39, 2 refs), WorkflowToolFuncs (func:30), nodeMetaSuffix (func:229), nodeTarget (func:203), runSavedWorkflow (func:72), listWorkflowNames (func:50), +2 more
- `workflow_run.go` ⭐ — Close (func:200, 94+ refs), run (func:233, 20 refs), randBytes (func:584), recordingEmit (func:112), runApproval (func:536), runUserInput (func:564), +16 more

### internal/web/static/ — 17 files
- `app-core.js` — approve (func:3687, 6 refs), popQueued (func:3597, 3 refs), api (const:4, 2 refs), handle (func:1291, 2 refs), renderSearchResults (func:5785, 2 refs), sendText (func:3006, 2 refs), +214 more
- `app-manage.js` — renderDeployPanel (func:2286, 2 refs), stopDeployPolling (func:2282, 2 refs), manageRow (func:1064), openMemoryEditor (func:237), openPlugins (func:663), refreshManage (func:1057), +74 more
- `app-render.js` — renderMD (func:390, 3 refs), prettyModel (func:845, 2 refs), providerLabel (func:910, 2 refs), appendDiffCount (func:1137), appendMsg (func:520), bashCommand (func:667), +39 more
- `app-state.js` — sym (const:197, 5 refs), alertBox (func:248, 2 refs), confirmBox (func:209, 2 refs), themeColor (const:202), AppIcon (class:173)
- `app-workflow.js` — open (func:157, 4 refs), field (func:924, 2 refs), base (func:63), checkbox (func:1111), renderAgentBuilderInto (func:1853), setRunning (func:1252), +92 more
- `app.css` — text-faint (class:2932, 4 refs), input (id:636, 3 refs), access-empty (class:900, 2 refs), access-form (class:950, 2 refs), access-head (class:897, 2 refs), access-input (class:951, 2 refs), +25 more

### internal/worker/ — 8 files
_entry points: worker_
- `acp.go` — Prompt (func:173, 5 refs), openACP (func:65, 2 refs), handleRequest (func:354), newACPSession (func:135), permissionDecision (func:369), acpSession (type:26), +12 more
- `claude.go` — openClaude (func:22), parseClaudeJSON (func:116), claudeResult (type:31), claudeSession (type:16)
- `detect.go` — Readiness (func:179, 6 refs), InstallCommand (func:41, 3 refs), ProbeReadiness (func:130, 3 refs), InstallPlanFor (func:159, 2 refs), RefreshReadiness (func:145, 2 refs), Installed (func:67), +5 more
- `gemini.go` — buildArgs (func:93, 2 refs), openGemini (func:25), parseGeminiJSON (func:112), geminiResult (type:36), geminiSession (type:20)
- `worker.go` ⭐ — Add (func:542, 63+ refs), SetEnabled (func:461, 9 refs), All (func:313, 8 refs), Configs (func:296, 6 refs), file (type:114, 6 refs), globalPath (func:118, 5 refs), +15 more

### internal/workflow/ — 19 files
- `dictionary_demo.go` — GetDictionaryNodeFunc (func:72, 2 refs), NewDictionaryWorkflow (func:41, 2 refs), workflow (package:1)
- `engine.go` — RunWithOptions (func:140, 2 refs), asString (func:585, 2 refs), nodeLabel (func:535, 2 refs), nextNode (func:566), Clock (type:119), NodeFunc (type:70), +13 more
- `evals.go` — RunEvals (func:53, 2 refs), EvalCase (type:20), EvalCaseResult (type:26), EvalSummary (type:38), runOneEval (func:76)
- `expr.go` — EvalCondition (func:89, 2 refs), Interpolate (func:30, 2 refs), compare (func:132), operand (func:124), renderValue (func:66), resolvePath (func:39)
- `guardrails.go` — CheckGuardrails (func:74, 2 refs), ValidateStructured (func:121, 2 refs), GuardrailConfig (type:25), GuardrailResult (type:33), localGuardrails (func:48), validateAgainst (func:141)
- `judge.go` — extractJSON (func:34, 2 refs), judge (func:71, 2 refs), responseText (func:19)
- `runstore.go` — OpenRuns (func:66, 4 refs), OpenRunsAt (func:75), prune (func:163), RunRecord (type:42), RunStore (type:63), TraceEntry (type:29)
- `store.go` — path (func:54), validID (func:40)
- `templates.go` — Template (type:10, 4 refs), Templates (func:26, 3 refs)
- `types.go` — Edge (type:132), Node (type:118), NodeData (type:40), Position (type:33), Workflow (type:143)
- `validate.go` — Validate (func:22, 2 refs), branchJoin (func:121), matchJoin (func:85), nonNoteTargets (func:174), walkEdges (func:160)

### ios/AgentRelayClient/ — 10 files
_entry points: SSEClient_
- `AgentRelayClientApp.swift` — reload (func:171), AgentRelayClientApp (struct:7), BrowserView (struct:82), seedDefaultProfilesIfNeeded (func:42), startObservingProgress (func:173)
- `ComposerView.swift` — send (func:169, 8 refs), ComposerView (struct:5), agentHue (func:258), AttachmentStripView (struct:309), AutocompletePopover (struct:274), agentColor (func:239), +6 more
- `ConfigSheets.swift` — encode (func:343, 2 refs), ConfigSheetsView (struct:10), AnyEncodableValue (struct:339), MCPListView (struct:221), MCPServer (struct:327), SkillConfig (struct:319), +15 more
- `HostProfile.swift` — HostProfile (class:5, 2 refs)
- `MainSplitView.swift` — AddHostSheet (func:531), HeaderBarView (struct:581), MainSplitView (struct:4), MenuPickerSheet (struct:780), ProgressDrawer (struct:680), checkAuthAndFetchSessions (func:297), +10 more
- `Models.swift` — ChatMessage (struct:232), ClientApproval (struct:248), ClientQuestion (struct:257), MessageBody (struct:197), OutboundAttachment (struct:185)
- `SSEClient.swift` ⭐ — cancel (func:817, 83+ refs), interrupt (func:597, 7 refs), sendMessage (func:480, 4 refs), connect (func:81, 2 refs), set (func:811, 2 refs), AuthWebView (struct:682), +26 more
- `TranscriptView.swift` — foregroundColor (func:108, 3 refs), TranscriptView (struct:3), ApprovalCardView (struct:154), AvatarView (struct:118), CodeDiffView (struct:333), MessageRowView (struct:43), +6 more

### test/browser/src/ — 4 files
- `decider.ts` — scriptedDecider (func:27, 3 refs), llmDecider (func:13), Decider (type:10), Script (interface:21)
- `driver.ts` — runConversation (func:16, 4 refs), trunc (func:66)
- `persona.ts` — Turn (interface:6)
- `sqe.ts` — newSession (func:12, 9 refs), Sqe (class:33, 4 refs), focusLaptop (func:9), selectSession (func:11), Pending (type:24), QSpec (interface:16)
<!-- END AUTO:STRUCTURE -->

## Narrative — features → files

<!-- BEGIN NARRATIVE -->
Features → the files that implement them. Hand-maintained (via `@map`); the Structure section above is the generated ground truth for *where things are*, this section explains *what they are*.

## What this project is

`moony` is an AI coding agent for the terminal, with three front ends over one core: a **TUI**, a **web UI**, and a **relay** that serves the web UI remotely. It runs locally, headless (one-shot prompts), or as a background **daemon**.

Layering: `main.go` (CLI, flags, wiring) → `internal/agent` (turn loop, policy, context) → `internal/tools` (capabilities) + `internal/llm` (provider streaming) → `internal/session` (persistence). Front ends never call tools directly; they consume the event stream the agent emits (`internal/agent/agent_events.go`) and send decisions back, which is why the TUI and web UI stay in step.

## 1. Agent core — the turn loop

- **Construction & the loop** — `internal/agent/agent.go` (`New`:319, `Run`:994). Owns config, tool registry, permissions, plan state and the message history.
- **Tool execution** — `internal/agent/agent_tools.go` (`runToolCalls`:29): permission checks, approval gating, and parallel execution of independent calls.
- **System prompt assembly** — `internal/agent/agent_prompt.go` (`buildSystemPrompt`:16): identity, environment, project memory, the codebase-map digest, skills and tool docs.
- **Events to front ends** — `internal/agent/agent_events.go`: `ApprovalRequest`, `ToolStart`/`ToolEnd`, streaming text; the single contract both UIs render.
- **Persistence & rehoming** — `internal/agent/agent_persist.go`, `internal/agent/rehome.go` (re-points an agent at a new working dir, reloading permissions).
- **Approvals** — `internal/agent/agent_approval.go` (`requestApproval`:96, `requestApprovalOnce`:104).

## 2. Context management

- **Compaction** — `internal/agent/compact.go` (`estimateTokens`:19, `maybeCompact`:58) and `internal/agent/compact_overflow.go` for hard overflows.
- **Context assembly & budget** — `internal/agent/context.go`; relevance-based trimming in `internal/agent/relevance.go`; JSON payload shrinking in `internal/agent/jsonshrink.go`.
- **Repetition & injection guards** — `internal/agent/repeat.go`, `internal/agent/injection.go`.
- **Checkpoints** — `internal/agent/checkpoint.go`.

## 3. Planning, tasks & effort

- **Plan mode** — `internal/agent/plan.go`: draft → approve → execute → retire. Note the pinned invariant: `PlanStore.Set` notifies (forces draft); host-side mirror writes must use `PlanStore.Sync`.
- **Autoplan** — `internal/agent/agent.go` (`MaybeAutoPlan`:751).
- **Goals** — `internal/agent/goal.go`; **difficulty / reasoning effort** — `internal/agent/difficulty.go`, `internal/agent/agent_effort.go`.
- **Sub-agent coordination** — `internal/agent/task.go` (delegation), `internal/agent/board.go` (`messageBoard`:55 — the shared message board), `internal/agent/delegate.go` (external workers).

## 4. Memory & project knowledge

- **This map** — `internal/config/codebasemap.go` (read/write with the PINNED / AUTO:STRUCTURE / NARRATIVE markers), `internal/tools/codebasemap.go` (`CodebaseMapStructural`:115 renders the structure), `internal/tools/codebasemap_tool.go` (the `codebase_map` tool serving per-directory detail), `internal/agent/codebasemap.go` (the compact digest that goes into the prompt).
- **Symbol map** — `internal/tools/symbolmap.go`, powering `find_symbol` / `find_references` and the map's per-file symbol lists.
- **AGENTS.md / project memory** — `internal/manage`, surfaced in the web UI via `internal/web/memory.go`.
- **Project notes** — `internal/config/project_notes.go` (a per-project checklist), the `@notes` backlog mention in `internal/agent/mentions.go`.
- **Skills** — `internal/skills/catalog.go`.

## 5. Tools

The registry lives in `internal/tools/tool.go` (`NewRegistry`), with the default set wired in `internal/tools/defaults.go` (~50 registrations). Notable groups:

- **Files** — `internal/tools/files.go` (read/write/edit/apply_patch).
- **Search & listing** — `internal/tools/search.go` (`grep`, `glob`, `list_dir`), `internal/tools/searchcode.go`, `internal/tools/semanticsearch.go`, `internal/tools/ignore.go` (`.moonyignore`, with a `.sqeignore` fallback).
- **Shell & jobs** — `internal/tools/bash.go`, `internal/tools/jobs.go`, `internal/tools/jobtty.go` (background jobs with a PTY).
- **Git** — `internal/tools/git.go`, `internal/tools/worktree.go` (worktrees under `.moony/worktrees/`), `internal/tools/pr.go`.
- **Debugging (DAP)** — `internal/dap/dap.go`, `internal/dap/spawn.go` (`dap_start` spawns stdio/socket adapters), tool in `internal/tools/dap.go`.
- **Browser** — `internal/tools/browser.go`; **MCP** — `internal/mcp/` (config, client, manager) exposing external servers as tools.
- **Workers & taskboard** — `internal/worker/worker.go`, `internal/taskboard/taskboard.go`.

## 6. LLM providers

- **Provider ids** — `internal/provider/provider.go`: hosted providers (OpenAI, Anthropic, Gemini, Azure, Bedrock, Copilot, OpenRouter, DeepSeek, Groq, xAI, Mistral, Together, Fireworks, Moonshot, ZAI, DashScope, NVIDIA, HuggingFace, Venice, OmniRoute, Cerebras, Perplexity, Cohere, DeepInfra, Meta) plus local runtimes (Ollama, LM Studio, llama.cpp), with `Normalize`:197 mapping aliases onto them.
- **Streaming & usage** — `internal/llm/` (21 files): `streamer.go`, `usage.go` (quota/budget/rates), plus per-provider adapters.
- **Model catalog** — `internal/config/config.go` (`AllKnownModels`:1567) with context sizes and pricing.

## 7. Front ends

- **TUI** — `internal/tui/tui.go` (Bubble Tea model/update), `internal/tui/tui_view.go` (rendering, including the approval box and status line).
- **Web UI** — `internal/web/server.go` (routes, sessions), `internal/web/wire.go` (`toWire` — the SSE event contract), `internal/web/static/` (the SPA: `app-core.js`, `app-manage.js`, `app.css`, `index.html`).
- **Relay** — `cmd/relay/`, `internal/relay/`. **Important:** the relay embeds and serves its own copy of `internal/web/static`, so web changes require rebuilding the relay too (see Pinned notes).

## 8. Permissions & approvals

- **Rule store** — `internal/permissions/permissions.go`: allow/deny rules at global and project scope (`.moony/permissions.json`), with prefix and exact whole-command matching for `bash:` rules.
- **Approval flow** — `internal/agent/agent_tools.go`; **destructive detection** — `internal/agent/destructive.go` (`destructiveCommand`:34, `classifyDestructive`:41, `detectRmRf`:107, `detectDestructiveGit`:148).
- Git commit/push and destructive commands re-confirm every time and ignore "always allow"; the request carries `AlwaysDisabled` so UIs hide that button.

## 9. Daemon, connector & scheduling

- **Daemon** — `internal/daemon/daemon.go`, `internal/daemon/service.go` (install/run as a user service).
- **Connector slot** — `internal/daemon/connector.go` (`TryHoldConnector`, `lockHeldByOther`, `pidIsOurs`) plus `internal/daemon/procname.go` (pid → executable name). One connector per machine; standbys mirror `connector.status`.
- **Scheduler** — `internal/daemon/scheduler.go` (`runScheduler`:21), `internal/schedule/`.

## 10. Sessions, config & entry points

- **Sessions** — `internal/session/session.go`, `backend.go`, `crypto.go` (at-rest encryption; `resolveCrypto`:76, BYOK support).
- **Config** — `internal/config/config.go` (`Load`), `internal/config/paths.go` (`GlobalConfigDir`, `ProjectDir` = `.moony`, migration from `.sqe`), `internal/config/prefs.go`.
- **Entry points** — `main.go` (TUI/CLI), `headless.go` (`runHeadless`:72), and `cmd/`: `relay`, `release`, `eval`, `sim-decide`, `dict-workflow`.
<!-- END NARRATIVE -->
