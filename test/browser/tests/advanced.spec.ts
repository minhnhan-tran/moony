import { test, expect } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { execFileSync } from 'node:child_process';
import { Sqe } from '../src/sqe';
import { runConversation } from '../src/driver';
import { scriptedDecider, Script } from '../src/decider';

// Advanced real-human interactions: approvals, interrupting a turn, and
// type-ahead queueing. All run against your live stack (MN_BASE_URL +
// MN_STORAGE_STATE). The session is rooted at the connector's first project.

// The working dir of the session under test (for on-disk / git assertions). The
// first project is the repo root by default; override with MN_WORKDIR.
const WORKDIR = process.env.MN_WORKDIR || path.resolve(process.cwd(), '..', '..');

function git(dir: string, ...args: string[]): string {
  return execFileSync('git', args, { cwd: dir, encoding: 'utf8' }).trim();
}

// Deny a hard-gated action (git commit) and prove nothing was committed.
//
// ⚠️ DESTRUCTIVE on an auto-approve session: with no approval prompt, the agent
// really runs (and may push) the commit. So this is SKIPPED unless you opt in
// with MN_APPROVAL_TEST=1, and you should only do that against a PROMPT-mode
// session rooted at a THROWAWAY repo (set MN_WORKDIR to it). It verifies the
// deny path: the commit prompt is denied and HEAD is unchanged.
test('approval: deny a git commit — nothing gets committed', async ({ page }) => {
  test.skip(
    process.env.MN_APPROVAL_TEST !== '1',
    'destructive without an approval prompt (auto-approve sessions really commit/push); opt in with MN_APPROVAL_TEST=1 on a PROMPT-mode session + throwaway MN_WORKDIR',
  );
  const headBefore = git(WORKDIR, 'rev-parse', 'HEAD');
  const scratch = 'MN_DENY_MARKER.txt';

  const sqe = new Sqe(page);
  let sawApproval = false;
  let approvalText = '';
  const script: Script = {
    messages: [
      `Create a file named ${scratch} at the repo root containing the text "deny-test", then stage and git-commit ONLY that file with the message "sqe-bot-deny-test".`,
    ],
    approve: () => false, // DENY the commit
  };

  await runConversation(sqe, scriptedDecider(script), {
    maxSteps: 8,
    log: (m) => console.log('[approval]', m),
    onPending: (p) => {
      if (p.kind === 'approval') { sawApproval = true; approvalText = p.summary; }
    },
  });

  // Clean up the scratch file (write auto-approves even though the commit is denied).
  try { fs.rmSync(path.join(WORKDIR, scratch)); } catch {}
  try { git(WORKDIR, 'reset', '-q', 'HEAD', '--', scratch); } catch {}

  expect(await sqe.errors(), 'agent surfaced an error banner').toEqual([]);
  // The core guarantee: the commit did NOT happen.
  expect(git(WORKDIR, 'rev-parse', 'HEAD'), 'HEAD must be unchanged (no commit)').toBe(headBefore);
  expect(git(WORKDIR, 'log', '--oneline', '-5'), 'the denied commit must be absent').not.toContain('sqe-bot-deny-test');

  if (sawApproval) console.log('[approval] denied approval for: ' + approvalText.slice(0, 100).replace(/\s+/g, ' '));
  else {
    test.info().annotations.push({ type: 'note', description: 'no approval prompt appeared (agent may not have reached the commit); nothing was committed either way' });
    console.warn('[approval] no approval prompt seen — deny path not exercised, but HEAD is unchanged');
  }
});

// Start a long turn, then hit Stop — the "changed my mind" gesture. The turn must
// halt (return to idle) well before it could have finished naturally.
test('interrupt: stop a running turn mid-flight', async ({ page }) => {
  const sqe = new Sqe(page);
  await sqe.openSession();
  await sqe.sendMessage(
    'Read internal/config/config.go, internal/web/session.go, internal/web/server.go, internal/agent/agent.go and internal/tools/ask.go, then write a thorough three-paragraph summary of EACH file. Take your time and be exhaustive.',
  );
  await sqe.waitForBusy(); // turn started (Send → Stop)
  await sqe.interrupt(); // changed my mind — hit Stop
  // A five-file exhaustive summary would take far longer than this; going idle now
  // means the interrupt took effect (a user cancel isn't recorded as an error).
  await sqe.waitForIdle(60_000);
  expect(await sqe.errors(), 'interrupt should not surface an error').toEqual([]);
  await expect(page.locator('#send[aria-label="Send"]'), 'composer should be idle after interrupt').toBeVisible();
});

// Type-ahead: send a second message while the agent is busy. On a queue-capable
// session it enqueues rather than interrupting, then runs after the first turn.
test('type-ahead: queue a message while busy, it runs after', async ({ page }) => {
  const sqe = new Sqe(page);
  await sqe.openSession();
  await sqe.sendMessage('Read internal/config/config.go and internal/web/wire.go and briefly summarize each.');
  await sqe.waitForBusy();

  await sqe.queueMessage('Also: what is the value of the Version constant? Answer in one line.');
  // It should be queued (type-ahead), not sent immediately or interrupting.
  await expect.poll(() => sqe.queuedCount(), { timeout: 15_000, message: 'message should be queued while busy' }).toBeGreaterThan(0);

  // After the first turn finishes, the queue drains and the second message runs —
  // it appears as a user message in the transcript.
  await expect.poll(() => sqe.queuedCount(), { timeout: 5 * 60 * 1000, message: 'queue should drain (second message runs)' }).toBe(0);
  await expect(
    page.locator('#transcript .msg-user', { hasText: 'Version constant' }),
    'the queued message should have run',
  ).toBeVisible();
  expect(await sqe.errors()).toEqual([]);
});
