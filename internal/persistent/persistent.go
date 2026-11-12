package persistent

import (
	"CodeS/internal/conversation"
	"context"
	"errors"
	"time"
)

var ErrSessionNotFound = errors.New("session not found")

type SessionInfo struct {
	// FileName is the JSONL file used to locate this conversation.
	FileName    string
	FirstPrompt string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Compaction records which original messages have been replaced by Summary
// when constructing future LLM requests. The original messages remain in the
// session log and are still available to the UI.
type Compaction struct {
	Summary        string `json:"summary"`
	ThroughMessage int    `json:"through_message"`
}

type Store interface {
	CreateSession(ctx context.Context) (string, error)
	AppendMessage(ctx context.Context, fileName string, message conversation.Message) error
	AppendCompaction(ctx context.Context, fileName string, compaction Compaction) error
	LoadMessages(ctx context.Context, fileName string) ([]conversation.Message, error)
	LoadCompaction(ctx context.Context, fileName string) (*Compaction, error)
	ListSessions(ctx context.Context) ([]SessionInfo, error)
	DeleteSession(ctx context.Context, fileName string) error
	TouchSession(ctx context.Context, fileName string) error
}
