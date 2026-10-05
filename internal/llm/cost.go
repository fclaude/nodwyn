package llm

// Rates are a model's prices per million tokens, in USD, as its
// configuration states them (config.Model price_*) or its provider publishes
// them (internal/catalogue); nil is not stated. CachedInput is the rate for
// prompt tokens read from the cache and CacheWrite for those written to it;
// unstated, they are priced as Input. Tiers price a request whose prompt is
// longer than a tier's Above in its place, as OpenAI prices a long context.
type Rates struct {
	Input, CachedInput, CacheWrite, Output *float64
	Tiers                                  []RateTier
}

// RateTier is the rates of a request whose prompt holds more than Above
// tokens.
type RateTier struct {
	Above                                  int
	Input, CachedInput, CacheWrite, Output *float64
}

// Known reports whether the rates can price a request: input and output.
func (r Rates) Known() bool { return r.Input != nil && r.Output != nil }

// Cost is what a request cost, in USD: what the provider reported when it
// reported it, else an estimate from the rates, else nil (unknown). An
// estimate prices the prompt tokens read from the cache at CachedInput,
// those written to it at CacheWrite, the rest of the prompt at Input and the
// output, reasoning included, at Output, each at the rates of the longest
// tier the prompt is past. A reported cost always wins: a provider knows its
// own surcharges.
func Cost(u Usage, r Rates) (cost *float64, estimated bool) {
	if u.Cost != nil {
		c := *u.Cost
		return &c, false
	}
	if !r.Known() {
		return nil, false
	}
	rates := RateTier{Input: r.Input, CachedInput: r.CachedInput, CacheWrite: r.CacheWrite, Output: r.Output}
	for _, t := range r.Tiers {
		if u.PromptTokens > t.Above && t.Input != nil && t.Output != nil && t.Above >= rates.Above {
			rates = t
		}
	}
	cached := 0
	if u.CachedTokens != nil && *u.CachedTokens <= u.PromptTokens {
		cached = *u.CachedTokens
	}
	written := 0
	if cached+u.CacheWriteTokens <= u.PromptTokens {
		written = u.CacheWriteTokens
	}
	or := func(rate, otherwise *float64) float64 {
		if rate != nil {
			return *rate
		}
		return *otherwise
	}
	c := (float64(u.PromptTokens-cached-written)**rates.Input +
		float64(cached)*or(rates.CachedInput, rates.Input) +
		float64(written)*or(rates.CacheWrite, rates.Input) +
		float64(u.OutputTokens)**rates.Output) / 1e6
	return &c, true
}
