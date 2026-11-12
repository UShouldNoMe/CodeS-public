package Agent

import (
	"CodeS/internal/conversation"
)

type Event interface {
	agentEvent()
}

type Emitter func(Event)

type ActionProposed struct { // LLM正常结束
	Step  int
	Calls []conversation.ToolCall
}

type TextDelta struct { // 收到LLM新来的文字就产生这样的event
	Step  int
	Delta string
}
type ActionStarted struct { // ToolExecutor中的某个工具即将要执行的时候
	Step int
	Call conversation.ToolCall
}

type ActionFinished struct { // ToolExecutor已经结束执行，但结果还没有写入Session
	Step   int
	Call   conversation.ToolCall
	Record conversation.ToolResult
}

type ObservationRecorded struct { // 已经成功写入session的结果
	Step   int
	Record conversation.ToolResult
}
type RunStarted struct{} // Run goroutine启动，并且基础参数校验完成的时候

type StepStarted struct { // 每次准备调用LLM之前
	Step int
}

type StepCompleted struct {
	Step int
}

type RunResult struct {
	Steps        int
	FinalContent string
	Usage        conversation.Usage
}

type RunCompleted struct {
	Result RunResult
}

type RunStopped struct {
	Step   int
	Reason string // 模型停止、取消或运行限制等受控停止原因
}

type RunFailed struct {
	Step  int
	Phase string
	Err   error
}

func (t TextDelta) agentEvent() {
}

func (a ActionProposed) agentEvent() {
}

func (a ActionStarted) agentEvent() {
}

func (a ActionFinished) agentEvent() {
}

func (o ObservationRecorded) agentEvent() {
}

func (r RunStarted) agentEvent() {
}

func (s StepStarted) agentEvent() {
}

func (s StepCompleted) agentEvent() {
}

func (r RunCompleted) agentEvent() {
}

func (r RunStopped) agentEvent() {
}

func (r RunFailed) agentEvent() {
}
