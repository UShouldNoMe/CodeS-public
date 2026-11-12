package Agent

import (
	"CodeS/internal/conversation"
	"CodeS/internal/tools"
	"CodeS/internal/tools/manager"
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

type executorTestTool struct {
	def tools.ToolDef
	run func(context.Context, map[string]any) ([]any, error)
}

func (t executorTestTool) Definition() tools.ToolDef { return t.def }
func (t executorTestTool) Execute(ctx context.Context, args map[string]any) ([]any, error) {
	return t.run(ctx, args)
}

type executorTestOutcome struct {
	results []conversation.ToolResult
	err     error
}

func executorTestContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx, cancel
}

func executorTestReceive[T any](t *testing.T, ctx context.Context, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatalf("timed out waiting for test synchronization: %v", ctx.Err())
		var zero T
		return zero
	}
}

func executorTestStart(e *ToolExecutor, ctx context.Context, calls []conversation.ToolCall, emit Emitter) <-chan executorTestOutcome {
	done := make(chan executorTestOutcome, 1)
	go func() {
		results, err := e.Execute(ctx, 7, calls, emit)
		done <- executorTestOutcome{results, err}
	}()
	return done
}

func executorTestRegister(t *testing.T, catalog *manager.Manager, tool executorTestTool) {
	t.Helper()
	if err := catalog.Register(tool); err != nil {
		t.Fatal(err)
	}
}

