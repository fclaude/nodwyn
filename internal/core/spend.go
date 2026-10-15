package core

import (
	"encoding/json"
	"fmt"

	"github.com/fclaude/nodwyn/internal/budget"
	"github.com/fclaude/nodwyn/internal/log"
)

// A spend limit ([budget] cost_hard, optional) bounds what a task's model
// requests cost, as far as anyone knows: the costs llm.resp records carry,
// reported by the provider or priced from the model's price_*. It counts the
// whole task, a resumed one's history and every turn of a run included.

// knownSpend is what the task's model and judge requests have cost so far,
// as far as known: a resumed session's requests it does not hold in memory
// (spendBefore, from the log's index) and those it holds, added in the order
// the log has them, as adding up the whole log would.
func (s *Session) knownSpend() float64 {
	total := s.spendBefore
	for _, ev := range s.EventsSoFar() {
		if cost, ok := knownCost(ev); ok {
			total += cost
		}
	}
	return total
}

// knownCost is what one model or judge request cost, when it is known.
func knownCost(ev log.Event) (float64, bool) {
	if ev.Kind != log.KindLLMResp && ev.Kind != log.KindJevReq {
		return 0, false
	}
	var r struct {
		Usage *struct {
			Cost *float64 `json:"cost"`
		} `json:"usage"`
	}
	if json.Unmarshal(ev.Payload, &r) == nil && r.Usage != nil && r.Usage.Cost != nil && *r.Usage.Cost > 0 {
		return *r.Usage.Cost, true
	}
	return 0, false
}

// endSpend keeps what a turn did to the spend limit: the soft limit a
// checkpoint raised holds for later turns. The first turn with a request
// whose cost nobody knows says so once: the limit cannot count it.
func (s *Session) endSpend(turn *budget.Turn) {
	_, unpriced, limit := turn.Spend()
	judge := turn.Snapshot().JudgeUnpriced
	s.mu.Lock()
	s.costSoft = limit.Soft
	noted, judgeNoted := s.spendNoted, s.judgeSpendNoted
	s.spendNoted = noted || unpriced > 0
	s.judgeSpendNoted = judgeNoted || judge > 0
	s.mu.Unlock()
	if unpriced > 0 && !noted {
		_, _ = s.Record(log.KindNote, log.Note{Text: fmt.Sprintf("A spend limit is set (budget.cost_hard), but %d model request(s) this turn had no known cost: the provider reported none, and the model has no prices (Settings › Models, or price_* in its entry in config.toml). The limit does not count them.", unpriced)})
	}
	if judge > 0 && !judgeNoted {
		_, _ = s.Record(log.KindNote, log.Note{Text: fmt.Sprintf("A spend limit is set (budget.cost_hard), but %d judge request(s) this turn had no price: the judge reports what it used, not what it cost, and [judge] has no price_input and price_output in config.toml, nor does its provider publish them. The limit does not count them.", judge)})
	}
}
