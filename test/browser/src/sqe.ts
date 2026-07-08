import { Page, Locator, expect } from '@playwright/test';

// SPA globals referenced by bare name inside page.evaluate(). They're top-level
// `let`/`const`/`function` bindings in app-core.js (a classic script), so they
// live in the page's global scope but are NOT properties of `window` — hence we
// must reference them unqualified, not as `window.x`. These `declare`s only make
// TypeScript accept the bare names; at runtime they resolve in the page.
declare const laptops: Array<{ host: string; base: string; online?: boolean }> | null;
declare function focusLaptop(host: string): void;
declare let currentProject: string;
declare function selectSession(id: string): void;
declare function newSession(): Promise<void>;


// QSpec mirrors one ask_user question as scraped from the DOM.
export interface QSpec {
  question: string;
  options: string[];
  multiple: boolean;
  allowText: boolean;
}

// Pending is what the agent is currently blocked on (mirrors sim-decide's input).
export type Pending =
  | { kind: 'none' }
  | { kind: 'approval'; tool: string; summary: string; diff: string }
  | { kind: 'question'; questions: QSpec[] };

// Sqe is a page object for the moony web UI. It targets the same
// elements the app already ships (ids/classes confirmed in index.html +
// app-core.js): #input/#send composer, #approval + [data-d], the #question panel
// (single OR the questions[] tabs), and #transcript messages.
export class Sqe {
  constructor(private page: Page) {}

  // openSession loads the app and ensures a fresh, ready session. It handles both
  // topologies the SPA runs in: relay mode (a laptop tree — create a session in
  // the first online laptop's first project via POST {base}/api/projects/{key}/sessions)
  // and mirror/direct mode (bare newSession()). Reuses the SPA's own globals so it
  // stays in lockstep with how the UI itself creates + selects a session.
  async openSession(): Promise<void> {
    await this.page.goto('/');
    if (await this.page.locator('text=/log ?in|sign ?in|AppleConnect/i').first().isVisible().catch(() => false)) {
      throw new Error('Login screen detected — capture a valid storageState (see README "Auth") or point MOONY_BASE_URL at a loopback, unauthenticated instance.');
    }
    await this.page.locator('#input').waitFor({ state: 'attached', timeout: 60_000 });
    // Wait for boot() to fetch /api/laptops (relay mode sets `laptops` to an array).
    // Mirror/direct mode leaves it null → the wait lapses and we use newSession().
    await this.page.waitForFunction(() => Array.isArray(laptops), undefined, { timeout: 20_000 }).catch(() => {});

    await this.page.evaluate(async () => {
      if (Array.isArray(laptops) && laptops.length) {
        // Relay mode: open a fresh session in the first online laptop's first
        // project via its proxied base (e.g. "/l/My-Mac"). lap.base is the exact
        // prefix the relay proxies to this connector.
        const lap = laptops.find((l) => l.online !== false) || laptops[0];
        const projs = await (await fetch(lap.base + '/api/projects')).json();
        if (!Array.isArray(projs) || !projs.length) throw new Error('no projects on laptop ' + lap.host);
        const key = projs[0].key;
        const cr = await fetch(lap.base + '/api/projects/' + key + '/sessions', { method: 'POST', body: '{}' });
        if (!cr.ok) throw new Error('create session failed: HTTP ' + cr.status + ' at ' + lap.base);
        const s = await cr.json();
        focusLaptop(lap.host); // sets the SPA's apiBase so later api() calls target this connector
        currentProject = key;
        selectSession(s.id);
      } else {
        // Mirror / direct mode (connector serves the SPA itself).
        await newSession();
      }
    });
    await this.waitForComposerReady();
  }

  async waitForComposerReady(): Promise<void> {
    await expect(this.page.locator('#input')).toBeEnabled({ timeout: 60_000 });
  }

