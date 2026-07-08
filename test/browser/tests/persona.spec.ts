import { test, expect } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { Sqe } from '../src/sqe';
import { runConversation } from '../src/driver';
import { llmDecider } from '../src/decider';

// Working dir of the session under test (for the goal-completion on-disk check).
const WORKDIR = process.env.SQE_WORKDIR || path.resolve(process.cwd(), '..', '..');

// A configurable synthetic user (LLM persona). Override via env to test journeys.
const PERSONA =
  process.env.SIM_PERSONA ||
  'You are a pragmatic backend engineer. You give brief, sometimes slightly under-specified requirements, answer questions concisely, approve edits that clearly serve your goal, and stop once the goal is met.';
const GOAL = process.env.SIM_GOAL || 'Add a GET /health endpoint that returns HTTP 200, with a passing test.';
const MAX_STEPS = Number(process.env.SIM_MAX_STEPS || 10);

// End-to-end, LLM-driven: a persona drives the REAL web UI in a real browser —
// typing messages, answering ask_user prompts (single AND the questions[] tabs),
// and approving/denying tool actions — until it decides the goal is met.
//
// LLM journeys are realistic but non-deterministic (real tokens on both sides);
// this test asserts only that the conversation runs. For strict, assertable
// regression see journey.spec.ts (scripted). Add goal-specific checks here as
// needed (e.g. sqe.toolCards(), sqe.errors()).
test('LLM persona drives the agent through the browser', async ({ page }) => {
  test.skip(
    process.env.SQE_PERSONA_EXPLORE !== '1',
    'exploratory: drives the agent to BUILD a feature, which mutates the repo — on an auto-approve session it really writes files. Opt in with SQE_PERSONA_EXPLORE=1 on a throwaway repo.',
  );
  const sqe = new Sqe(page);
  await runConversation(sqe, llmDecider(PERSONA, GOAL), {
    maxSteps: MAX_STEPS,
    log: (m) => console.log('[persona]', m),
  });
});

// A goal-directed persona journey with a concrete, verifiable outcome: the persona
// pursues creating a specific file, and we assert the goal was actually reached —
// the file exists on disk with the expected content. Real agent + real persona (so
// non-deterministic), but the goal is concrete enough to complete reliably.
test('LLM persona reaches a concrete goal (file created)', async ({ page }) => {
  const marker = 'SQE_PERSONA_GOAL.md';
  const content = 'persona goal reached';
  const persona =
    'You are a decisive engineer using a coding agent. State your goal clearly, approve reasonable edits, answer any question briefly, and STOP as soon as the goal is verifiably done. Do not chit-chat.';
  const goal = `Get the agent to create a file named ${marker} at the repository root whose entire contents are exactly the line: "${content}". Do NOT run git or commit anything — just create the file. The goal is done only once that file exists.`;

  const sqe = new Sqe(page);
  await runConversation(sqe, llmDecider(persona, goal), {
    maxSteps: 14,
    log: (m) => console.log('[persona-goal]', m),
  });

  // Verify goal completion on disk, then clean up the artifact.
  const file = path.join(WORKDIR, marker);
  const created = fs.existsSync(file);
  const body = created ? fs.readFileSync(file, 'utf8') : '';
  try { fs.rmSync(file); } catch {}

  expect(await sqe.errors(), 'agent surfaced an error banner').toEqual([]);
  expect(created, `persona did not reach the goal — ${marker} was not created`).toBeTruthy();
  expect(body).toContain(content);
});
