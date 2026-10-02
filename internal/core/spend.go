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

// knownSpend is what the task's model requests have cost so far, as far as
// known.
func (s *Session) knownSpend() float64 {
	var total float64
	for _, ev := range s.EventsSoFar() {
		if ev.Kind != log.KindLLMResp {
			continue
		}
		var r struct {
			Usage struct {
				Cost *float64 `json:"cost"`
			} `json:"usage"`
		}
		if json.Unmarshal(ev.Payload, &r) == nil && r.Usage.Cost != nil && *r.Usage.Cost > 0 {
			total += *r.Usage.Cost
		}
	}
	return total
}

// endSpend keeps what a turn did to the spend limit: the soft limit a
// checkpoint raised holds for later turns. The first turn with a request
// whose cost nobody knows says so once: the limit cannot count it.
func (s *Session) endSpend(turn *budget.Turn) {
	_, unpriced, limit := turn.Spend()
	s.mu.Lock()
	s.costSoft = limit.Soft
	noted := s.spendNoted
	if unpriced > 0 {
		s.spendNoted = true
	}
	s.mu.Unlock()
	if unpriced > 0 && !noted {
		_, _ = s.Record(log.KindNote, log.Note{Text: fmt.Sprintf("A spend limit is set (budget.cost_hard), but %d model request(s) this turn had no known cost: the provider reported none, and the model has no price_* in config.toml. The limit does not count them.", unpriced)})
	}
}