  async sendMessage(text: string): Promise<void> {
    const input = this.page.locator('#input');
    await input.fill(text);
    await this.page.locator('#send').click();
  }

  // waitForTurn blocks until the agent needs a human decision (a question or an
  // approval) or the turn settles (composer re-enabled). It first waits for the
  // turn to actually engage so a fast SSE round-trip can't report "idle" before
  // the agent even started.
  async waitForTurn(): Promise<Pending> {
    const engaged = () =>
      this.page.waitForFunction(() => {
        const vis = (el: Element | null) => !!el && (el as HTMLElement).offsetParent !== null;
        const i = document.querySelector('#input') as HTMLTextAreaElement | null;
        return vis(document.querySelector('#question')) || vis(document.querySelector('#approval')) || !!(i && i.disabled);
      }, undefined, { timeout: 30_000 });
    await engaged().catch(() => {}); // instant turns may never flip to busy — that's fine

    await this.page.waitForFunction(() => {
      const vis = (el: Element | null) => !!el && (el as HTMLElement).offsetParent !== null;
      const i = document.querySelector('#input') as HTMLTextAreaElement | null;
      return vis(document.querySelector('#question')) || vis(document.querySelector('#approval')) || !!(i && !i.disabled);
    }, undefined, { timeout: 20 * 60 * 1000 });

    if (await this.page.locator('#question').isVisible()) return this.readQuestion();
    if (await this.page.locator('#approval').isVisible()) return this.readApproval();
    return { kind: 'none' };
  }

  private async readApproval(): Promise<Pending> {
    const summary = (await this.page.locator('#approval').innerText().catch(() => '')) || '';
    return { kind: 'approval', tool: '', summary: summary.trim(), diff: '' };
  }

  // readQuestion scrapes the question panel. Grouped asks render a tab per
  // question (.app-question-tab); a single ask has none.
  private async readQuestion(): Promise<Pending> {
    const p = this.page.locator('#question');
    const tabs = p.locator('.app-question-tab');
    const n = await tabs.count();
    const questions: QSpec[] = [];
    if (n > 0) {
      for (let i = 0; i < n; i++) {
        await tabs.nth(i).click();
        questions.push(await this.readActiveQuestion(p, true));
      }
    } else {
      questions.push(await this.readActiveQuestion(p, false));
    }
    return { kind: 'question', questions };
  }

  private async readActiveQuestion(p: Locator, grouped: boolean): Promise<QSpec> {
    const qSel = grouped ? '.app-question-qtext' : '.q';
    const question = (await p.locator(qSel).first().innerText().catch(() => '')).trim();
    const labels = p.locator('.app-question-option-label');
    const options: string[] = [];
    const c = await labels.count();
    for (let i = 0; i < c; i++) options.push((await labels.nth(i).innerText()).trim());
    const allowText = (await p.locator('textarea.app-question-text').count()) > 0;
    // Single-choice questions answer on click and have no Submit button; a Submit
    // button implies multi-select.
    const multiple = grouped || (await p.getByRole('button', { name: /^Submit/ }).count()) > 0;
    return { question, options, multiple, allowText };
  }

  async approve(): Promise<void> {
    await this.page.locator('#approval [data-d="approve"]').click();
  }
  async deny(): Promise<void> {
    await this.page.locator('#approval [data-d="deny"]').click();
  }

  // answerSingle answers a non-grouped question: click chosen option(s), optionally
  // type free text, then Submit/Send if present (single-choice already fired on click).
  async answerSingle(labels: string[], text?: string): Promise<void> {
    const p = this.page.locator('#question');
    for (const l of labels) {
      const opt = p.locator('.app-question-option', { hasText: l }).first();
      if (await opt.count()) await opt.click();
    }
    if (text) {
      const ta = p.locator('textarea.app-question-text');
      if (await ta.count()) await ta.fill(text);
    }
    const submit = p.getByRole('button', { name: /^(Submit|Send)$/ });
    if ((await submit.count()) && (await p.isVisible())) await submit.first().click();
  }

