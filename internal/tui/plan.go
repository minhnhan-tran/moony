package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/pysty1995/moony/internal/agent"
	"github.com/pysty1995/moony/internal/session"
)

// autoPlanNotice is shown when AutoPlan auto-enters plan mode for a large task.
const autoPlanNotice = "This looks like a sizable change — switched to plan mode (read-only) to draft a plan first. " +
	"Review it, then approve to build (or /build to skip planning)."

// planCmd dispatches the /plan command and its subcommands. The plan is a
// first-class, session-persisted artifact (distinct from the ephemeral todo
// list): the agent drafts it in read-only plan mode, the user amends and
// approves it, then the agent executes the approved plan.
//
//	/plan                 show the current plan (or how to start one)
//	/plan <description>   draft a plan for <description> (enters plan mode)
//	/plan add <step>      append a step to the draft
//	/plan clear           discard the plan
//	/plan approve         approve the draft and start executing it
//	/plan auto [on|off]   toggle auto-entering plan mode for large tasks
func (m Model) planCmd(text string, fields []string) (tea.Model, tea.Cmd) {
	// Bare /plan with no argument: show status.
	if len(fields) < 2 {
		m.trans.add(entry{kind: entryInfo, text: m.agent.PlanStatusLine()})
		m.publishStatus()
		m.refresh()
		return m, nil
	}

	sub := strings.ToLower(strings.TrimSpace(fields[1]))
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(text, fields[0])), fields[1]))

	switch sub {
	case "approve", "ok", "go", "accept", "build", "execute":
		if !m.agent.ApprovePlan() {
			m.trans.add(entry{kind: entryInfo, text: "No plan to approve. Use /plan <description> to draft one first."})
			break
		}
		m.plan = m.agent.Plan()
		m.agent.SetPlanMode(false) // executing means leaving read-only mode
		m.trans.add(entry{kind: entryInfo, text: "Plan approved — executing it now. Mutating tools are enabled again."})
		return m.runUserMessage("/plan approve", agent.PlanExecutePrompt)

	case "clear", "off", "none", "remove", "discard", "cancel":
		m.agent.ClearPlan()
		m.plan = nil
		m.trans.add(entry{kind: entryInfo, text: "Plan cleared."})

	case "add", "+":
		if rest == "" {
			m.trans.add(entry{kind: entryInfo, text: "Usage: /plan add <step description>"})
			break
		}
		p := m.agent.Plan()
		if p == nil {
			p = &session.Plan{Status: session.PlanDraft}
		}
		p.Steps = append(p.Steps, session.PlanStep{Title: rest})
		p.Status = session.PlanDraft // adding a step un-approves it
		m.agent.SetPlan(p)
		m.plan = m.agent.Plan()
		m.trans.add(entry{kind: entryInfo, text: m.agent.PlanStatusLine()})

	case "show", "status", "view":
		m.trans.add(entry{kind: entryInfo, text: m.agent.PlanStatusLine()})

	case "auto":
		switch strings.ToLower(rest) {
		case "", "show", "status":
			if m.cfg.AutoPlan {
				m.trans.add(entry{kind: entryInfo, text: "Auto-plan is ON — large/multi-step tasks auto-enter plan mode. /plan auto off to disable."})
			} else {
				m.trans.add(entry{kind: entryInfo, text: "Auto-plan is OFF. /plan auto on makes large/multi-step tasks auto-enter plan mode first."})
			}
		case "on", "true", "yes", "1", "enable":
			m.cfg.AutoPlan = true
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig("Auto-plan ON — large/multi-step tasks will auto-enter plan mode")})
		case "off", "false", "no", "0", "disable":
			m.cfg.AutoPlan = false
			m.trans.add(entry{kind: entryInfo, text: m.saveConfig("Auto-plan OFF")})
		default:
			m.trans.add(entry{kind: entryInfo, text: "Usage: /plan auto [on|off]"})
		}

	default:
		// Anything else is treated as a free-text description to plan: enter
		// read-only plan mode and ask the model to draft a plan via plan_write.
		desc := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
		m.agent.SetPlanMode(true)
		m.trans.add(entry{kind: entryInfo, text: "Plan mode ON (read-only) — drafting a plan. Review it, then /plan approve to execute (or /plan clear to discard)."})
		return m.runUserMessage("/plan "+desc, agent.PlanDraftPrompt(desc))
	}

	m.publishStatus()
	m.refresh()
	return m, nil
}
