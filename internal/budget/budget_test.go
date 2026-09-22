package budget

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func testLimits() Limits {
	return Limits{ToolCalls: Limit{2, 5}, Tokens: Limit{10, 100}, Actions: Limit{2, 5}, Wall: Limit{int64(time.Hour), int64(2 * time.Hour)}}
}

func TestToolCheckpointsAndHardCeiling(t *testing.T) {
	var checkpoints []Checkpoint
	ctx, turn, done := Start(context.Background(), testLimits(), func(_ context.Context, p Checkpoint) (Choice, error) {
		checkpoints = append(checkpoints, p)
		return ParseChoice("y", p)
	})
	defer done()
	for i := 0; i < 5; i++ {
		if err := turn.ToolCall(ctx); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if len(checkpoints) != 2 || checkpoints[0].Used != 2 || checkpoints[1].Used != 4 {
		t.Fatalf("checkpoints: %+v", checkpoints)
	}
	if err := turn.ToolCall(ctx); err == nil || !strings.Contains(err.Error(), "hard stop") {
		t.Fatalf("hard ceiling: %v", err)
	}
	if ctx.Err() == nil || turn.state.ToolCalls != 5 {
		t.Fatal("hard stop did not cancel or exceeded allowance")
	}
}

func TestChoices(t *testing.T) {
	p := Checkpoint{Dimension: "tokens", Used: 20, Limit: Limit{10, 100}}
	for _, input := range []string{"a", "t", "0", "20", "10", "101", "garbage"} {
		if _, err := ParseChoice(input, p); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	choice, err := ParseChoice("25", p)
	if err != nil || !choice.Continue || choice.Soft != 25 {
		t.Fatalf("custom: %+v %v", choice, err)
	}
	choice, err = ParseChoice("yes", p)
	if err != nil || choice.Soft <= p.Used || choice.Soft > p.Limit.Hard {
		t.Fatalf("overshoot: %+v %v", choice, err)
	}
	p = Checkpoint{Dimension: "wall", Used: int64(time.Minute), Limit: Limit{int64(time.Minute), int64(time.Hour)}}
	choice, err = ParseChoice("5m", p)
	if err != nil || choice.Soft != int64(5*time.Minute) {
		t.Fatalf("wall: %+v %v", choice, err)
	}
	p = Checkpoint{Used: math.MaxInt64 - 2, Limit: Limit{math.MaxInt64 - 3, math.MaxInt64}}
	choice, err = ParseChoice("y", p)
	if err != nil || choice.Soft != math.MaxInt64 {
		t.Fatal("doubling overflowed")
	}
}

func TestActionsSnapshotsAndStop(t *testing.T) {
	var snapshot Checkpoint
	ctx, turn, done := Start(context.Background(), testLimits(), func(_ context.Context, p Checkpoint) (Choice, error) { snapshot = p; return Choice{}, nil })
	defer done()
	turn.Action(ctx, "a", "echo one")
	turn.Action(ctx, "a", "echo two")
	if err := turn.Action(ctx, "b", ""); err == nil {
		t.Fatal("decline did not stop")
	}
	if turn.state.Actions != 2 || len(snapshot.State.Files) != 1 || len(snapshot.State.Commands) != 2 {
		t.Fatalf("state: %+v", snapshot.State)
	}
	snapshot.State.Files[0] = "changed"
	if turn.state.Files[0] != "a" {
		t.Fatal("snapshot aliases accounting")
	}
}

func TestTokensAndWall(t *testing.T) {
	limits := testLimits()
	limits.Wall = Limit{int64(10 * time.Millisecond), int64(80 * time.Millisecond)}
	ctx, turn, done := Start(context.Background(), limits, func(_ context.Context, p Checkpoint) (Choice, error) { return ParseChoice("y", p) })
	defer done()
	// Exercise soft wall boundary deterministically, without sleeping until it.
	turn.start = time.Now().Add(-20 * time.Millisecond)
	if err := turn.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if turn.limits.Wall.Soft <= int64(20*time.Millisecond) {
		t.Fatal("wall did not advance")
	}
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("hard wall did not cancel")
	}
	if !strings.Contains(context.Cause(ctx).Error(), "wall") {
		t.Fatal(context.Cause(ctx))
	}
	ctx, turn, done2 := Start(context.Background(), testLimits(), nil)
	defer done2()
	turn.AddTokens(60, 60)
	if err := turn.Check(ctx); err == nil {
		t.Fatal("reported token hard ceiling ignored")
	}
	if turn.state.Tokens != 120 {
		t.Fatal("usage was capped instead of reporting actual overshoot")
	}
}

func TestCheckpointCancellationAndOverflow(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	ctx, turn, done := Start(parent, testLimits(), func(ctx context.Context, p Checkpoint) (Choice, error) {
		cancel()
		<-ctx.Done()
		return Choice{}, context.Cause(ctx)
	})
	defer done()
	turn.ToolCall(ctx)
	turn.ToolCall(ctx)
	if err := turn.ToolCall(ctx); err == nil || ctx.Err() == nil {
		t.Fatal("cancelled checkpoint continued")
	}
	_, turn, done2 := Start(context.Background(), testLimits(), nil)
	defer done2()
	turn.state.Tokens = math.MaxInt64 - 1
	turn.AddTokens(3, -1)
	if turn.state.Tokens != math.MaxInt64 {
		t.Fatal("token accounting overflowed")
	}
}

func TestFinalAnswerDoesNotAskForMoreSoftBudget(t *testing.T) {
	ctx, turn, done := Start(context.Background(), testLimits(), func(context.Context, Checkpoint) (Choice, error) {
		t.Fatal("finished turn asked to continue")
		return Choice{}, nil
	})
	defer done()
	turn.AddTokens(20, 20)
	if err := turn.Finish(ctx); err != nil {
		t.Fatal(err)
	}
	turn.AddTokens(60, 0)
	if err := turn.Finish(ctx); err == nil {
		t.Fatal("finished answer ignored hard token ceiling")
	}
}

func TestHardCeilingPrecedesOtherSoftCheckpoint(t *testing.T) {
	ctx, turn, done := Start(context.Background(), testLimits(), func(context.Context, Checkpoint) (Choice, error) {
		t.Fatal("asked for more budget after a hard ceiling")
		return Choice{}, nil
	})
	defer done()
	turn.start = time.Now().Add(-90 * time.Minute)
	turn.AddTokens(100, 0)
	if err := turn.Check(ctx); err == nil {
		t.Fatal("hard tokens did not stop")
	}
	ctx, turn, done2 := Start(context.Background(), testLimits(), func(context.Context, Checkpoint) (Choice, error) {
		t.Fatal("asked for more budget after action ceiling")
		return Choice{}, nil
	})
	defer done2()
	turn.state.Actions = 5
	turn.AddTokens(20, 0)
	if err := turn.Action(ctx, "x", ""); err == nil {
		t.Fatal("hard actions did not stop")
	}
}
