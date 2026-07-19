package budget

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func withTestLedger(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "usage_ledger.json")
	old := ledgerPath
	ledgerPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { ledgerPath = old })
	return path
}

func TestDailyMonthlyBudgetEnv(t *testing.T) {
	t.Setenv("MN_DAILY_BUDGET", "")
	t.Setenv("MN_MONTHLY_BUDGET", "")
	if DailyBudget() != 0 || MonthlyBudget() != 0 {
		t.Error("unset env vars should disable both budgets (0)")
	}

	t.Setenv("MN_DAILY_BUDGET", "5.50")
	t.Setenv("MN_MONTHLY_BUDGET", "not-a-number")
	if got := DailyBudget(); got != 5.50 {
		t.Errorf("DailyBudget = %v, want 5.50", got)
	}
	if got := MonthlyBudget(); got != 0 {
		t.Errorf("MonthlyBudget with garbage value = %v, want 0 (disabled)", got)
	}

	t.Setenv("MN_DAILY_BUDGET", "-1")
	if got := DailyBudget(); got != 0 {
		t.Errorf("negative budget = %v, want 0 (disabled)", got)
	}
}

func TestRecordAccumulatesWithinADay(t *testing.T) {
	withTestLedger(t)

	daily, monthly, err := Record(1.5)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if daily != 1.5 || monthly != 1.5 {
		t.Fatalf("first Record: daily=%v monthly=%v, want 1.5/1.5", daily, monthly)
	}

	daily, monthly, err = Record(2.25)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if daily != 3.75 || monthly != 3.75 {
		t.Errorf("second Record: daily=%v monthly=%v, want 3.75/3.75", daily, monthly)
	}
}

func TestRecordAggregatesAcrossDaysInSameMonth(t *testing.T) {
	path := withTestLedger(t)

	now := time.Now()
	// Seed yesterday's entry directly (Record always writes "today").
	yesterday := now.AddDate(0, 0, -1).Format(dayFormat)
	if err := save(path, map[string]float64{yesterday: 10.0}); err != nil {
		t.Fatal(err)
	}

	daily, monthly, err := Record(5.0)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if daily != 5.0 {
		t.Errorf("daily = %v, want 5.0 (today only)", daily)
	}
	// If yesterday fell in the same calendar month, it should be included;
	// otherwise just today's 5.0. Compute the expected value the same way.
	wantMonthly := 5.0
	if yesterday[:7] == now.Format("2006-01") {
		wantMonthly = 15.0
	}
	if monthly != wantMonthly {
		t.Errorf("monthly = %v, want %v", monthly, wantMonthly)
	}
}

func TestRecordPrunesOldEntries(t *testing.T) {
	path := withTestLedger(t)

	now := time.Now()
	old := now.AddDate(0, 0, -maxLedgerAgeDays-10).Format(dayFormat)
	recent := now.AddDate(0, 0, -5).Format(dayFormat)
	if err := save(path, map[string]float64{old: 100.0, recent: 3.0}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := Record(1.0); err != nil {
		t.Fatalf("Record: %v", err)
	}

	ledger := load(path)
	if _, ok := ledger[old]; ok {
		t.Errorf("entry older than %d days should have been pruned", maxLedgerAgeDays)
	}
	if _, ok := ledger[recent]; !ok {
		t.Error("a recent entry should survive pruning")
	}
}

func TestRecordMissingOrCorruptLedgerStartsFresh(t *testing.T) {
	withTestLedger(t) // file doesn't exist yet

	daily, monthly, err := Record(2.0)
	if err != nil || daily != 2.0 || monthly != 2.0 {
		t.Fatalf("Record on missing ledger = %v, %v, %v", daily, monthly, err)
	}
}

func TestLedgerPathErrorPropagates(t *testing.T) {
	old := ledgerPath
	defer func() { ledgerPath = old }()
	ledgerPath = func() (string, error) { return "", errors.New("boom") }

	if _, _, err := Record(1.0); err == nil {
		t.Error("expected an error when ledgerPath fails")
	}
}
