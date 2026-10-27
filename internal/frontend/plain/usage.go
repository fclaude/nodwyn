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
// cost appear only when known: the provider's reported share, or, when it
// reports none (older Ollama servers), the estimate from its prompt-eval time, and the
// cost when prices are configured; an estimate is marked ≈. The time
// appears only when the provider reports it, so an older log reads as it
// always did.
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
		parts = append(parts, fmt.Sprintf("%d%% cached", Percent(int64(*u.CachedTokens), int64(u.PromptTokens))))
	} else if u.EstimatedCachedTokens != nil && u.PromptTokens > 0 {
		// The provider reports no cache count (older Ollama servers): the estimate from
		// its prompt-eval time is shown marked, as a cost estimated from
		// configured prices.
		parts = append(parts, fmt.Sprintf("≈%d%% cached", Percent(int64(*u.EstimatedCachedTokens), int64(u.PromptTokens))))
	}
	if u.Cost != nil {
		parts = append(parts, USD(*u.Cost, u.CostEstimated))
	}
	// tok/s is output tokens per second of the request's wall time,
	// host-measured (wall_ms): every provider gets one, unlike total_ms,
	// which only Ollama reports. Comparing providers and models is the
	// point of the figure, so it counts the whole request, prompt
	// processing included, for all of them alike.
	if u.WallMS > 0 && u.OutputTokens > 0 && !u.Unreported {
		parts = append(parts, fmt.Sprintf("%.1f tok/s", float64(u.OutputTokens)/(float64(u.WallMS)/1000)))
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

// JudgeLine is one judge request as the transcript shows it: the kind of
// question, the judge's tokens and, with its prices, its estimated cost; or
// why it failed. "[judge choice: 318→34 tok · ≈$0.0002]". The kind is the
// tool without its judge- prefix; a log from before ADR 46 names jev-choice
// and the like, which show as they are.
func JudgeLine(p log.JevReq) string {
	kind := strings.TrimPrefix(p.Tool, "judge-")
	if !p.OK && p.Usage == nil {
		return fmt.Sprintf("[judge %s failed: %s]", kind, p.Error)
	}
	parts := []string{"no usage reported"}
	if u := p.Usage; u != nil {
		parts = []string{fmt.Sprintf("%s→%s tok", Thousands(int64(u.InputTokens)), Thousands(int64(u.OutputTokens)))}
		if u.Cost != nil {
			parts = append(parts, USD(*u.Cost, true))
		}
	}
	if !p.OK {
		parts = append(parts, "no answer: "+p.Error)
	}
	return fmt.Sprintf("[judge %s: %s]", kind, strings.Join(parts, " · "))
}

// JudgeTally adds judge requests up: how many, their tokens, and their cost
// as far as priced.
type JudgeTally struct {
	Calls, Tokens, Unpriced int64
	Cost                    float64
}

// Add counts one jev.req.
func (j *JudgeTally) Add(p log.JevReq) {
	j.Calls++
	if p.Usage == nil {
		return
	}
	j.Tokens += int64(p.Usage.InputTokens + p.Usage.OutputTokens)
	if p.Usage.Cost != nil {
		j.Cost += *p.Usage.Cost
	} else {
		j.Unpriced++
	}
}

// String is the tally for a person: "3 judge calls, 1,240 tokens, ≈$0.0012",
// "" for none. Costs are estimates from the judge's prices ([judge], else its provider's): the judge reports
// none.
func (j JudgeTally) String() string {
	if j.Calls == 0 {
		return ""
	}
	s := fmt.Sprintf("%d judge call", j.Calls)
	if j.Calls != 1 {
		s += "s"
	}
	s += ", " + Thousands(j.Tokens) + " tokens"
	switch {
	case j.Unpriced == 0 && j.Tokens > 0:
		s += ", " + USD(j.Cost, true)
	case j.Unpriced < j.Calls && j.Cost > 0:
		s += ", at least " + USD(j.Cost, true)
	case j.Unpriced > 0:
		s += ", not priced ([judge] price_input, price_output)"
	}
	return s
}

// JudgeSoFar is a turn's judge requests for a checkpoint, "" for none.
func JudgeSoFar(s budget.State) string {
	return JudgeTally{Calls: s.JudgeCalls, Tokens: s.JudgeTokens, Unpriced: s.JudgeUnpriced, Cost: s.JudgeCost}.String()
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

// Percent is part of whole as a rounded whole percentage, 0 for no whole.
func Percent(part, whole int64) int64 {
	if whole <= 0 {
		return 0
	}
	if part > whole {
		part = whole
	}
	return (part*100 + whole/2) / whole
}
