package core

import (
	"context"

	"github.com/fclaude/nibb/internal/log"
	"github.com/fclaude/nibb/internal/scheme"
)

// StartEval is the asynchronous, serialized frontend entrypoint for user
// Scheme. Eval remains available to the existing in-process frontend. Source
// and result are redacted on the durable path; neither enters model context.
func (s *Session) StartEval(source, actor string) error {
	return s.startTurn(func(ctx context.Context) error {
		if _, err := s.Record(log.KindEvalReq, log.EvalRequest{Source: source, Actor: actor}); err != nil {
			return err
		}
		value, err := s.Eval(ctx, source)
		result := log.EvalResult{}
		if err != nil {
			result.Error = err.Error()
		} else if value.Kind != scheme.KVoid {
			result.Value = scheme.Write(value)
		}
		if _, recordErr := s.Record(log.KindEvalResult, result); recordErr != nil {
			return recordErr
		}
		return err
	})
}
