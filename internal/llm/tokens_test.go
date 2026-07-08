package llm

import "testing"

func TestTokenEstimatorFallback(t *testing.T) {
	var e TokenEstimator
	if got := e.Estimate(4000); got != 1000 {
		t.Errorf("uncalibrated Estimate(4000) = %d, want 1000 (chars/4)", got)
	}
	if cpt := e.CharsPerToken(); cpt != defaultCharsPerToken {
		t.Errorf("uncalibrated CharsPerToken = %v, want %v", cpt, defaultCharsPerToken)
	}
	if got := e.Estimate(-5); got != 0 {
		t.Errorf("Estimate(negative) = %d, want 0", got)
	}
}

func TestTokenEstimatorRejectsAnomalies(t *testing.T) {
	var e TokenEstimator
	e.Observe(0, 100)      // non-positive chars
	e.Observe(1000, 0)     // non-positive tokens
	e.Observe(1000, 5000)  // chars/token 0.2 < 0.5
	e.Observe(100000, 100) // chars/token 1000 > 8
	if got := e.Estimate(8000); got != 2000 {
		t.Errorf("after only anomalies Estimate should stay chars/4=2000, got %d", got)
	}
}

func TestTokenEstimatorLinearFit(t *testing.T) {
	var e TokenEstimator
	// Points (8000,3000) and (16000,5000) → slope 0.25, intercept 1000.
	e.Observe(8000, 3000)
	if got := e.Estimate(8000); got != 2000 {
		t.Fatalf("one observation should not fit yet; got %d, want 2000", got)
	}
	e.Observe(16000, 5000)
	if got := e.Estimate(8000); got < 2998 || got > 3002 {
		t.Errorf("Estimate(8000) = %d, want ~3000", got)
	}
	if got := e.Estimate(24000); got < 6996 || got > 7004 {
		t.Errorf("Estimate(24000) = %d, want ~7000", got)
	}
	if cpt := e.CharsPerToken(); cpt < 3.9 || cpt > 4.1 {
		t.Errorf("CharsPerToken = %v, want ~4", cpt)
	}
}

// With no spread in char counts the slope can't be identified, so the estimator
// keeps the default ratio and learns only the intercept from the means.
func TestTokenEstimatorIllConditioned(t *testing.T) {
	var e TokenEstimator
	e.Observe(8000, 3000)
	e.Observe(8000, 3000)
	// slope defaults to 0.25 → intercept = 3000 - 0.25*8000 = 1000.
	if got := e.Estimate(8000); got < 2998 || got > 3002 {
		t.Errorf("ill-conditioned Estimate(8000) = %d, want ~3000", got)
	}
}

func TestMessageChars(t *testing.T) {
	msgs := []Message{
		UserText("hello"), // 5
		{Role: RoleUser, Content: []ContentBlock{ToolResultBlock("id", "output!", false)}}, // 7
	}
	if got := MessageChars(msgs); got != 12 {
		t.Errorf("MessageChars = %d, want 12", got)
	}
}
