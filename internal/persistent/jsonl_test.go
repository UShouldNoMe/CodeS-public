package persistent

import (
	"CodeS/internal/conversation"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func newJSONLTestStore(t *testing.T) *JSONLConvStore {
	t.Helper()
	s, err := JSONLStoreNew(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJSONLSessionSurvivesRestartWithoutPersistedIDs(t *testing.T) {
	s := newJSONLTestStore(t)
	ctx := context.Background()
	id, err := s.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := s.ListSessions(ctx)
	if err != nil || len(initial) != 1 {
		t.Fatalf("list new session: %v, %v", initial, err)
	}
	if initial[0].CreatedAt.IsZero() || !initial[0].UpdatedAt.Equal(initial[0].CreatedAt) {
		t.Fatalf("invalid initial timestamps: %+v", initial[0])
	}
	want := []conversation.Message{
		{Role: conversation.RoleSystem, Content: "instructions"},
		{Role: conversation.RoleUser, Content: "请读取文件\n保留换行"},
		{Role: conversation.RoleAssistant, ToolCalls: []conversation.ToolCall{{ID: "call-1", Name: "ReadFile", Arguments: map[string]any{"path": "go.mod"}}}},
		{Role: conversation.RoleTool, ToolResult: &conversation.ToolResult{ToolCallID: "call-1", ToolName: "ReadFile", Status: conversation.ToolResultSuccess, Content: strings.Repeat("文", 30000)}},
		{Role: conversation.RoleAssistant, Content: "读取完成"},
	}
	for _, message := range want {
		if err := s.AppendMessage(ctx, id, message); err != nil {
			t.Fatal(err)
		}
	}
	before, err := s.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !before[0].UpdatedAt.Equal(initial[0].UpdatedAt) {
		t.Fatal("appending messages changed the session exit time")
	}
	if err := s.TouchSession(ctx, id); err != nil {
		t.Fatal(err)
	}
	before, err = s.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !before[0].CreatedAt.Equal(initial[0].CreatedAt) || !before[0].UpdatedAt.After(initial[0].UpdatedAt) {
		t.Fatalf("timestamps did not preserve creation/advance update: %+v", before[0])
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil || len(entries) != 1 || !strings.HasSuffix(entries[0].Name(), ".jsonl") {
		t.Fatalf("expected only a session JSONL file: %v, %v", entries, err)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != len(want)+2 {
		t.Fatalf("got %v JSONL lines", len(lines))
	}
	for _, line := range lines {
		var record map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		for key := range record {
			if key != "type" && key != "created_at" && key != "updated_at" && key != "message" {
				t.Fatalf("unexpected persisted metadata: %s", key)
			}
		}
	}
	// Restoring needs no original runtime map, counter, or numeric filename.
	if err := os.Rename(filepath.Join(s.dir, entries[0].Name()), filepath.Join(s.dir, "renamed-chat.jsonl")); err != nil {
		t.Fatal(err)
	}
	reopened, err := JSONLStoreNew(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.ListSessions(ctx)
	if err != nil || len(after) != 1 {
		t.Fatalf("reopen list: %v, %v", after, err)
	}
	got, err := reopened.LoadMessages(ctx, after[0].FileName)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("messages not restored correctly: %v", err)
	}
	if after[0].FirstPrompt != want[1].Content || !after[0].CreatedAt.Equal(before[0].CreatedAt) || !after[0].UpdatedAt.Equal(before[0].UpdatedAt) {
		t.Fatalf("metadata changed on read/restart: %+v", after[0])
	}
	if err := reopened.AppendMessage(ctx, after[0].FileName, conversation.Message{Role: conversation.RoleUser, Content: "继续"}); err != nil {
		t.Fatal(err)
	}
	continued, err := reopened.LoadMessages(ctx, after[0].FileName)
	if err != nil || len(continued) != len(want)+1 {
		t.Fatalf("append after restart: %v messages, %v", len(continued), err)
	}
}

func TestJSONLCompactionPreservesOriginalMessagesAndRestoresBoundary(t *testing.T) {
	s := newJSONLTestStore(t)
	ctx := context.Background()
	id, err := s.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantMessages := []conversation.Message{
		{Role: conversation.RoleUser, Content: "old question"},
		{Role: conversation.RoleAssistant, Content: "old answer"},
	}
	for _, message := range wantMessages {
		if err := s.AppendMessage(ctx, id, message); err != nil {
			t.Fatal(err)
		}
	}
	wantCompaction := Compaction{Summary: "the earlier exchange was summarized", ThroughMessage: 2}
	if err := s.AppendCompaction(ctx, id, wantCompaction); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(ctx, id, conversation.Message{Role: conversation.RoleUser, Content: "new question"}); err != nil {
		t.Fatal(err)
	}
	wantMessages = append(wantMessages, conversation.Message{Role: conversation.RoleUser, Content: "new question"})

	reopened, err := JSONLStoreNew(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	gotMessages, err := reopened.LoadMessages(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotMessages, wantMessages) {
		t.Fatalf("LoadMessages() = %#v, want complete original history %#v", gotMessages, wantMessages)
	}
	gotCompaction, err := reopened.LoadCompaction(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if gotCompaction == nil || *gotCompaction != wantCompaction {
		t.Fatalf("LoadCompaction() = %#v, want %#v", gotCompaction, wantCompaction)
	}
}

func TestJSONLRejectsCompactionAtWrongBoundary(t *testing.T) {
	s := newJSONLTestStore(t)
	ctx := context.Background()
	id, err := s.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(ctx, id, conversation.Message{Role: conversation.RoleUser, Content: "question"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendCompaction(ctx, id, Compaction{Summary: "summary", ThroughMessage: 0}); err == nil {
		t.Fatal("AppendCompaction() accepted a stale boundary")
	}
	if compaction, err := s.LoadCompaction(ctx, id); err != nil || compaction != nil {
		t.Fatalf("failed append changed compaction: %#v, %v", compaction, err)
	}
}

func TestJSONLSessionsOrderDeletionAndMissingHandles(t *testing.T) {
	s := newJSONLTestStore(t)
	ctx := context.Background()
	first, err := s.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessage(ctx, first, conversation.Message{Role: conversation.RoleUser, Content: "latest"}); err != nil {
		t.Fatal(err)
	}
	if err := s.TouchSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	infos, err := s.ListSessions(ctx)
	if err != nil || len(infos) != 2 || infos[0].FileName != first || infos[1].FileName != second {
		t.Fatalf("expected newest modified first: %+v, %v", infos, err)
	}
	if err := s.DeleteSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadMessages(ctx, first); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("load deleted: %v", err)
	}
	if err := s.AppendMessage(ctx, first, conversation.Message{}); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("append deleted: %v", err)
	}
	if err := s.DeleteSession(ctx, first); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
	third, err := s.CreateSession(ctx)
	if err != nil || third == first || third == second {
		t.Fatalf("reused session filename: %v, %v", third, err)
	}
	// A deletion outside this instance disappears from subsequent listings.
	path, err := s.path(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	infos, err = s.ListSessions(ctx)
	if err != nil || len(infos) != 1 || infos[0].FileName != third {
		t.Fatalf("unexpected remaining sessions: %+v, %v", infos, err)
	}
}

func TestJSONLRejectsCorruptionWithoutAppending(t *testing.T) {
	for _, invalid := range []string{"", "{", "{}\n", "{broken}\n"} {
		t.Run(invalid, func(t *testing.T) {
			s := newJSONLTestStore(t)
			ctx := context.Background()
			id, err := s.CreateSession(ctx)
			if err != nil {
				t.Fatal(err)
			}
			path, err := s.path(id)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(invalid), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoadMessages(ctx, id); err == nil {
				t.Fatal("loaded corrupt file")
			}
			if err := s.AppendMessage(ctx, id, conversation.Message{}); err == nil {
				t.Fatal("appended to corrupt file")
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != invalid {
				t.Fatalf("changed corrupt file: %q, %v", data, err)
			}
		})
	}
}

func TestJSONLCancelledOperationsDoNotChangeDisk(t *testing.T) {
	s := newJSONLTestStore(t)
	id, err := s.CreateSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	path, err := s.path(id)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.CreateSession(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("create: %v", err)
	}
	if err := s.AppendMessage(ctx, id, conversation.Message{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("append: %v", err)
	}
	if err := s.DeleteSession(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("delete: %v", err)
	}
	if err := s.TouchSession(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("touch: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(before) {
		t.Fatalf("cancelled operation modified file: %v", err)
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cancelled create left a file: %v, %v", entries, err)
	}
}

func TestJSONLSerializesConcurrentAppends(t *testing.T) {
	s := newJSONLTestStore(t)
	ctx := context.Background()
	id, err := s.CreateSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.AppendMessage(ctx, id, conversation.Message{Role: conversation.RoleUser, Content: "hello"}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	messages, err := s.LoadMessages(ctx, id)
	if err != nil || len(messages) != 12 {
		t.Fatalf("lost/interleaved messages: %v, %v", len(messages), err)
	}
}

func TestJSONLRejectsPathsOutsideSessionDirectory(t *testing.T) {
	s := newJSONLTestStore(t)
	for _, name := range []string{"../other.jsonl", "..\\other.jsonl", "/tmp/other.jsonl", "C:\\other.jsonl", "chat.jsonl:stream", ""} {
		if _, err := s.LoadMessages(context.Background(), name); !errors.Is(err, ErrSessionNotFound) {
			t.Fatalf("accepted invalid filename %q: %v", name, err)
		}
	}
}
