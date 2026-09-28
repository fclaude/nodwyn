package window

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/fclaude/nibb/internal/llm"
	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
)

// RecipeVersion names how windows are built. It is recorded with the window
// recipe, so a replay can refuse a recipe it does not know. v3 added
// harness messages, v4 narrowed contexts, v5 folded ones, v6 records the
// task and plan headers (default when absent), v7 requests that offer no
// tools (llm.req no_tools), and v8 the cap on generated tokens (max_output);
// an older log has none of them, so Build rebuilds it unchanged.
const RecipeVersion = "window-v8"

// RequestHash fingerprints the provider-neutral request, including tool schemas
// and model options. It verifies reconstruction; it is not a tamper-proof seal.
func RequestHash(req llm.Request) (string, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// ReplayRequest reconstructs one recorded turn request using only its preceding
// log prefix. It never loads current configuration, runs tools or contacts a
// model. Legacy logs without the recipe/hash cannot prove exact reconstruction.
func ReplayRequest(events []log.Event, seq uint64) (llm.Request, error) {
	var zero llm.Request
	prefix := make([]log.Event, 0, len(events))
	var runtime log.SessionStart
	for _, ev := range events {
		if len(ev.Payload) > 0 {
			raw, _, err := redact.JSON(ev.Payload, nil)
			if err != nil {
				return zero, fmt.Errorf("window replay: invalid event %d: %w", ev.Seq, err)
			}
			ev.Payload = raw
		}
		if ev.Seq == seq {
			if ev.Kind != log.KindLLMReq {
				return zero, fmt.Errorf("window replay: seq %d is not llm.req", seq)
			}
			var rec log.LLMReq
			if err := ev.Decode(&rec); err != nil {
				return zero, err
			}
			c := runtime.Context
			if c == nil || rec.RequestHash == "" {
				return zero, fmt.Errorf("window replay: this legacy request lacks recorded context or a request hash")
			}
			switch c.Version {
			case "window-v1", "window-v2", "window-v3", "window-v4", "window-v5", "window-v6", "window-v7", RecipeVersion:
			default:
				return zero, fmt.Errorf("window replay: unsupported recipe %q", c.Version)
			}
			var tools []llm.Tool
			if err := json.Unmarshal(c.Tools, &tools); err != nil {
				return zero, fmt.Errorf("window replay: invalid tool catalog: %w", err)
			}
			canonical, err := json.Marshal(tools)
			if err != nil {
				return zero, err
			}
			build := Build
			if c.Version == "window-v1" {
				build = buildV1
			}
			built := build(prefix, c.System, Estimate(string(canonical)), Policy{HighWater: c.HighWater, LowWater: c.LowWater, MaxResultTokens: c.MaxResultTokens,
				TaskHeader: c.TaskHeader, PlanHeader: c.PlanHeader})
			req := llm.Request{Model: rec.Model, Messages: built.Messages, Tools: tools, NumCtx: c.NumCtx, KeepAlive: c.KeepAlive, MaxTokens: c.MaxOutput}
			if rec.NoTools {
				req.Tools = nil
			} else if len(rec.OfferedTools) > 0 {
				req.Tools = nil
				for _, t := range tools {
					if slices.Contains(rec.OfferedTools, t.Name) {
						req.Tools = append(req.Tools, t)
					}
				}
			}
			if rec.Endpoint != "" {
				req.NumCtx, req.KeepAlive = rec.NumCtx, rec.KeepAlive
			}
			digest, err := RequestHash(req)
			if err != nil {
				return zero, err
			}
			if digest != rec.RequestHash {
				return zero, fmt.Errorf("window replay: reconstructed request does not match recorded hash at seq %d", seq)
			}
			return req, nil
		}
		if ev.Kind == log.KindSessionStart || ev.Kind == log.KindSessionResume {
			// Missing context on a resume intentionally clears the preceding recipe.
			var next log.SessionStart
			if err := ev.Decode(&next); err != nil {
				return zero, err
			}
			runtime = next
		}
		prefix = append(prefix, ev)
	}
	return zero, fmt.Errorf("window replay: no request at seq %d", seq)
}
