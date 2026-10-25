package kids

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
	_ "time/tzdata"

	"github.com/fclaude/nodwyn/internal/llm"
)

// Ledger reserves a complete bounded request before contacting the local model.
// A failed request or process death retains the reservation. Successful calls
// refund unused tokens/time only after the accounting update is durable.
type Ledger struct {
	mu        sync.Mutex
	path      string
	clock     func() time.Time
	zone      *time.Location
	maxTokens int64
	maxMS     int64
	state     allowance
}

type allowance struct {
	Day    string `json:"day"`
	Tokens int64  `json:"tokens"`
	MS     int64  `json:"milliseconds"`
}

func OpenLedger(dir string, tokens, minutes int64) (*Ledger, error) {
	if tokens <= 0 || tokens > 2000000 || minutes <= 0 || minutes > 60 {
		return nil, errors.New("invalid daily limits")
	}
	zone, err := time.LoadLocation("America/Los_Angeles")
	if err != nil {
		return nil, err
	}
	l := &Ledger{path: filepath.Join(dir, "allowance.json"), clock: time.Now, zone: zone, maxTokens: tokens, maxMS: minutes * 60000}
	err = readJSON(l.path, &l.state)
	marker := filepath.Join(dir, "accounting-initialised")
	if errors.Is(err, os.ErrNotExist) {
		if _, e := os.Stat(marker); !errors.Is(e, os.ErrNotExist) {
			return nil, errors.New("daily accounting is missing; parent recovery is required")
		}
		l.state.Day = l.clock().In(zone).Format("2006-01-02")
		if err = l.save(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	_, dayErr := time.Parse("2006-01-02", l.state.Day)
	if dayErr != nil || len(l.state.Day) != 10 || l.state.Tokens < 0 || l.state.MS < 0 || l.state.Tokens > l.maxTokens || l.state.MS > l.maxMS {
		return nil, errors.New("invalid daily accounting")
	}
	if err = atomicFile(marker, []byte("1\n")); err != nil {
		return nil, err
	}
	return l, nil
}
func (l *Ledger) save() error {
	b, err := json.Marshal(l.state)
	if err != nil {
		return err
	}
	return atomicFile(l.path, b)
}
func (l *Ledger) reserve(tokens, ms int64) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	day := l.clock().In(l.zone).Format("2006-01-02")
	if day > l.state.Day {
		l.state = allowance{Day: day}
	}
	if l.state.Tokens+tokens > l.maxTokens || l.state.MS+ms > l.maxMS {
		return "", errors.New("building is finished for today; you can build again tomorrow")
	}
	l.state.Tokens += tokens
	l.state.MS += ms
	return l.state.Day, l.save()
}
func (l *Ledger) settle(day string, tokens, ms int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if day == l.state.Day {
		l.state.Tokens -= tokens
		l.state.MS -= ms
	}
	return l.save()
}

// BoundedProvider is the only model path the managed core receives, including
// compaction and planning. No named model or task setting can escape it.
type BoundedProvider struct {
	upstream llm.Provider
	model    string
	ledger   *Ledger
}

func NewProvider(upstream llm.Provider, model string, ledger *Ledger) *BoundedProvider {
	return &BoundedProvider{upstream: upstream, model: model, ledger: ledger}
}
func (p *BoundedProvider) Name() string                             { return "ollama" }
func (p *BoundedProvider) Ping(ctx context.Context) (string, error) { return p.upstream.Ping(ctx) }
func (p *BoundedProvider) Chat(ctx context.Context, r llm.Request, onDelta func(llm.Delta)) (*llm.Response, error) {
	if r.Model != p.model || r.NumCtx <= 0 || r.NumCtx > 32768 || r.MaxTokens <= 0 || r.MaxTokens > 4096 {
		return nil, errors.New("managed: model request exceeds profile")
	}
	b, err := json.Marshal(r)
	if err != nil {
		return nil, err
	}
	if len(b) > 256<<10 {
		return nil, errors.New("conversation is too large")
	}
	const reserveTokens = int64(32768)
	const reserveMS = int64(120000)
	day, err := p.ledger.reserve(reserveTokens, reserveMS)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	size := 0
	response, err := p.upstream.Chat(ctx, r, func(d llm.Delta) {
		size += len(d.Text) + len(d.Thinking)
		if size > 2<<20 {
			cancel()
			return
		}
		if onDelta != nil {
			onDelta(d)
		}
	})
	if err != nil {
		return nil, err
	}
	if response == nil || ctx.Err() != nil {
		return nil, errors.New("model request interrupted")
	}
	used := int64(response.Usage.PromptTokens + response.Usage.OutputTokens)
	if response.Usage.Unreported || used <= 0 {
		used = reserveTokens
	}
	if used > reserveTokens || size > 2<<20 {
		return nil, errors.New("model exceeded its request allowance")
	}
	ms := min(time.Since(start).Milliseconds(), reserveMS)
	if err = p.ledger.settle(day, reserveTokens-used, reserveMS-ms); err != nil {
		return nil, err
	}
	return response, nil
}
