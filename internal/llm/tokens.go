package llm

import "math"

// TokenEstimator predicts a request's input-token count from the character count
// of its conversation, learning the model's real chars-per-token ratio and its
// fixed overhead (system prompt + tool schemas + checkpoint) online from actual
// provider usage reports. It models
//
//	tokens ≈ slope·chars + intercept
//
// and fits slope and intercept by forgetting (EWMA-weighted) least squares.
// Because a session's conversation grows each turn, successive observations span
// a range of char counts, which is what lets the fit separate the per-char slope
// from the fixed intercept — something the old additive-overhead model could not
// do (a constant can't correct a proportional error in a bytes/4 guess).
//
// The zero value is ready to use and, until it has learned enough, falls back to
// the classic chars/4 approximation. Not safe for concurrent use; the agent
// updates and reads it on its single Run goroutine.
type TokenEstimator struct {
	// EWMA-decayed regression sums over (x = conversation chars, y = tokens).
	n, sx, sy, sxx, sxy float64
}

const (
	// estDecay is the per-observation forgetting factor: older observations lose
	// weight so the fit tracks drift (model/provider/content-mix changes).
	estDecay = 0.85
	// defaultCharsPerToken is the fallback ratio while uncalibrated or when the
	// fit is ill-conditioned (too little spread in char counts to trust a slope).
	defaultCharsPerToken = 4.0
	// minSlope/maxSlope clamp the learned tokens-per-char (~2–6.7 chars/token),
	// covering prose, code, and JSON tool args across tokenizers and keeping a
	// noisy early fit from running away.
	minSlope = 0.15
	maxSlope = 0.5
	// minSpread is the minimum coefficient of variation in char counts required
	// before the slope is trusted; below it we keep the default ratio.
	minSpread = 0.05
)

// Observe folds one real usage report — conversation characters chars and the
// provider-reported input tokens — into the fit. Non-positive or wildly
// inconsistent reports (overall chars/token outside ~0.5–8) are ignored so a
// single anomaly can't poison the model.
func (e *TokenEstimator) Observe(chars, actualTokens int) {
	if chars <= 0 || actualTokens <= 0 {
		return
	}
	if cpt := float64(chars) / float64(actualTokens); cpt < 0.5 || cpt > 8 {
		return
	}
	x, y := float64(chars), float64(actualTokens)
	e.n = estDecay*e.n + 1
	e.sx = estDecay*e.sx + x
	e.sy = estDecay*e.sy + y
	e.sxx = estDecay*e.sxx + x*x
	e.sxy = estDecay*e.sxy + x*y
}

// Estimate returns the predicted input tokens for a conversation spanning chars
// characters, including the learned fixed overhead. Before calibration (or when
// the fit is ill-conditioned) it falls back to chars/4.
func (e *TokenEstimator) Estimate(chars int) int {
	if chars < 0 {
		chars = 0
	}
	slope, intercept, ok := e.fit()
	if !ok {
		return int(float64(chars) / defaultCharsPerToken)
	}
	t := slope*float64(chars) + intercept
	if t < 0 {
		t = 0
	}
	return int(t)
}

// CharsPerToken reports the currently learned characters-per-token ratio (for
// telemetry/UI); it returns the default until calibrated.
func (e *TokenEstimator) CharsPerToken() float64 {
	if slope, _, ok := e.fit(); ok && slope > 0 {
		return 1 / slope
	}
	return defaultCharsPerToken
}

// fit solves the forgetting least-squares for slope/intercept. It reports ok
// only once it has ≥2 effective observations. When there is too little spread in
// char counts to identify a slope, it keeps the default ratio and learns only
// the intercept from the running means.
func (e *TokenEstimator) fit() (slope, intercept float64, ok bool) {
	if e.n < 1.5 {
		return 0, 0, false
	}
	meanX := e.sx / e.n
	meanY := e.sy / e.n
	den := e.n*e.sxx - e.sx*e.sx // = n² · var(x)
	if den > 0 && meanX > 0 && math.Sqrt(den)/e.n >= minSpread*meanX {
		slope = (e.n*e.sxy - e.sx*e.sy) / den
		if slope < minSlope {
			slope = minSlope
		} else if slope > maxSlope {
			slope = maxSlope
		}
	} else {
		slope = 1 / defaultCharsPerToken // not enough spread to trust a slope
	}
	intercept = meanY - slope*meanX
	if intercept < 0 {
		intercept = 0
	}
	return slope, intercept, true
}

// MessageChars sums the character length of all textual content across messages
// (assistant/user text, tool_result content, and tool_use input JSON) — the
// cheap size proxy a TokenEstimator converts into a token count.
func MessageChars(messages []Message) int {
	chars := 0
	for _, m := range messages {
		for _, c := range m.Content {
			chars += len(c.Text) + len(c.Content) + len(c.Input)
		}
	}
	return chars
}
