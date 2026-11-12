package conversation

import "reflect"

type Role string
type ToolResultStatus string
type FinishReason string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
	RoleSystem    Role = "system"
)

const (
	ToolResultSuccess        ToolResultStatus = "success"
	ToolResultDenied         ToolResultStatus = "denied"
	ToolResultUnknownTool    ToolResultStatus = "unknown_tool"
	ToolResultExecutionError ToolResultStatus = "execution_error"
	ToolResultEncodingError  ToolResultStatus = "encoding_error"
)

const ( // 这些字段是从deepseek api网站得到的
	FinishReasonStop                       FinishReason = "stop"
	FinishReasonToolCalls                  FinishReason = "tool_calls"
	FinishReasonLength                     FinishReason = "length"
	FinishReasonContentFilter              FinishReason = "content_filter"
	FinishReasonInsufficientSystemResource FinishReason = "insufficient_system_resource"
)

type ToolCall struct { // LLM选择了什么Tool
	ID        string
	Name      string
	Arguments map[string]any
}

type ToolResult struct { // Agent运行完函数之后的结果返回
	ToolCallID string
	Content    string
	ToolName   string
	Status     ToolResultStatus
	Command    *CommandResult `json:",omitempty"`
}

type CommandResult struct {
	Status   string
	ExitCode int
	TimedOut bool
}

type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolResult *ToolResult
}

type Conversation struct {
	Messages []Message
}

type Usage struct {
	InputTokens  int
	OutputTokens int
}

type Response struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason FinishReason
	Usage        Usage
}

type StreamEventType string

const (
	StreamEventTextDelta        StreamEventType = "text_delta"
	StreamEventToolCallStart    StreamEventType = "tool_call_start"
	StreamEventToolCallDelta    StreamEventType = "tool_call_delta"
	StreamEventToolCallComplete StreamEventType = "tool_call_complete"
	StreamEventEnd              StreamEventType = "stream_end"
)

type StreamEvent struct {
	Type           StreamEventType
	Delta          string
	ToolCallIndex  int
	ToolCallID     string
	ToolName       string
	ArgumentsDelta string
	ToolCall       *ToolCall
	FinishReason   FinishReason
	Usage          Usage
}

func New() *Conversation {
	return &Conversation{
		Messages: []Message{},
	}
}

func (c *Conversation) AddUser(content string) {
	c.Messages = append(c.Messages, Message{
		Role:    RoleUser,
		Content: content,
	})
}

func (c *Conversation) AddSystem(content string) {
	c.Messages = append(c.Messages, Message{Role: RoleSystem, Content: content})
}

func (c *Conversation) AddAssistant(content string, toolCalls []ToolCall) {
	c.Messages = append(c.Messages, Message{
		Role:      RoleAssistant,
		Content:   content,
		ToolCalls: toolCalls,
	})
}

func (c *Conversation) AddToolResult(result ToolResult) {
	resultCopy := result
	c.Messages = append(c.Messages, Message{
		Role:       RoleTool,
		ToolResult: &resultCopy,
	})
}

func (c *Conversation) GetMessage() []Message {
	if c == nil || c.Messages == nil {
		return nil
	}

	messages := make([]Message, len(c.Messages))
	for i, source := range c.Messages {
		messages[i] = Message{
			Role:    source.Role,
			Content: source.Content,
		}
		if source.ToolCalls != nil {
			messages[i].ToolCalls = make([]ToolCall, len(source.ToolCalls))
			for j, call := range source.ToolCalls {
				messages[i].ToolCalls[j] = ToolCall{
					ID:        call.ID,
					Name:      call.Name,
					Arguments: cloneArguments(call.Arguments),
				}
			}
		}
		if source.ToolResult != nil {
			result := *source.ToolResult
			messages[i].ToolResult = &result
		}
	}
	return messages
}

// cloneArguments copies the JSON-shaped values carried in tool arguments.
// Tool arguments are decoded from JSON, but reflection also preserves any
// concrete map or slice types used by callers in tests or custom clients.
func cloneArguments(arguments map[string]any) map[string]any {
	if arguments == nil {
		return nil
	}
	return cloneValue(reflect.ValueOf(arguments)).Interface().(map[string]any)
}

func cloneValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.New(value.Type()).Elem()
		copy.Set(cloneValue(value.Elem()))
		return copy

	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			copy.SetMapIndex(cloneValue(iter.Key()), cloneValue(iter.Value()))
		}
		return copy

	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			copy.Index(i).Set(cloneValue(value.Index(i)))
		}
		return copy

	case reflect.Array:
		copy := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			copy.Index(i).Set(cloneValue(value.Index(i)))
		}
		return copy

	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.New(value.Type().Elem())
		copy.Elem().Set(cloneValue(value.Elem()))
		return copy
	}

	return value
}
