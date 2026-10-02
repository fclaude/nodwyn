package llm

// Rates are a model's prices per million tokens, in USD, as its
// configuration states them (config.Model price_*); nil is not stated.
// CachedInput is the rate for prompt tokens read from the cache; unstated,
// they are priced as Input.
type Rates struct {
	Input, CachedInput, Output *float64
}

// Known reports whether the rates can price a request: input and output.
func (r Rates) Known() bool { return r.Input != nil && r.Output != nil }

// Cost is what a request cost, in USD: what the provider reported when it
// reported it, else an estimate from the configured rates, else nil
// (unknown). An estimate prices cached prompt tokens at CachedInput, the
// rest of the prompt at Input and the output, reasoning included, at
// Output; it does not know a provider's surcharge for writing to its
// cache, which is why a reported cost always wins.
func Cost(u Usage, r Rates) (cost *float64, estimated bool) {
	if u.Cost != nil {
		c := *u.Cost
		return &c, false
	}
	if !r.Known() {
		return nil, false
	}
	cached := 0
	if u.CachedTokens != nil && *u.CachedTokens <= u.PromptTokens {
		cached = *u.CachedTokens
	}
	cachedRate := *r.Input
	if r.CachedInput != nil {
		cachedRate = *r.CachedInput
	}
	c := (float64(u.PromptTokens-cached)**r.Input + float64(cached)*cachedRate + float64(u.OutputTokens)**r.Output) / 1e6
	return &c, true
}
