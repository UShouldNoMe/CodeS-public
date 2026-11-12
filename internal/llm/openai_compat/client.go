package openai_compat

import (
	"CodeS/internal/conversation"
	"CodeS/internal/llm"
	"CodeS/internal/tools"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

type Config struct {
	Provider string
	APIKey   string
	BaseURL  string
	Model    string
}

type ClientEntity struct {
	client   *openai.Client
	provider string
	model    string
}

func New(config Config) (*ClientEntity, error) {
	if strings.TrimSpace(config.Model) == "" {
		return nil, errors.New("OpenAI-compatible model is empty")
	}
	return newClient(config)
}

// NewModelCatalog does not require a selected model or system prompt.
func NewModelCatalog(config Config) (llm.ModelCatalog, error) {
	return newClient(config)
}

func newClient(config Config) (*ClientEntity, error) {
	if strings.TrimSpace(config.Provider) == "" {
		return nil, errors.New("OpenAI-compatible provider name is empty")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("OpenAI-compatible API key is empty")
	}
	if strings.TrimSpace(config.BaseURL) == "" {
		return nil, errors.New("OpenAI-compatible base URL is empty")
	}
	baseURL, err := url.Parse(config.BaseURL)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("invalid OpenAI-compatible base URL %q", config.BaseURL)
	}

	sdkClient := openai.NewClient(
		option.WithAPIKey(config.APIKey),
		option.WithBaseURL(config.BaseURL),
	)
	return &ClientEntity{
		client:   &sdkClient,
		provider: config.Provider,
		model:    config.Model,
	}, nil
}

func (c *ClientEntity) Generate(ctx context.Context, request *llm.Request) (*conversation.Response, error) {
	if err := validateRequest(ctx, c, request); err != nil {
		return nil, err
	}

	messages, err := buildMessages(request.Prompt, request.Conversation)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    c.model,
		Messages: messages,
		Tools:    buildTools(request.Tools),
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || len(resp.Choices) == 0 {
		return nil, errors.New("OpenAI-compatible provider returned no completion choices")
	}

	choice := resp.Choices[0]
	result := &conversation.Response{
		Content:      choice.Message.Content,
		FinishReason: mapFinishReason(choice.FinishReason),
		Usage: conversation.Usage{
			InputTokens:  int(resp.Usage.PromptTokens),
			OutputTokens: int(resp.Usage.CompletionTokens),
		},
		ToolCalls: make([]conversation.ToolCall, 0, len(choice.Message.ToolCalls)),
	}
	for index, source := range choice.Message.ToolCalls {
		if source.Type != "function" {
			return nil, fmt.Errorf("unsupported tool call type %q at index %d", source.Type, index)
		}
		if source.ID == "" {
			return nil, fmt.Errorf("tool call at index %d has no ID", index)
		}
		if source.Function.Name == "" {
			return nil, fmt.Errorf("tool call at index %d has no name", index)
		}
		arguments, err := decodeArguments(source.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("decode arguments for tool %q at index %d: %w", source.Function.Name, index, err)
		}
		result.ToolCalls = append(result.ToolCalls, conversation.ToolCall{
			ID:        source.ID,
			Name:      source.Function.Name,
			Arguments: arguments,
		})
	}
	return result, nil
}

func (c *ClientEntity) Stream(
	ctx context.Context,
	request *llm.Request,
	onEvent func(conversation.StreamEvent) error,
) (*conversation.Response, error) {
	if onEvent == nil {
		return nil, errors.New("stream event callback is required")
	}
	if err := validateRequest(ctx, c, request); err != nil {
		return nil, err
	}

	messages, err := buildMessages(request.Prompt, request.Conversation)
	if err != nil {
		return nil, err
	}
	stream := c.client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model:    c.model,
		Messages: messages,
		Tools:    buildTools(request.Tools),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: param.NewOpt(true),
		},
	})
	defer stream.Close()

	collector := newStreamCollector()
	for stream.Next() {
		chunk := stream.Current()
		if err := collector.Consume(&chunk, onEvent); err != nil {
			return nil, err
		}
	}
	if err := stream.Err(); err != nil {
		return nil, err
	}
	return collector.Finish(onEvent)
}

func (c *ClientEntity) ListModels(ctx context.Context) ([]llm.ModelInfo, error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}
	if c == nil || c.client == nil {
		return nil, errors.New("OpenAI-compatible client is nil")
	}

	page, err := c.client.Models.List(ctx)
	if err != nil {
		return nil, err
	}
	if page == nil {
		return nil, errors.New("OpenAI-compatible provider returned a nil model list")
	}

	models := make([]llm.ModelInfo, 0, len(page.Data))
	seen := make(map[string]struct{}, len(page.Data))
	for index, model := range page.Data {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			return nil, fmt.Errorf("model at index %d has no ID", index)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, llm.ModelInfo{
			Provider:    c.provider,
			ID:          id,
			DisplayName: id,
		})
	}
	sort.Slice(models, func(i, j int) bool {
		return models[i].ID < models[j].ID
	})
	return models, nil
}

