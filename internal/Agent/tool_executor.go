package Agent

import (
	"CodeS/internal/conversation"
	"CodeS/internal/tools"
	"CodeS/internal/tools/manager"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

const defaultToolParallelism = 4

type ToolExecutor struct {
	catalog     manager.ToolCatalog
	permissions *manager.PermissionManager
	maxParallel int
}

func NewToolExecutor(catalog manager.ToolCatalog, permissions *manager.PermissionManager, maxParallel int) *ToolExecutor {
	if maxParallel <= 0 {
		maxParallel = 1
	}
	return &ToolExecutor{catalog: catalog, permissions: permissions, maxParallel: maxParallel}
}

type scheduledToolCall struct {
	call conversation.ToolCall
	tool tools.Tool
	def  tools.ToolDef
}

func (c scheduledToolCall) parallelSafe() bool {
	// Interactive approvals and mutating calls remain serial barriers.
	return c.tool != nil && c.def.CouldBeParalleled && c.def.Authority == tools.CategoryRead
}

// Execute runs adjacent safe calls concurrently, bounded by maxParallel.
// Other calls are barriers: all earlier tasks finish before they start,
// and later tasks wait for them. Results retain the original model order.
func (e *ToolExecutor) Execute(ctx context.Context, step int, calls []conversation.ToolCall, emit Emitter) ([]conversation.ToolResult, error) {
	if e == nil || e.catalog == nil {
		return nil, errors.New("tool catalog is nil")
	}
	if e.permissions == nil {
		return nil, errors.New("permission manager is nil")
	}
	if ctx == nil {
		return nil, errors.New("context is nil")
	}
	if emit == nil {
		emit = func(Event) {}
	}
	// Callbacks need not be concurrency-safe. Delivery is serialized while
	// tool execution remains concurrent and completion events arrive live.
	var emitMu sync.Mutex
	emitEvent := func(event Event) {
		emitMu.Lock()
		defer emitMu.Unlock()
		emit(event)
	}

	results := make([]conversation.ToolResult, len(calls))
	scheduled := make([]scheduledToolCall, len(calls))
	for i, call := range calls {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		tool, err := e.catalog.Get(call.Name)
		if err != nil {
			results[i] = conversation.ToolResult{
				ToolCallID: call.ID, ToolName: call.Name,
				Status:  conversation.ToolResultUnknownTool,
				Content: encodeToolError(call.Name, err),
			}
			scheduled[i] = scheduledToolCall{call: call}
			continue
		}
		scheduled[i] = scheduledToolCall{call: call, tool: tool, def: tool.Definition()}
	}

	limit := e.maxParallel
	if limit <= 0 {
		limit = 1
	}
	for start := 0; start < len(scheduled); {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entry := scheduled[start]
		if entry.tool == nil {
			start++ // Unknown calls retain their result and split batches.
			continue
		}
		if !entry.parallelSafe() || limit == 1 {
			record, err := e.executeCall(ctx, step, entry, emitEvent)
			if err != nil {
				return nil, err
			}
			results[start] = record
			start++
			continue
		}

		end := start + 1
		for end < len(scheduled) && scheduled[end].parallelSafe() {
			end++
		}
		batchLimit := limit
		if batchLimit > end-start {
			batchLimit = end - start
		}
		slots := make(chan struct{}, batchLimit)
		batchErrors := make([]error, end-start)
		var wg sync.WaitGroup
	launch:
		for i := start; i < end; i++ {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				break launch
			}
			if ctx.Err() != nil {
				<-slots
				break
			}
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				defer func() { <-slots }()
				results[index], batchErrors[index-start] = e.executeCall(ctx, step, scheduled[index], emitEvent)
			}(i)
		}
		// No worker may outlive Execute and emit into a closed Agent channel.
		wg.Wait()
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, err := range batchErrors {
			if err != nil {
				return nil, err
			}
		}
		start = end
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

func (e *ToolExecutor) executeCall(ctx context.Context, step int, entry scheduledToolCall, emit Emitter) (conversation.ToolResult, error) {
	call := entry.call
	if err := ctx.Err(); err != nil {
		return conversation.ToolResult{}, err
	}
	allowed, err := e.permissions.Authorize(ctx, entry.def, call.Arguments)
	if err != nil {
		return conversation.ToolResult{}, fmt.Errorf("authorize tool %q: %w", call.Name, err)
	}
	record := conversation.ToolResult{
		ToolCallID: call.ID, ToolName: call.Name,
		Status: conversation.ToolResultSuccess,
	}
	if !allowed {
		record.Status = conversation.ToolResultDenied
		record.Content = encodeToolDenied(call.Name)
		return record, nil
	}
	if err := ctx.Err(); err != nil {
		return conversation.ToolResult{}, err
	}

	emit(ActionStarted{Step: step, Call: call})
	output, executeErr := entry.tool.Execute(ctx, call.Arguments)
	if executeErr != nil {
		record.Status = conversation.ToolResultExecutionError
		record.Content = encodeToolError(call.Name, executeErr)
	} else {
		encoded, encodeErr := json.Marshal(output)
		if encodeErr != nil {
			record.Status = conversation.ToolResultEncodingError
			record.Content = encodeToolError(call.Name, encodeErr)
		} else {
			record.Content = string(encoded)
			if len(output) > 0 {
				if result, ok := output[0].(tools.BashResult); ok {
					record.Command = &conversation.CommandResult{
						Status: result.CommandStatus, ExitCode: result.ExitCode, TimedOut: result.TimedOut,
					}
				}
			}
		}
	}
	emit(ActionFinished{Step: step, Call: call, Record: record})
	return record, nil
}
