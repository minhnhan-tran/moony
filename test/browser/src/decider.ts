import { Pending, QSpec } from './sqe';
import { decide as llmDecide, Turn, Decision } from './persona';

export type { Turn, Decision };

// A Decider maps the conversation state (and whatever the agent is waiting on) to
// the next human action. Two implementations ship: an LLM persona (realistic,
// non-deterministic) and a scripted persona (deterministic, no model, no tokens
// on the user side — ideal for assertable regression journeys).
export type Decider = (history: Turn[], pending: Pending) => Decision;

// llmDecider role-plays a user via the Go persona brain (cmd/sim-decide).
export function llmDecider(persona: string, goal: string): Decider {
  return (history, pending) => llmDecide(persona, goal, history, pending);
}

// Script drives a deterministic persona: a fixed list of user messages (one sent
// per idle turn, then it stops), plus optional policies for answering questions
// and approvals. Defaults: answer picks each question's first option (or a short
// canned text when a question has no options); approvals are approved.
export interface Script {
  messages: string[];
  answer?: (questions: QSpec[]) => { labels?: string[]; text?: string; groups?: string[][] };
  approve?: (pending: Extract<Pending, { kind: 'approval' }>) => boolean;
}

export function scriptedDecider(script: Script): Decider {
  let sent = 0;
  return (_history, pending) => {
    if (pending.kind === 'approval') {
      const ok = script.approve ? script.approve(pending) : true;
      return { action: ok ? 'approve' : 'deny', reason: 'scripted' };
    }
    if (pending.kind === 'question') {
      const qs = pending.questions;
      const custom = script.answer ? script.answer(qs) : undefined;
      if (qs.length > 1) {
        return { action: 'answerGroup', groups: custom?.groups ?? qs.map(firstOptionGroup), reason: 'scripted' };
      }
      const single = custom ?? defaultSingle(qs[0]);
      return { action: 'answer', labels: single.labels, text: single.text, reason: 'scripted' };
    }
    // idle → send the next scripted message, or stop when the script runs out.
    if (sent < script.messages.length) return { action: 'message', text: script.messages[sent++], reason: 'scripted' };
    return { action: 'stop', reason: 'script exhausted' };
  };
}

function defaultSingle(q: QSpec): { labels?: string[]; text?: string } {
  return q.options.length ? { labels: [q.options[0]] } : { text: 'yes, go ahead' };
}

function firstOptionGroup(q: QSpec): string[] {
  return q.options.length ? [q.options[0]] : ['yes'];
}
