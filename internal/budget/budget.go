// Package budget tracks moony's cost across ALL sessions (not just the
// current turn/session) so a user can set a daily/monthly USD cap and get an
// early warning once it's crossed — the per-turn caps in
// internal/agent/agent_approval.go only ever see one turn at a time.
package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pysty1995/moony/internal/config"
)

// maxLedgerAgeDays bounds the ledger file's size: a year of daily floats plus
// slack for calendar-month rollups, pruned on every Record.
const maxLedgerAgeDays = 400

const dayFormat = "2006-01-02"

// ledgerPath resolves the ledger file location; a var so tests can point it
// at a temp file without touching the real ~/.config/moony.
var ledgerPath = func() (string, error) { return config.GlobalConfigPath("usage_ledger.json") }

// DailyBudget/MonthlyBudget read the USD caps from env (MN_DAILY_BUDGET /
// MN_MONTHLY_BUDGET). 0 means disabled — the default.
func DailyBudget() float64   { return envUSD("MN_DAILY_BUDGET") }
func MonthlyBudget() float64 { return envUSD("MN_MONTHLY_BUDGET") }

func envUSD(name string) float64 {
	v := strings.TrimSpace(os.Getenv(name))
	if v == "" {
		return 0
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil || f < 0 {
		return 0
	}
	return f
}

// Record adds cost to today's entry in the persisted cross-session ledger and
// returns the new running totals: today's total, and this calendar month's
// total (every day so far this month, including today).
//
// Best-effort by design: callers should treat a non-nil error as "skip the
// budget check this turn", never as a reason to fail the turn. A missing or
// corrupt ledger file just starts fresh rather than erroring.
//
// Not safe against two moony processes racing to Record concurrently (a rare
// last-write-wins clobber) — acceptable for an advisory warning, not a hard
// billing ledger.
func Record(cost float64) (daily, monthly float64, err error) {
	path, err := ledgerPath()
	if err != nil {
		return 0, 0, err
	}
	ledger := load(path)

	now := time.Now()
	today := now.Format(dayFormat)
	ledger[today] += cost
	prune(ledger, now)

	if err := save(path, ledger); err != nil {
		return 0, 0, err
	}

	monthPrefix := now.Format("2006-01")
	for day, c := range ledger {
		if strings.HasPrefix(day, monthPrefix) {
			monthly += c
		}
	}
	return ledger[today], monthly, nil
}

// prune drops entries older than maxLedgerAgeDays. "YYYY-MM-DD" sorts
// lexically in chronological order, so a plain string compare works.
func prune(ledger map[string]float64, now time.Time) {
	cutoff := now.AddDate(0, 0, -maxLedgerAgeDays).Format(dayFormat)
	for day := range ledger {
		if day < cutoff {
			delete(ledger, day)
		}
	}
}

func load(path string) map[string]float64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]float64{}
	}
	var m map[string]float64
	if err := json.Unmarshal(data, &m); err != nil || m == nil {
		return map[string]float64{}
	}
	return m
}

func save(path string, ledger map[string]float64) error {
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