  // answerGroup answers the tabbed (questions[]) form: for each question select
  // its labels (typing anything that isn't an offered option into the free-text
  // box), then Submit all.
  async answerGroup(groups: string[][]): Promise<void> {
    const p = this.page.locator('#question');
    const tabs = p.locator('.app-question-tab');
    const n = await tabs.count();
    for (let i = 0; i < n; i++) {
      await tabs.nth(i).click();
      for (const val of groups[i] || []) {
        const opt = p.locator('.app-question-option', { hasText: val }).first();
        if (await opt.count()) await opt.click();
        else {
          const ta = p.locator('textarea.app-question-text');
          if (await ta.count()) await ta.fill(val);
        }
      }
    }
    await p.getByRole('button', { name: 'Submit all' }).click();
  }

  // latestAssistant returns the newest assistant message text (the agent's reply).
  async latestAssistant(): Promise<string> {
    const msgs = this.page.locator('#transcript .msg-assistant');
    const n = await msgs.count();
    if (!n) return '';
    return (await msgs.nth(n - 1).innerText()).trim();
  }

  // toolCards returns each tool card's full text — tool name + summary (header)
  // plus its parameters (the collapsed body). Uses textContent so collapsed args
  // are included regardless of expand state. For asserting which tools ran and on
  // what (e.g. "a write_file touched foo.md").
  async toolCards(): Promise<string[]> {
    const cards = this.page.locator('#transcript .file-card');
    const n = await cards.count();
    const out: string[] = [];
    for (let i = 0; i < n; i++) {
      out.push(((await cards.nth(i).textContent()) || '').replace(/\s+/g, ' ').trim());
    }
    return out;
  }

  // errors returns any error banners shown in the transcript (empty = none).
  async errors(): Promise<string[]> {
    const errs = this.page.locator('#transcript .msg-error');
    const n = await errs.count();
    const out: string[] = [];
    for (let i = 0; i < n; i++) out.push(((await errs.nth(i).innerText()) || '').trim());
    return out;
  }

  // questionOpen reports whether the ask_user panel is currently showing.
  async questionOpen(): Promise<boolean> {
    return this.page.locator('#question').isVisible();
  }

  // waitForBusy resolves once a turn is actually running — the Send button flips
  // to Stop (aria-label "Stop") while the agent works.
  async waitForBusy(timeout = 30_000): Promise<void> {
    await this.page.locator('#send[aria-label="Stop"]').waitFor({ state: 'visible', timeout });
  }

  // waitForIdle resolves once the turn finishes — the Stop button reverts to Send.
  async waitForIdle(timeout = 20 * 60 * 1000): Promise<void> {
    await this.page.locator('#send[aria-label="Send"]').waitFor({ state: 'visible', timeout });
  }

  // interrupt stops the running turn. The composer's Send button doubles as Stop
  // while busy, so clicking it mid-turn interrupts (the "changed my mind" gesture).
  async interrupt(): Promise<void> {
    await this.page.locator('#send[aria-label="Stop"]').click();
  }

  // queueMessage types a message and presses Enter WHILE the agent is busy, which
  // on a queue-capable session enqueues it (type-ahead) rather than interrupting.
  // (Clicking Send while busy would interrupt instead — hence Enter here.)
  async queueMessage(text: string): Promise<void> {
    const input = this.page.locator('#input');
    await input.fill(text);
    await input.press('Enter');
  }

  // queuedCount is how many type-ahead messages are waiting, read from the queue
  // strip ("queued (N) …"); 0 when the strip is hidden/empty.
  async queuedCount(): Promise<number> {
    const strip = this.page.locator('#queueStrip');
    if (!(await strip.isVisible())) return 0;
    const m = ((await strip.innerText()) || '').match(/queued \((\d+)\)/);
    return m ? Number(m[1]) : 0;
  }
}
