package openai_compat

import (
	"CodeS/internal/conversation"
	"CodeS/internal/llm"
	"CodeS/internal/tools"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestNewValidatesConfig(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{name: "missing provider", config: Config{APIKey: "key", BaseURL: "https://example.com/v1", Model: "model"}},
		{name: "missing API key", config: Config{Provider: "provider", BaseURL: "https://example.com/v1", Model: "model"}},
		{name: "missing base URL", config: Config{Provider: "provider", APIKey: "key", Model: "model"}},
		{name: "invalid base URL", config: Config{Provider: "provider", APIKey: "key", BaseURL: "not-a-url", Model: "model"}},
		{name: "missing model", config: Config{Provider: "provider", APIKey: "key", BaseURL: "https://example.com/v1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config); err == nil {
				t.Fatal("New() error = nil")
			}
		})
	}
}

func TestBuildMessagesPreservesToolConversation(t *testing.T) {
	conv := &conversation.Conversation{Messages: []conversation.Message{
		{Role: conversation.RoleUser, Content: "inspect"},
		{
			Role: conversation.RoleAssistant,
			ToolCalls: []conversation.ToolCall{{
				ID:   "call-1",
				Name: "ReadFile",
			}},
		},
		{
			Role: conversation.RoleTool,
			ToolResult: &conversation.ToolResult{
				ToolCallID: "call-1",
				Content:    "package main",
			},
		},
	}}

	messages, err := buildMessages("system prompt", conv)
	if err != nil {
		t.Fatalf("buildMessages() error = %v", err)
	}
	if len(messages) != 4 {
		t.Fatalf("message count = %d, want 4", len(messages))
	}
	if messages[0].OfSystem == nil || messages[0].OfSystem.Content.OfString.Value != "system prompt" {
		t.Fatalf("system message = %#v", messages[0])
	}
	if messages[1].OfUser == nil || messages[1].OfUser.Content.OfString.Value != "inspect" {
		t.Fatalf("user message = %#v", messages[1])
	}
	assistant := messages[2].OfAssistant
	if assistant == nil || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].OfFunction == nil {
		t.Fatalf("assistant message = %#v", messages[2])
	}
	call := assistant.ToolCalls[0].OfFunction
	if call.ID != "call-1" || call.Function.Name != "ReadFile" || call.Function.Arguments != "{}" {
		t.Fatalf("tool call = %#v", call)
	}
	if messages[3].OfTool == nil || messages[3].OfTool.ToolCallID != "call-1" || messages[3].OfTool.Content.OfString.Value != "package main" {
		t.Fatalf("tool message = %#v", messages[3])
	}
}

func TestBuildMessagesReportsUnencodableArguments(t *testing.T) {
	conv := &conversation.Conversation{Messages: []conversation.Message{{
		Role: conversation.RoleAssistant,
		ToolCalls: []conversation.ToolCall{{
			ID:        "call-1",
			Name:      "Broken",
			Arguments: map[string]any{"value": make(chan int)},
		}},
	}}}
	if _, err := buildMessages("", conv); err == nil {
		t.Fatal("buildMessages() error = nil")
	}
}

func TestBuildTools(t *testing.T) {
	definitions := []tools.ToolDef{{
		Name:        "Time",
		Description: "Get the current time",
		Type:        "object",
		Properties: map[string]any{
			"zone": map[string]any{"type": "string"},
		},
		Required: []string{"zone"},
	}}
	got := buildTools(definitions)
	if len(got) != 1 || got[0].OfFunction == nil {
		t.Fatalf("buildTools() = %#v", got)
	}
	function := got[0].OfFunction.Function
	if function.Name != "Time" || function.Description.Value != "Get the current time" {
		t.Fatalf("function = %#v", function)
	}
	if !reflect.DeepEqual(function.Parameters["required"], []string{"zone"}) {
		t.Fatalf("required = %#v", function.Parameters["required"])
	}
}

