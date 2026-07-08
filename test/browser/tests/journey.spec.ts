import { test, expect } from '@playwright/test';
import * as fs from 'node:fs';
import * as path from 'node:path';
import { Sqe } from '../src/sqe';
import { runConversation } from '../src/driver';
import { scriptedDecider, Script } from '../src/decider';

// A file with a unique marker so the assertions can't be satisfied by unrelated
// pre-existing state.
const MARKER = 'SQE_BOT_MARKER.md';
const CONTENT = 'hello from the sqe browser bot';

// A deterministic, LLM-free JOURNEY with concrete assertions. The USER side is
// scripted (no persona model, no user-side tokens, reproducible); the agent is
// still the real thing, so the assertions are outcome-based and tolerant of how
// the agent chooses to do the work.
//
// It asks for one small, verifiable change, approves any prompt, then asserts:
//   1. no error banner surfaced,
//   2. a file-writing tool acted on the marker file,
//   3. the agent actually replied / finished,
//   4. (opt-in, SQE_WORKDIR) the file exists on disk with the expected content.
test('scripted journey: create a marker file (asserted)', async ({ page }) => {
  const script: Script = {
    messages: [
      `Create a file named ${MARKER} at the repository root whose entire contents are exactly this line: "${CONTENT}". Then briefly confirm you are done.`,
    ],
    approve: () => true, // approve the write if the agent asks
  };

  const sqe = new Sqe(page);
  const history = await runConversation(sqe, scriptedDecider(script), {
    maxSteps: 8,
    log: (m) => console.log('[journey]', m),
  });

  // 1. The agent didn't surface an error.
  expect(await sqe.errors(), 'agent surfaced an error banner').toEqual([]);

  // 2. A file-writing tool acted on the marker (tolerant of write_file /
  //    edit_file / multi_edit / create-style tools).
  const cards = await sqe.toolCards();
  const wrote = cards.some((c) => /(write_file|edit_file|multi_edit|create)/i.test(c) && c.includes(MARKER));
  expect(wrote, `no write/edit tool card referenced ${MARKER}.\ntool cards:\n${cards.join('\n---\n') || '(none)'}`).toBeTruthy();

  // 3. The agent actually produced a reply and the conversation finished.
  expect(history.some((t) => t.role === 'agent' && t.text.length > 0), 'agent never replied').toBeTruthy();

  // 4. Strongest check (opt-in): verify the file on disk. Set SQE_WORKDIR to the
  //    session's working directory to enable it.
  const workdir = process.env.SQE_WORKDIR;
  if (workdir) {
    const file = path.join(workdir, MARKER);
    expect(fs.existsSync(file), `expected ${file} to exist`).toBeTruthy();
    expect(fs.readFileSync(file, 'utf8')).toContain(CONTENT);
  } else {
    console.log('[journey] SQE_WORKDIR not set — skipping the on-disk file check (UI assertions still ran).');
  }
});
