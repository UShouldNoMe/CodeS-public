package persistent

import (
	"CodeS/internal/conversation"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// JSONLConvStore uses session filenames as stable keys, with no numeric index.
// The mutex serializes operations through this instance, not across processes.
type JSONLConvStore struct {
	mu  sync.Mutex
	dir string
}

// The first line contains both timestamps. Message lines do not change them.
// A session_updated record stores UpdatedAt when the session is left or closed.
type jsonlRecord struct {
	Type       string                `json:"type"`
	CreatedAt  *time.Time            `json:"created_at,omitempty"`
	UpdatedAt  *time.Time            `json:"updated_at,omitempty"`
	Message    *conversation.Message `json:"message,omitempty"`
	Compaction *Compaction           `json:"compaction,omitempty"`
}

var _ Store = (*JSONLConvStore)(nil)

func JSONLStoreNew(dir string) (*JSONLConvStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("JSONL directory is empty")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, fmt.Errorf("create JSONL directory: %w", err)
	}
	return &JSONLConvStore{dir: abs}, nil
}

func (s *JSONLConvStore) CreateSession(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	// The timestamp and random suffix identify a file, not a persisted session ID.
	f, err := os.CreateTemp(s.dir, now.Format("20060102T150405.000000000Z")+"-*.jsonl")
	if err != nil {
		return "", err
	}
	err = json.NewEncoder(f).Encode(jsonlRecord{Type: "session", CreatedAt: &now, UpdatedAt: &now})
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return "", errors.Join(err, os.Remove(f.Name()))
	}
	return filepath.Base(f.Name()), nil
}

func (s *JSONLConvStore) path(id string) (string, error) {
	name := id
	if name == "" || name == "." || filepath.Base(name) != name || strings.ContainsAny(name, "/\\\\:") || !strings.HasSuffix(name, ".jsonl") {
		return "", fmt.Errorf("%w: invalid session filename %q", ErrSessionNotFound, name)
	}
	path := filepath.Join(s.dir, name)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("session file is not a regular file: %s", name)
	}
	return path, nil
}

func (s *JSONLConvStore) AppendMessage(ctx context.Context, id string, message conversation.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(id)
	if err != nil {
		return err
	}
	// Validate before appending, so a truncated/corrupt file is never silently
	// extended. In particular, a partial final line must not swallow a new record.
	_, _, _, err = readJSONL(ctx, path, false)
	if err != nil {
		return err
	}
	return appendJSONL(ctx, path, jsonlRecord{Type: "message", Message: &message})
}

func (s *JSONLConvStore) AppendCompaction(ctx context.Context, id string, compaction Compaction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(id)
	if err != nil {
		return err
	}
	messages, _, _, err := readJSONL(ctx, path, true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(compaction.Summary) == "" {
		return errors.New("compaction summary is empty")
	}
	if compaction.ThroughMessage != len(messages) {
		return fmt.Errorf(
			"compaction boundary %d does not match current message count %d",
			compaction.ThroughMessage,
			len(messages),
		)
	}
	compactionCopy := compaction
	return appendJSONL(ctx, path, jsonlRecord{
		Type:       "compaction",
		Compaction: &compactionCopy,
	})
}

func (s *JSONLConvStore) TouchSession(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(id)
	if err != nil {
		return err
	}
	_, info, _, err := readJSONL(ctx, path, false)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if !now.After(info.UpdatedAt) {
		now = info.UpdatedAt.Add(time.Nanosecond)
	}
	return appendJSONL(ctx, path, jsonlRecord{Type: "session_updated", UpdatedAt: &now})
}

func appendJSONL(ctx context.Context, path string, record jsonlRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	data = append(data, '\n')
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	if err != nil {
		// Restore the previous complete transcript when this write fails.
		return errors.Join(err, f.Truncate(stat.Size()), f.Sync())
	}
	return nil
}

func (s *JSONLConvStore) LoadMessages(ctx context.Context, id string) ([]conversation.Message, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	messages, _, _, err := readJSONL(ctx, path, true)
	return messages, err
}

func (s *JSONLConvStore) LoadCompaction(ctx context.Context, id string) (*Compaction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	_, _, compaction, err := readJSONL(ctx, path, false)
	return compaction, err
}

func (s *JSONLConvStore) ListSessions(ctx context.Context) ([]SessionInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	result := make([]SessionInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		fileName := entry.Name()
		path, err := s.path(fileName)
		if errors.Is(err, ErrSessionNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		_, info, _, err := readJSONL(ctx, path, false)
		if err != nil {
			return nil, err
		}
		info.FileName = fileName
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].FileName < result[j].FileName
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result, nil
}

func (s *JSONLConvStore) DeleteSession(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	return nil
}

func readJSONL(ctx context.Context, path string, retainMessages bool) ([]conversation.Message, SessionInfo, *Compaction, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, SessionInfo{}, nil, err
	}
	defer f.Close()
	reader := bufio.NewReader(f)
	info := SessionInfo{}
	messages := make([]conversation.Message, 0)
	messageCount := 0
	var latestCompaction *Compaction
	foundPrompt := false
	for line := 1; ; line++ {
		if err := ctx.Err(); err != nil {
			return nil, info, nil, err
		}
		data, err := reader.ReadBytes('\n') // No Scanner's 64 KiB line limit.
		if errors.Is(err, io.EOF) && len(data) == 0 && line > 1 {
			return messages, info, latestCompaction, nil
		}
		if err != nil {
			return nil, info, nil, fmt.Errorf("read %s line %d (empty or incomplete JSONL record): %w", path, line, err)
		}
		var record jsonlRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return nil, info, nil, fmt.Errorf("decode %s line %d: %w", path, line, err)
		}
		if line == 1 {
			if record.Type != "session" || record.CreatedAt == nil || record.CreatedAt.IsZero() || record.UpdatedAt == nil || record.Message != nil || record.Compaction != nil || !record.UpdatedAt.Equal(*record.CreatedAt) {
				return nil, info, nil, fmt.Errorf("%s: invalid session metadata", path)
			}
			info.CreatedAt = *record.CreatedAt
			info.UpdatedAt = *record.UpdatedAt
		} else if record.Type == "session_updated" {
			if record.UpdatedAt == nil || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(info.UpdatedAt) || record.CreatedAt != nil || record.Message != nil || record.Compaction != nil {
				return nil, info, nil, fmt.Errorf("%s line %d: invalid session update", path, line)
			}
			info.UpdatedAt = *record.UpdatedAt
		} else if record.Type == "compaction" {
			if record.Compaction == nil || record.CreatedAt != nil || record.UpdatedAt != nil || record.Message != nil || strings.TrimSpace(record.Compaction.Summary) == "" || record.Compaction.ThroughMessage != messageCount {
				return nil, info, nil, fmt.Errorf("%s line %d: invalid compaction record", path, line)
			}
			copy := *record.Compaction
			latestCompaction = &copy
		} else {
			if record.Type != "message" || record.Message == nil || record.CreatedAt != nil || record.UpdatedAt != nil || record.Compaction != nil {
				return nil, info, nil, fmt.Errorf("%s line %d: invalid message record", path, line)
			}
			messageCount++
			if !foundPrompt && record.Message.Role == conversation.RoleUser {
				info.FirstPrompt = record.Message.Content
				foundPrompt = true
			}
			if retainMessages {
				messages = append(messages, *record.Message)
			}
		}
	}
}
