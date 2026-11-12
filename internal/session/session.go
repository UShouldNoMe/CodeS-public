package session

import (
	"CodeS/internal/conversation"
	"CodeS/internal/persistent"
	"context"
	"errors"
	"fmt"
	"strings"
)

const compactedContextPrefix = "Earlier conversation context was compacted into this summary:\n\n"

type Session struct {
	conversation *conversation.Conversation
	convDB       persistent.Store
	fileName     string
	compaction   *persistent.Compaction
}

func NewSession(ctx context.Context, db persistent.Store) (*Session, error) {
	if ctx == nil {
		return nil, errors.New("context is nil")
	}
	if db == nil {
		return nil, errors.New("persistent is nil")
	}

	fileName, err := db.CreateSession(ctx)
	if err != nil {
		return nil, err
	}
	return &Session{
		conversation: conversation.New(),
		convDB:       db,
		fileName:     fileName,
	}, nil
}

// NewTransient creates an independent, non-persistent session from a deep copy
// of the snapshot. A nil snapshot creates an empty session.
func NewTransient(snapshot *conversation.Conversation) *Session {
	return &Session{
		conversation: &conversation.Conversation{Messages: snapshot.GetMessage()},
	}
}

func (s *Session) Snapshot() *conversation.Conversation {
	return new(conversation.Conversation{
		Messages: s.conversation.GetMessage(),
	})
}

// LLMSnapshot returns the smaller conversation sent to the model. Snapshot
// remains the full, original history used by the TUI and other user-facing
// callers.
func (s *Session) LLMSnapshot() *conversation.Conversation {
	messages := s.conversation.GetMessage()
	if s.compaction == nil {
		return &conversation.Conversation{Messages: messages}
	}
	through := s.compaction.ThroughMessage
	if through < 0 || through > len(messages) {
		// A persisted boundary is validated on load. Keep this defensive fallback
		// so an invalid in-memory value can never hide original context.
		return &conversation.Conversation{Messages: messages}
	}
	compacted := make([]conversation.Message, 0, 1+len(messages)-through)
	compacted = append(compacted, conversation.Message{
		Role:    conversation.RoleUser,
		Content: compactedContextPrefix + s.compaction.Summary,
	})
	compacted = append(compacted, messages[through:]...)
	return &conversation.Conversation{Messages: compacted}
}

// ApplyCompaction changes only the context view used for future LLM requests.
// Persistent sessions append a boundary before changing memory; transient
// sessions only update their in-memory context view.
func (s *Session) ApplyCompaction(ctx context.Context, summary string) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.conversation == nil {
		return errors.New("session is not initialized")
	}
	if strings.TrimSpace(summary) == "" {
		return errors.New("compaction summary is empty")
	}
	boundary := persistent.Compaction{
		Summary:        summary,
		ThroughMessage: len(s.conversation.Messages),
	}
	if boundary.ThroughMessage == 0 {
		return errors.New("conversation is empty")
	}
	if s.convDB != nil {
		if err := s.convDB.AppendCompaction(ctx, s.fileName, boundary); err != nil {
			return fmt.Errorf("persist compaction: %w", err)
		}
	}
	s.compaction = &boundary
	return nil
}

// persistMessage validates both session modes and writes only when a Store exists.
func (s *Session) persistMessage(ctx context.Context, message conversation.Message) error {
	if ctx == nil {
		return errors.New("context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.conversation == nil {
		return errors.New("session is not initialized")
	}
	if s.convDB == nil {
		return nil
	}
	return s.convDB.AppendMessage(ctx, s.fileName, message)
}

func (s *Session) AddUser(ctx context.Context, content string) error {
	err := s.persistMessage(ctx, conversation.Message{
		Role:    conversation.RoleUser,
		Content: content,
	})
	if err != nil {
		return err
	}
	s.conversation.AddUser(content)
	return nil
}

func (s *Session) AddSystem(ctx context.Context, content string) error {
	if err := s.persistMessage(ctx, conversation.Message{
		Role: conversation.RoleSystem, Content: content,
	}); err != nil {
		return err
	}
	s.conversation.AddSystem(content)
	return nil
}

func (s *Session) AddAssistant(ctx context.Context, content string, toolCalls []conversation.ToolCall) error {
	err := s.persistMessage(ctx, conversation.Message{
		Role:      conversation.RoleAssistant,
		Content:   content,
		ToolCalls: toolCalls,
	})
	if err != nil {
		return err
	}
	s.conversation.AddAssistant(content, toolCalls)
	return nil
}

func (s *Session) AddToolResult(ctx context.Context, result conversation.ToolResult) error {
	resultCopy := result
	err := s.persistMessage(ctx, conversation.Message{
		Role:       conversation.RoleTool,
		ToolResult: &resultCopy,
	})
	if err != nil {
		return err
	}
	s.conversation.AddToolResult(result)
	return nil
}

func (s *Session) FileName() string { return s.fileName }

func OpenSession(context context.Context, fileName string, db persistent.Store) (*Session, error) {
	if fileName == "" || db == nil {
		return nil, errors.New("file name and db cannot be nil")
	}
	messages, err := db.LoadMessages(context, fileName)
	if err != nil {
		return nil, err
	}
	compaction, err := db.LoadCompaction(context, fileName)
	if err != nil {
		return nil, err
	}
	if compaction != nil && (compaction.ThroughMessage < 0 || compaction.ThroughMessage > len(messages)) {
		return nil, errors.New("compaction boundary is outside conversation history")
	}
	return new(Session{
		conversation: new(conversation.Conversation{
			Messages: messages,
		}),
		convDB:     db,
		fileName:   fileName,
		compaction: compaction,
	}), nil

}
