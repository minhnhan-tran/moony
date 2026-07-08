import { execFileSync } from 'node:child_process';
import * as path from 'node:path';
import { Pending } from './sqe';

// Turn is one line of transcript passed to the persona brain.
export interface Turn {
  role: 'user' | 'agent';
  text: string;
}

// Decision mirrors sim-decide's output action.
export interface Decision {
  action: 'message' | 'answer' | 'answerGroup' | 'approve' | 'deny' | 'stop';
  text?: string;
  labels?: string[];
  groups?: string[][];
  reason?: string;
}

// The Go persona brain. Built by `make sim-decide` into repo bin/. Override with
// SIM_DECIDE_BIN. Default resolves relative to this harness (test/browser → repo/bin).
const BIN = process.env.SIM_DECIDE_BIN || path.resolve(process.cwd(), '..', '..', 'bin', 'sim-decide');

// decide asks the LLM persona for the next human action given the conversation
// and whatever the agent is waiting on. It shells out to the Go decider (which
// owns Claude/Floodgate auth) and parses its one-line JSON action.
export function decide(persona: string, goal: string, history: Turn[], pending: Pending): Decision {
  const input = JSON.stringify({ persona, goal, history, pending });
  let out: string;
  try {
    out = execFileSync(BIN, { input, encoding: 'utf8', maxBuffer: 16 * 1024 * 1024 });
  } catch (e: any) {
    const stderr = e?.stderr ? `\n${e.stderr}` : '';
    throw new Error(`sim-decide failed (is it built? \`make sim-decide\`, path=${BIN}):${stderr}\n${e?.message ?? e}`);
  }
  return JSON.parse(out) as Decision;
}