func TestToolExecutorParallelCompletionAndResultOrder(t *testing.T) {
	ctx, _ := executorTestContext(t)
	catalog := manager.NewRegistry()
	started := make(chan string, 2)
	gates := map[string]chan struct{}{"slow": make(chan struct{}), "fast": make(chan struct{})}
	executorTestRegister(t, catalog, executorTestTool{
		def: tools.ToolDef{Name: "Read", Authority: tools.CategoryRead, CouldBeParalleled: true},
		run: func(ctx context.Context, args map[string]any) ([]any, error) {
			id := args["id"].(string)
			started <- id
			select {
			case <-gates[id]:
				return []any{id}, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	})
	calls := []conversation.ToolCall{
		{ID: "slow", Name: "Read", Arguments: map[string]any{"id": "slow"}},
		{ID: "fast", Name: "Read", Arguments: map[string]any{"id": "fast"}},
	}
	finished := make(chan string, 2)
	// Intentionally not mutex-protected: Execute must serialize callbacks.
	var events []Event
	done := executorTestStart(NewToolExecutor(catalog, manager.New(nil), 2), ctx, calls, func(event Event) {
		events = append(events, event)
		if e, ok := event.(ActionFinished); ok {
			finished <- e.Call.ID
		}
	})
	executorTestReceive(t, ctx, started)
	executorTestReceive(t, ctx, started) // Both must start before either is released.
	close(gates["fast"])
	if id := executorTestReceive(t, ctx, finished); id != "fast" {
		t.Fatalf("first completed = %s", id)
	}
	close(gates["slow"])
	out := executorTestReceive(t, ctx, done)
	if out.err != nil {
		t.Fatal(out.err)
	}
	for i, result := range out.results {
		if result.ToolCallID != calls[i].ID || result.Status != conversation.ToolResultSuccess || result.Content != fmt.Sprintf("[%q]", calls[i].ID) {
			t.Fatalf("result %d = %#v", i, result)
		}
	}
	if len(events) != 4 {
		t.Fatalf("events = %d, want 4", len(events))
	}
	for _, event := range events {
		switch e := event.(type) {
		case ActionStarted:
			if e.Step != 7 {
				t.Fatalf("step = %d", e.Step)
			}
		case ActionFinished:
			if e.Step != 7 {
				t.Fatalf("step = %d", e.Step)
			}
		}
	}
}

func TestToolExecutorSerialBarriers(t *testing.T) {
	for _, barrier := range []tools.ToolDef{
		{Name: "Barrier", Authority: tools.CategoryWrite, CouldBeParalleled: true},
		{Name: "Barrier", Authority: tools.CategoryCommand, CouldBeParalleled: true},
		{Name: "Barrier", Authority: tools.CategoryRead, CouldBeParalleled: false},
	} {
		t.Run(string(barrier.Authority), func(t *testing.T) {
			ctx, _ := executorTestContext(t)
			catalog := manager.NewRegistry()
			started := make(chan string, 5)
			gates := make(map[string]chan struct{})
			for _, id := range []string{"a", "b", "w", "c", "d"} {
				gates[id] = make(chan struct{})
			}
			var active atomic.Int32
			var barrierActive atomic.Bool
			var overlap atomic.Bool
			run := func(ctx context.Context, args map[string]any) ([]any, error) {
				id := args["id"].(string)
				n := active.Add(1)
				defer active.Add(-1)
				if id == "w" && n != 1 {
					overlap.Store(true)
				}
				if id == "w" {
					barrierActive.Store(true)
					defer barrierActive.Store(false)
				} else if barrierActive.Load() {
					overlap.Store(true)
				}
				started <- id
				select {
				case <-gates[id]:
					return []any{id}, nil
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			executorTestRegister(t, catalog, executorTestTool{tools.ToolDef{Name: "Read", Authority: tools.CategoryRead, CouldBeParalleled: true}, run})
			executorTestRegister(t, catalog, executorTestTool{barrier, run})
			var calls []conversation.ToolCall
			for _, id := range []string{"a", "b", "w", "c", "d"} {
				name := "Read"
				if id == "w" {
					name = "Barrier"
				}
				calls = append(calls, conversation.ToolCall{ID: id, Name: name, Arguments: map[string]any{"id": id}})
			}
			permissions := manager.New(func(context.Context, manager.Request) (bool, error) { return true, nil })
			done := executorTestStart(NewToolExecutor(catalog, permissions, 4), ctx, calls, nil)
			first := map[string]bool{executorTestReceive(t, ctx, started): true, executorTestReceive(t, ctx, started): true}
			if !first["a"] || !first["b"] {
				t.Fatalf("first batch = %v", first)
			}
			close(gates["a"])
			close(gates["b"])
			if id := executorTestReceive(t, ctx, started); id != "w" {
				t.Fatalf("barrier overtaken by %s", id)
			}
			close(gates["w"])
			last := map[string]bool{executorTestReceive(t, ctx, started): true, executorTestReceive(t, ctx, started): true}
			if !last["c"] || !last["d"] {
				t.Fatalf("last batch = %v", last)
			}
			close(gates["c"])
			close(gates["d"])
			out := executorTestReceive(t, ctx, done)
			if out.err != nil {
				t.Fatal(out.err)
			}
			if overlap.Load() {
				t.Fatal("barrier overlapped another call")
			}
			for i, result := range out.results {
				if result.ToolCallID != calls[i].ID {
					t.Fatalf("result order = %v", out.results)
				}
			}
		})
	}
}

func TestToolExecutorConcurrencyLimit(t *testing.T) {
	for _, limit := range []int{-1, 0, 1, 2, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			ctx, _ := executorTestContext(t)
			catalog := manager.NewRegistry()
			started := make(chan struct{}, 8)
			release := make(chan struct{}, 8)
			var active, peak atomic.Int32
			executorTestRegister(t, catalog, executorTestTool{
				def: tools.ToolDef{Name: "Read", Authority: tools.CategoryRead, CouldBeParalleled: true},
				run: func(ctx context.Context, _ map[string]any) ([]any, error) {
					n := active.Add(1)
					defer active.Add(-1)
					for old := peak.Load(); n > old; old = peak.Load() {
						if peak.CompareAndSwap(old, n) {
							break
						}
					}
					started <- struct{}{}
					select {
					case <-release:
						return []any{"ok"}, nil
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				},
			})
			calls := make([]conversation.ToolCall, 8)
			for i := range calls {
				calls[i] = conversation.ToolCall{ID: fmt.Sprint(i), Name: "Read"}
			}
			done := executorTestStart(NewToolExecutor(catalog, manager.New(nil), limit), ctx, calls, nil)
			expected := limit
			if expected <= 0 {
				expected = 1
			}
			for i := 0; i < expected; i++ {
				executorTestReceive(t, ctx, started)
			}
			for i := 0; i < len(calls); i++ {
				release <- struct{}{}
			}
			out := executorTestReceive(t, ctx, done)
			if out.err != nil {
				t.Fatal(out.err)
			}
			if peak.Load() != int32(expected) {
				t.Fatalf("peak concurrency = %d, want %d", peak.Load(), expected)
			}
			if len(out.results) != len(calls) {
				t.Fatalf("results = %d", len(out.results))
			}
		})
	}
}

func TestToolExecutorCancellationWaitsForWorkers(t *testing.T) {
	ctx, cancel := executorTestContext(t)
	catalog := manager.NewRegistry()
	started := make(chan struct{}, 10)
	var stopped atomic.Int32
	executorTestRegister(t, catalog, executorTestTool{
		def: tools.ToolDef{Name: "Read", Authority: tools.CategoryRead, CouldBeParalleled: true},
		run: func(ctx context.Context, _ map[string]any) ([]any, error) {
			started <- struct{}{}
			<-ctx.Done()
			stopped.Add(1)
			return nil, ctx.Err()
		},
	})
	calls := make([]conversation.ToolCall, 10)
	for i := range calls {
		calls[i] = conversation.ToolCall{ID: fmt.Sprint(i), Name: "Read"}
	}
	done := executorTestStart(NewToolExecutor(catalog, manager.New(nil), 2), ctx, calls, nil)
	executorTestReceive(t, ctx, started)
	executorTestReceive(t, ctx, started)
	cancel()
	// Use an independent timeout because execution's context is now cancelled.
	waitCtx, _ := executorTestContext(t)
	out := executorTestReceive(t, waitCtx, done)
	if !errors.Is(out.err, context.Canceled) {
		t.Fatalf("error = %v", out.err)
	}
	if stopped.Load() != 2 {
		t.Fatalf("workers stopped before return = %d", stopped.Load())
	}
	if len(started) != 0 {
		t.Fatal("additional tools started after cancellation")
	}
}

func TestToolExecutorErrorResultsAndContinuation(t *testing.T) {
	ctx, _ := executorTestContext(t)
	catalog := manager.NewRegistry()
	for _, spec := range []executorTestTool{
		{tools.ToolDef{Name: "Denied", Authority: tools.CategoryWrite}, func(context.Context, map[string]any) ([]any, error) { t.Error("denied tool executed"); return nil, nil }},
		{tools.ToolDef{Name: "Fail", Authority: tools.CategoryRead, CouldBeParalleled: true}, func(context.Context, map[string]any) ([]any, error) { return nil, errors.New("execution failed") }},
		{tools.ToolDef{Name: "Encode", Authority: tools.CategoryRead, CouldBeParalleled: true}, func(context.Context, map[string]any) ([]any, error) { return []any{make(chan int)}, nil }},
		{tools.ToolDef{Name: "OK", Authority: tools.CategoryRead, CouldBeParalleled: true}, func(context.Context, map[string]any) ([]any, error) { return []any{"ok"}, nil }},
	} {
		executorTestRegister(t, catalog, spec)
	}
	var calls []conversation.ToolCall
	for _, name := range []string{"Missing", "Denied", "Fail", "Encode", "OK"} {
		calls = append(calls, conversation.ToolCall{ID: name, Name: name})
	}
	results, err := NewToolExecutor(catalog, manager.New(nil), 4).Execute(ctx, 1, calls, nil)
	if err != nil {
		t.Fatal(err)
	}
	var statuses []conversation.ToolResultStatus
	for i, result := range results {
		statuses = append(statuses, result.Status)
		if result.ToolCallID != calls[i].ID || result.Content == "" {
			t.Fatalf("result = %#v", result)
		}
	}
	want := []conversation.ToolResultStatus{conversation.ToolResultUnknownTool, conversation.ToolResultDenied, conversation.ToolResultExecutionError, conversation.ToolResultEncodingError, conversation.ToolResultSuccess}
	if !reflect.DeepEqual(statuses, want) {
		t.Fatalf("statuses = %v, want %v", statuses, want)
	}
	approvalErr := errors.New("approval unavailable")
	executor := NewToolExecutor(catalog, manager.New(func(context.Context, manager.Request) (bool, error) { return false, approvalErr }), 4)
	if _, err := executor.Execute(ctx, 1, calls[1:2], nil); !errors.Is(err, approvalErr) {
		t.Fatalf("approval error = %v", err)
	}
	if _, err := executor.Execute(nil, 1, nil, nil); err == nil {
		t.Fatal("nil context accepted")
	}
	if results, err := executor.Execute(ctx, 1, nil, nil); err != nil || len(results) != 0 {
		t.Fatalf("empty calls = %v, %v", results, err)
	}
}
