package openai_compat

import (
	"CodeS/internal/conversation"
	"errors"
	"reflect"
	"testing"

	"github.com/openai/openai-go/v3"
)

func TestStreamCollectorBuildsTextAndMultipleToolCalls(t *testing.T) {
	collector := newStreamCollector()
	var events []conversation.StreamEvent
	onEvent := func(event conversation.StreamEvent) error {
		events = append(events, event)
		return nil
	}

	chunks := []*openai.ChatCompletionChunk{
		streamChunk(openai.ChatCompletionChunkChoiceDelta{Content: "先"}),
		streamChunk(openai.ChatCompletionChunkChoiceDelta{
			Content: "查询",
			ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
				{Index: 1, Function: openai.ChatCompletionChunkChoiceDeltaToolCallFunction{Arguments: `{"zone":"Asia/`}},
				{Index: 0, Function: openai.ChatCompletionChunkChoiceDeltaToolCallFunction{Arguments: `{`}},
			},
		}),
		streamChunk(openai.ChatCompletionChunkChoiceDelta{
			ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{
				{Index: 0, ID: "call-os", Function: openai.ChatCompletionChunkChoiceDeltaToolCallFunction{Name: "OS", Arguments: `}`}},
				{Index: 1, ID: "call-time", Function: openai.ChatCompletionChunkChoiceDeltaToolCallFunction{Name: "Time", Arguments: `Shanghai"}`}},
			},
		}),
		streamFinishChunk("tool_calls"),
		{Usage: openai.CompletionUsage{PromptTokens: 30, CompletionTokens: 12}},
	}
	for _, chunk := range chunks {
		if err := collector.Consume(chunk, onEvent); err != nil {
			t.Fatalf("Consume() error = %v", err)
		}
	}
	response, err := collector.Finish(onEvent)
	if err != nil {
		t.Fatalf("Finish() error = %v", err)
	}

	wantCalls := []conversation.ToolCall{
		{ID: "call-os", Name: "OS", Arguments: map[string]any{}},
		{ID: "call-time", Name: "Time", Arguments: map[string]any{"zone": "Asia/Shanghai"}},
	}
	if response.Content != "先查询" || !reflect.DeepEqual(response.ToolCalls, wantCalls) {
		t.Fatalf("response = %#v", response)
	}
	if response.FinishReason != conversation.FinishReasonToolCalls {
		t.Fatalf("FinishReason = %q", response.FinishReason)
	}
	if response.Usage != (conversation.Usage{InputTokens: 30, OutputTokens: 12}) {
		t.Fatalf("Usage = %#v", response.Usage)
	}
	if events[len(events)-1].Type != conversation.StreamEventEnd {
		t.Fatalf("last event = %q", events[len(events)-1].Type)
	}
	for _, event := range events {
		if event.Type == conversation.StreamEventToolCallStart && (event.ToolCallID == "" || event.ToolName == "") {
			t.Fatalf("incomplete start event = %#v", event)
		}
	}
}

func TestStreamCollectorRejectsChangedToolIdentity(t *testing.T) {
	collector := newStreamCollector()
	onEvent := func(conversation.StreamEvent) error { return nil }
	if err := collector.Consume(streamChunk(openai.ChatCompletionChunkChoiceDelta{
		ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{{
			Index: 0,
			ID:    "call-1",
			Function: openai.ChatCompletionChunkChoiceDeltaToolCallFunction{
				Name: "Time",
			},
		}},
	}), onEvent); err != nil {
		t.Fatalf("first Consume() error = %v", err)
	}
	err := collector.Consume(streamChunk(openai.ChatCompletionChunkChoiceDelta{
		ToolCalls: []openai.ChatCompletionChunkChoiceDeltaToolCall{{Index: 0, ID: "call-2"}},
	}), onEvent)
	if err == nil {
		t.Fatal("second Consume() error = nil")
	}
}

func TestStreamCollectorPropagatesCallbackError(t *testing.T) {
	collector := newStreamCollector()
	wantErr := errors.New("stop stream")
	err := collector.Consume(
		streamChunk(openai.ChatCompletionChunkChoiceDelta{Content: "hello"}),
		func(conversation.StreamEvent) error { return wantErr },
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Consume() error = %v, want %v", err, wantErr)
	}
}

func TestStreamCollectorRejectsMissingFinishReason(t *testing.T) {
	collector := newStreamCollector()
	if _, err := collector.Finish(func(conversation.StreamEvent) error { return nil }); err == nil {
		t.Fatal("Finish() error = nil")
	}
}

func streamChunk(delta openai.ChatCompletionChunkChoiceDelta) *openai.ChatCompletionChunk {
	return &openai.ChatCompletionChunk{Choices: []openai.ChatCompletionChunkChoice{{
		Index: 0,
		Delta: delta,
	}}}
}

func streamFinishChunk(reason string) *openai.ChatCompletionChunk {
	return &openai.ChatCompletionChunk{Choices: []openai.ChatCompletionChunkChoice{{
		Index:        0,
		FinishReason: reason,
	}}}
}