func TestGenerateHandlesEmptyToolArguments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if payload["model"] != "test-model" {
			t.Errorf("model = %#v", payload["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"id":"chatcmpl-test",
			"object":"chat.completion",
			"created":1,
			"model":"test-model",
			"choices":[{
				"index":0,
				"message":{"role":"assistant","content":"","tool_calls":[{
					"id":"call-1","type":"function","function":{"name":"Time","arguments":""}
				}]},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}
		}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Provider: "test-provider",
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "test-model",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response, err := client.Generate(context.Background(), &llm.Request{
		Conversation: &conversation.Conversation{Messages: []conversation.Message{{
			Role:    conversation.RoleUser,
			Content: "what time is it?",
		}}},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	wantCalls := []conversation.ToolCall{{ID: "call-1", Name: "Time", Arguments: map[string]any{}}}
	if !reflect.DeepEqual(response.ToolCalls, wantCalls) {
		t.Fatalf("ToolCalls = %#v, want %#v", response.ToolCalls, wantCalls)
	}
	if response.FinishReason != conversation.FinishReasonToolCalls {
		t.Fatalf("FinishReason = %q", response.FinishReason)
	}
	if response.Usage != (conversation.Usage{InputTokens: 7, OutputTokens: 3}) {
		t.Fatalf("Usage = %#v", response.Usage)
	}
}

func TestStreamReadsTextFinishReasonAndUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if payload["stream"] != true {
			t.Errorf("stream = %#v", payload["stream"])
		}
		streamOptions, _ := payload["stream_options"].(map[string]any)
		if streamOptions["include_usage"] != true {
			t.Errorf("stream_options = %#v", streamOptions)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"id\":\"chatcmpl-test\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"test-model\",\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	client, err := New(Config{
		Provider: "test-provider",
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "test-model",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	var events []conversation.StreamEvent
	response, err := client.Stream(context.Background(), &llm.Request{
		Conversation: &conversation.Conversation{Messages: []conversation.Message{{
			Role:    conversation.RoleUser,
			Content: "hello",
		}}},
	}, func(event conversation.StreamEvent) error {
		events = append(events, event)
		return nil
	})
	if err != nil {
		t.Fatalf("Stream() error = %v", err)
	}
	if response.Content != "hello" || response.FinishReason != conversation.FinishReasonStop {
		t.Fatalf("response = %#v", response)
	}
	if response.Usage != (conversation.Usage{InputTokens: 5, OutputTokens: 2}) {
		t.Fatalf("Usage = %#v", response.Usage)
	}
	if len(events) != 2 || events[0].Type != conversation.StreamEventTextDelta || events[1].Type != conversation.StreamEventEnd {
		t.Fatalf("events = %#v", events)
	}
}

func TestListModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/models" {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "missing authorization", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"object":"list",
			"data":[
				{"id":"model-b","object":"model","created":2,"owned_by":"upstream"},
				{"id":"model-a","object":"model","created":1,"owned_by":"another-owner"},
				{"id":"model-b","object":"model","created":2,"owned_by":"upstream"}
			]
		}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Provider: "configured-provider",
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "model-a",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	models, err := client.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels() error = %v", err)
	}
	want := []llm.ModelInfo{
		{Provider: "configured-provider", ID: "model-a", DisplayName: "model-a"},
		{Provider: "configured-provider", ID: "model-b", DisplayName: "model-b"},
	}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("ListModels() = %#v, want %#v", models, want)
	}
}

func TestListModelsRejectsModelWithoutID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"","object":"model","owned_by":"provider"}]}`)
	}))
	defer server.Close()

	client, err := New(Config{
		Provider: "provider",
		APIKey:   "test-key",
		BaseURL:  server.URL + "/v1",
		Model:    "model",
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := client.ListModels(context.Background()); err == nil {
		t.Fatal("ListModels() error = nil")
	}
}
