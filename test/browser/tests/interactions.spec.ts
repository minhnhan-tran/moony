import { test, expect } from '@playwright/test';
import { Sqe, Pending } from '../src/sqe';
import { runConversation } from '../src/driver';
import { scriptedDecider, Script } from '../src/decider';

// More real-human-interaction journeys, deterministic on the user side (scripted).
// The agent is real, so assertions are outcome-based and tolerant of variation.
// All run against your live stack (MOONY_BASE_URL + MOONY_STORAGE_STATE, see README).

// A natural multi-turn, read-only conversation: ask something, then a follow-up
// that depends on the first answer. Exercises conversation continuity + the
// agent's read/inspect tools, with zero mutations (any prompt is denied).
test('multi-turn: two-message read-only conversation', async ({ page }) => {
  const sqe = new Sqe(page);
  const script: Script = {
    messages: [
      'List the files and top-level folders in the repository root. Do not modify anything.',
      'Thanks — based on that, what kind of project is this? One sentence.',
    ],
    approve: () => false, // read-only: reject any mutating action if the agent asks
  };

  const history = await runConversation(sqe, scriptedDecider(script), {
    maxSteps: 12,
    log: (m) => console.log('[multiturn]', m),
  });

  expect(await sqe.errors(), 'agent surfaced an error banner').toEqual([]);
  // Both user messages were actually sent (conversation ran to the follow-up).
  expect(history.filter((t) => t.role === 'user').length, 'both messages should be sent').toBe(2);
  // The agent replied at least once and used at least one tool to inspect the repo.
  expect(history.some((t) => t.role === 'agent' && t.text.length > 0), 'agent never replied').toBeTruthy();
  expect((await sqe.toolCards()).length, 'agent should have used a tool to look at the repo').toBeGreaterThan(0);
});

// The flagship ask_user flow: get the agent to pose several questions in ONE
// ask_user call, and drive the answer through the panel (the questions[] tabs).
// Best-effort — a real agent may decline to ask; when it does ask, this exercises
// the full answer path (grouped tabs or single) end-to-end in the browser.
test('ask_user: bot answers the question panel (tabs / single)', async ({ page }) => {
  const sqe = new Sqe(page);
  let sawQuestion = false;
  let sawGrouped = false;

  const script: Script = {
    messages: [
      'Before doing any work, collect some decisions from me. Use the ask_user tool with its `questions` array to ask, in ONE call: ' +
        '(1) target language — options Go or Python; (2) include tests? — options yes or no; (3) any constraints — free text. ' +
        'Ask now via ask_user and wait for my answers; do not start work yet.',
    ],
    approve: () => false, // keep it non-mutating regardless of what it proposes
    // default answer policy picks each question's first option / a short free-text reply
  };

  const history = await runConversation(sqe, scriptedDecider(script), {
    maxSteps: 8,
    log: (m) => console.log('[askuser]', m),
    onPending: (p: Pending) => {
      if (p.kind === 'question') {
        sawQuestion = true;
        if (p.questions.length > 1) sawGrouped = true;
      }
    },
  });

  expect(await sqe.errors(), 'agent surfaced an error banner').toEqual([]);

  if (!sawQuestion) {
    // The agent chose not to ask this run — the harness didn't break, but the
    // answer path wasn't exercised. Surface it rather than silently "passing".
    test.info().annotations.push({ type: 'note', description: 'agent did not call ask_user this run; answer path not exercised' });
    console.warn('[askuser] agent did not call ask_user — nothing to answer (re-run or strengthen the prompt)');
    return;
  }

  // The agent asked and the driver answered it via the panel without throwing (a
  // broken tabs/single interaction would have failed the run). Confirm the answer
  // was accepted — the panel is dismissed and the turn moved on. (We don't require
  // a follow-up text reply: asking with no preamble and ending the turn is valid,
  // especially when told not to start work yet.)
  console.log(`[askuser] exercised ask_user (grouped tabs = ${sawGrouped})`);
  expect(await sqe.questionOpen(), 'question panel should be dismissed after answering').toBeFalsy();
});
