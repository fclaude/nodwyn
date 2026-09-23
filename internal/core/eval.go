package core

import (
	"context"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/redact"
	"github.com/fclaude/nibb/internal/scheme"
)

// StartEval is the asynchronous, serialized frontend entrypoint for user
// Scheme. Eval remains available to the existing in-process frontend. Source
// and result are redacted on the durable path; neither enters model context.
func (s *Session) StartEval(source, actor string) error {
	return s.startTurn(func(ctx context.Context) error {
		_, err := s.evalRecorded(ctx, source, actor)
		return err
	})
}

func (s *Session) evalRecorded(ctx context.Context, source, actor string) (scheme.Value, error) {
	return s.recordEvaluation(source, actor, func() (scheme.Value, error) { return s.Eval(ctx, source) })
}

// recordEvaluation keeps both ordinary Scheme and fixed host entrypoints on the
// same redacted evaluation history path.
func (s *Session) recordEvaluation(source, actor string, evaluate func() (scheme.Value, error)) (scheme.Value, error) {
	if _, err := s.Record(log.KindEvalReq, log.EvalRequest{Source: source, Actor: actor}); err != nil {
		return scheme.Void, err
	}
	value, err := evaluate()
	result := log.EvalResult{}
	var redactions []redact.Event
	if err != nil {
		result.Error = err.Error()
	} else if value.Kind != scheme.KVoid {
		// Scan decoded strings before Scheme escaping can hide token boundaries.
		result.Value = scheme.WriteWithStrings(value, func(text string) string {
			clean := redact.Redact(text, nil)
			redactions = append(redactions, clean.Events...)
			return clean.Text
		})
	}
	record, recordErr := s.Record(log.KindEvalResult, result)
	if recordErr != nil {
		return scheme.Void, recordErr
	}
	if auditErr := s.recordRedactions(record.Seq, redactions); auditErr != nil {
		return scheme.Void, auditErr
	}
	return value, err
}