func validateRequest(ctx context.Context, client *ClientEntity, request *llm.Request) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	if client == nil || client.client == nil {
		return errors.New("OpenAI-compatible client is nil")
	}
	if request == nil {
		return errors.New("request is nil")
	}
	if request.Conversation == nil {
		return errors.New("conversation is nil")
	}
	return nil
}

func buildMessages(
	systemPrompt string,
	conv *conversation.Conversation,
) ([]openai.ChatCompletionMessageParamUnion, error) {
	if conv == nil {
		return nil, errors.New("conversation is nil")
	}

	capacity := len(conv.Messages)
	if systemPrompt != "" {
		capacity++
	}
	messages := make([]openai.ChatCompletionMessageParamUnion, 0, capacity)
	if systemPrompt != "" {
		messages = append(messages, openai.SystemMessage(systemPrompt))
	}

	for index, source := range conv.Messages {
		switch source.Role {
		case conversation.RoleSystem:
			messages = append(messages, openai.SystemMessage(source.Content))

		case conversation.RoleUser:
			messages = append(messages, openai.UserMessage(source.Content))

		case conversation.RoleAssistant:
			assistant := openai.ChatCompletionAssistantMessageParam{}
			if source.Content != "" || len(source.ToolCalls) == 0 {
				assistant.Content.OfString = param.NewOpt(source.Content)
			}
			for callIndex, call := range source.ToolCalls {
				if call.ID == "" {
					return nil, fmt.Errorf("assistant message %d tool call %d has no ID", index, callIndex)
				}
				if call.Name == "" {
					return nil, fmt.Errorf("assistant message %d tool call %d has no name", index, callIndex)
				}
				arguments := call.Arguments
				if arguments == nil {
					arguments = map[string]any{}
				}
				rawArguments, err := json.Marshal(arguments)
				if err != nil {
					return nil, fmt.Errorf("encode arguments for tool %q: %w", call.Name, err)
				}
				assistant.ToolCalls = append(assistant.ToolCalls, openai.ChatCompletionMessageToolCallUnionParam{
					OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
						ID: call.ID,
						Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
							Name:      call.Name,
							Arguments: string(rawArguments),
						},
					},
				})
			}
			messages = append(messages, openai.ChatCompletionMessageParamUnion{OfAssistant: &assistant})

		case conversation.RoleTool:
			if source.ToolResult == nil {
				return nil, fmt.Errorf("tool message %d has no result", index)
			}
			if source.ToolResult.ToolCallID == "" {
				return nil, fmt.Errorf("tool message %d has no tool call ID", index)
			}
			messages = append(messages, openai.ToolMessage(
				source.ToolResult.Content,
				source.ToolResult.ToolCallID,
			))

		default:
			return nil, fmt.Errorf("message %d has unsupported role %q", index, source.Role)
		}
	}
	return messages, nil
}

func buildTools(definitions []tools.ToolDef) []openai.ChatCompletionToolUnionParam {
	if len(definitions) == 0 {
		return nil
	}
	result := make([]openai.ChatCompletionToolUnionParam, 0, len(definitions))
	for _, definition := range definitions {
		parameters := shared.FunctionParameters{
			"type":       definition.Type,
			"properties": definition.Properties,
		}
		if definition.Required != nil {
			parameters["required"] = definition.Required
		}
		function := shared.FunctionDefinitionParam{
			Name:       definition.Name,
			Parameters: parameters,
		}
		if definition.Description != "" {
			function.Description = param.NewOpt(definition.Description)
		}
		result = append(result, openai.ChatCompletionFunctionTool(function))
	}
	return result
}

func decodeArguments(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	arguments := make(map[string]any)
	if err := json.Unmarshal([]byte(raw), &arguments); err != nil {
		return nil, err
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	return arguments, nil
}

func mapFinishReason(reason string) conversation.FinishReason {
	switch reason {
	case string(conversation.FinishReasonStop):
		return conversation.FinishReasonStop
	case string(conversation.FinishReasonToolCalls):
		return conversation.FinishReasonToolCalls
	case string(conversation.FinishReasonLength):
		return conversation.FinishReasonLength
	case string(conversation.FinishReasonContentFilter):
		return conversation.FinishReasonContentFilter
	case string(conversation.FinishReasonInsufficientSystemResource):
		return conversation.FinishReasonInsufficientSystemResource
	default:
		return conversation.FinishReason(reason)
	}
}

var _ llm.Client = (*ClientEntity)(nil)
var _ llm.ModelCatalog = (*ClientEntity)(nil)
