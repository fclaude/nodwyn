package plain

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
)

// UsageLine is one model request's usage as the TUI and line mode show it:
// "12,400→800 tok · 78% cached · $0.012 · 3.2s". The cache hit rate and the
// cost appear only when known (a provider that reports them, or configured
// prices: an estimate is marked ≈), the time only when the provider
// reports one, so an Ollama request or an older log reads as it always did.
func UsageLine(u log.Usage) string {
	tok := fmt.Sprintf("%s→%s tok", Thousands(int64(u.PromptTokens)), Thousands(int64(u.OutputTokens)))
	if u.Unreported {
		tok = "tokens not reported"
	}
	if u.ReasoningTokens > 0 {
		// Reasoning is part of the output the provider counted.
		tok += fmt.Sprintf(" (%s reasoning)", Thousands(int64(u.ReasoningTokens)))
	}
	parts := []string{tok}
	if u.CachedTokens != nil && u.PromptTokens > 0 {
		parts = append(parts, fmt.Sprintf("%d%% cached", percent(int64(*u.CachedTokens), int64(u.PromptTokens))))
	}
	if u.Cost != nil {
		parts = append(parts, USD(*u.Cost, u.CostEstimated))
	}
	if u.TotalMS > 0 {
		parts = append(parts, fmt.Sprintf("%.1fs", float64(u.TotalMS)/1000))
	}
	return strings.Join(parts, " · ")
}

// USD writes a cost: to a tenth of a cent under a dollar, to the cent
// above, with ≈ when it is an estimate from configured prices.
func USD(c float64, estimated bool) string {
	s := ""
	switch {
	case c > 0 && c < 0.0001:
		s = "<$0.0001"
	case c < 0.01:
		s = fmt.Sprintf("$%.4f", c)
	case c < 1:
		s = fmt.Sprintf("$%.3f", c)
	default:
		s = fmt.Sprintf("$%.2f", c)
	}
	if estimated {
		return "≈" + s
	}
	return s
}

// SpendSoFar is a turn's known spend for a checkpoint: "$0.012 so far",
// "at least $0.012 so far" when some requests have no known cost, or ""
// when none has one.
func SpendSoFar(s budget.State) string {
	switch {
	case s.Priced == 0:
		return ""
	case s.Unpriced > 0:
		return "at least " + USD(s.Cost, false) + " so far"
	}
	return USD(s.Cost, false) + " so far"
}

// Thousands writes n with commas.
func Thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	start := 0
	if n < 0 {
		start = 1
	}
	for i := len(s) - 3; i > start; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func percent(part, whole int64) int64 {
	if whole <= 0 {
		return 0
	}
	if part > whole {
		part = whole
	}
	return (part*100 + whole/2) / whole
}
