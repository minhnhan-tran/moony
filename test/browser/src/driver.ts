import { Sqe, Pending } from './sqe';
import { Decider, Turn } from './decider';

export interface RunOptions {
  maxSteps?: number; // safety cap on interactions (don't loop forever)
  log?: (msg: string) => void;
  onPending?: (pending: Pending) => void; // observe each question/approval the agent raises
}

// runConversation drives one full session with the given Decider (LLM or
// scripted): open a session, send the opening message, then repeatedly wait for
// the agent to pause (question/approval) or settle, letting the Decider choose
// each response, until it says "stop" or the step budget is hit. Returns the
// transcript so callers can assert on it (the passed-in Sqe can be used for
// DOM-level assertions afterward — tool cards, errors, etc.).
export async function runConversation(sqe: Sqe, decide: Decider, opts: RunOptions = {}): Promise<Turn[]> {
  const maxSteps = opts.maxSteps ?? 10;
  const log = opts.log ?? (() => {});
  const history: Turn[] = [];

  await sqe.openSession();

  let d = decide(history, { kind: 'none' });
  if (d.action !== 'message' || !d.text) {
    throw new Error(`decider did not open with a message: ${JSON.stringify(d)}`);
  }
  log(`open → ${trunc(d.text)}`);
  history.push({ role: 'user', text: d.text });
  await sqe.sendMessage(d.text);

  for (let step = 0; step < maxSteps; step++) {
    const pending: Pending = await sqe.waitForTurn();

    // Mid-turn: the agent is blocked on a question or an approval.
    if (pending.kind !== 'none') {
      opts.onPending?.(pending);
      const dec = decide(history, pending);
      log(`${pending.kind} → ${dec.action}${dec.reason ? ` (${dec.reason})` : ''}`);
      switch (dec.action) {
        case 'approve': await sqe.approve(); break;
        case 'deny': await sqe.deny(); break;
        case 'answerGroup': await sqe.answerGroup(dec.groups || []); break;
        case 'answer': await sqe.answerSingle(dec.labels || [], dec.text); break;
        case 'stop': return history;
        default: throw new Error(`unexpected action for ${pending.kind}: ${dec.action}`);
      }
      continue; // the same agent turn resumes after we respond
    }

    // Turn settled: record the agent's reply, then decide the next human move.
    const reply = await sqe.latestAssistant();
    if (reply) history.push({ role: 'agent', text: reply });

    d = decide(history, { kind: 'none' });
    log(`idle → ${d.action}${d.reason ? ` (${d.reason})` : ''}`);
    if (d.action === 'stop') return history;
    if (d.action === 'message' && d.text) {
      history.push({ role: 'user', text: d.text });
      await sqe.sendMessage(d.text);
    }
  }
  log(`hit maxSteps=${maxSteps}`);
  return history;
}

function trunc(s: string): string {
  return s.length > 80 ? s.slice(0, 79) + '…' : s;
}
