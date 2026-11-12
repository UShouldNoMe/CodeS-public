package openai_compat

import (
	"CodeS/internal/conversation"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/openai/openai-go/v3"
)

type streamedToolCall struct {
	id               string
	name             string
	arguments        strings.Builder
	emittedArguments int
	started          bool
}

type streamCollector struct {
	content      strings.Builder
	toolCalls    map[int]*streamedToolCall
	finishReason conversation.FinishReason
	usage        conversation.Usage
}

func newStreamCollector() *streamCollector {
	return &streamCollector{toolCalls: make(map[int]*streamedToolCall)}
}

func (c *streamCollector) Consume(
	chunk *openai.ChatCompletionChunk,
	onEvent func(conversation.StreamEvent) error,
) error {
	if chunk == nil {
		return nil
	}
	if chunk.JSON.Usage.Valid() || chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 {
		c.usage = conversation.Usage{
			InputTokens:  int(chunk.Usage.PromptTokens),
			OutputTokens: int(chunk.Usage.CompletionTokens),
		}
	}
	if len(chunk.Choices) == 0 {
		return nil
	}

	choice := chunk.Choices[0]
	if choice.FinishReason != "" {
		finishReason := mapFinishReason(choice.FinishReason)
		if c.finishReason != "" && c.finishReason != finishReason {
			return fmt.Errorf(
				"OpenAI-compatible provider changed finish reason from %q to %q",
				c.finishReason,
				finishReason,
			)
		}
		c.finishReason = finishReason
	}

	delta := choice.Delta
	if delta.Content != "" {
		c.content.WriteString(delta.Content)
		if err := onEvent(conversation.StreamEvent{
			Type:  conversation.StreamEventTextDelta,
			Delta: delta.Content,
		}); err != nil {
			return err
		}
	}

	for _, source := range delta.ToolCalls {
		index := int(source.Index)
		call, ok := c.toolCalls[index]
		if !ok {
			call = &streamedToolCall{}
			c.toolCalls[index] = call
		}
		if source.ID != "" {
			if call.id != "" && call.id != source.ID {
				return fmt.Errorf("tool call at index %d changed ID from %q to %q", index, call.id, source.ID)
			}
			call.id = source.ID
		}
		if source.Function.Name != "" {
			if call.name != "" && call.name != source.Function.Name {
				return fmt.Errorf("tool call at index %d changed name from %q to %q", index, call.name, source.Function.Name)
			}
			call.name = source.Function.Name
		}
		if source.Function.Arguments != "" {
			call.arguments.WriteString(source.Function.Arguments)
		}

		if !call.started && call.id != "" && call.name != "" {
			call.started = true
			if err := onEvent(conversation.StreamEvent{
				Type:          conversation.StreamEventToolCallStart,
				ToolCallIndex: index,
				ToolCallID:    call.id,
				ToolName:      call.name,
			}); err != nil {
				return err
			}
		}
		if call.started && call.emittedArguments < call.arguments.Len() {
			rawArguments := call.arguments.String()
			argumentsDelta := rawArguments[call.emittedArguments:]
			call.emittedArguments = len(rawArguments)
			if err := onEvent(conversation.StreamEvent{
				Type:           conversation.StreamEventToolCallDelta,
				ToolCallIndex:  index,
				ToolCallID:     call.id,
				ToolName:       call.name,
				ArgumentsDelta: argumentsDelta,
			}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *streamCollector) Finish(
	onEvent func(conversation.StreamEvent) error,
) (*conversation.Response, error) {
	if c.finishReason == "" {
		return nil, errors.New("OpenAI-compatible stream ended without a finish reason")
	}
	response := &conversation.Response{
		Content:      c.content.String(),
		ToolCalls:    make([]conversation.ToolCall, 0, len(c.toolCalls)),
		FinishReason: c.finishReason,
		Usage:        c.usage,
	}

	indexes := make([]int, 0, len(c.toolCalls))
	for index := range c.toolCalls {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	for _, index := range indexes {
		pending := c.toolCalls[index]
		if pending.id == "" {
			return nil, fmt.Errorf("streamed tool call at index %d has no ID", index)
		}
		if pending.name == "" {
			return nil, fmt.Errorf("streamed tool call at index %d has no name", index)
		}
		arguments := make(map[string]any)
		rawArguments := pending.arguments.String()
		if strings.TrimSpace(rawArguments) != "" {
			if err := json.Unmarshal([]byte(rawArguments), &arguments); err != nil {
				return nil, fmt.Errorf("decode arguments for streamed tool %q at index %d: %w", pending.name, index, err)
			}
		}
		if arguments == nil {
			arguments = map[string]any{}
		}
		toolCall := conversation.ToolCall{
			ID:        pending.id,
			Name:      pending.name,
			Arguments: arguments,
		}
		response.ToolCalls = append(response.ToolCalls, toolCall)
		toolCallCopy := toolCall
		if err := onEvent(conversation.StreamEvent{
			Type:          conversation.StreamEventToolCallComplete,
			ToolCallIndex: index,
			ToolCallID:    toolCall.ID,
			ToolName:      toolCall.Name,
			ToolCall:      &toolCallCopy,
		}); err != nil {
			return nil, err
		}
	}
	if err := onEvent(conversation.StreamEvent{
		Type:         conversation.StreamEventEnd,
		FinishReason: response.FinishReason,
		Usage:        response.Usage,
	}); err != nil {
		return nil, err
	}
	return response, nil
}
