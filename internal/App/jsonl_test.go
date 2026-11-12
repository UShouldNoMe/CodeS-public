package App

import (
	"CodeS/internal/Agent"
	"CodeS/internal/MCP"
	"CodeS/internal/conversation"
	store2 "CodeS/internal/persistent"
	toolmanager "CodeS/internal/tools/manager"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func jsonlTestApp(t *testing.T, dir string) *App {
	t.Helper()
	database, err := store2.JSONLStoreNew(dir)
	if err != nil {
		t.Fatal(err)
	}
	registry := toolmanager.NewRegistry()
	manager, err := MCP.NewManager(nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Agent.NewAgent(staticClient{content: "hello"}, registry, 2, toolmanager.New(nil)), database, manager, registry)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Error(err)
		}
	})
	return a
}

func findSessionInfo(t *testing.T, a *App, id string) store2.SessionInfo {
	t.Helper()
	infos, err := a.ListSessions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.FileName == id {
			return info
		}
	}
	t.Fatalf("missing session %v", id)
	return store2.SessionInfo{}
}

func TestJSONLAppUpdatesTimeOnSessionExitAndClose(t *testing.T) {
	dir := t.TempDir()
	a := jsonlTestApp(t, dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := a.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	created := findSessionInfo(t, a, first)
	collectAgentEvents(a.ChatStream(ctx, "question"))
	wantMessages := a.CurrentMessages()
	if len(wantMessages) != 2 {
		t.Fatalf("chat did not persist two messages: %+v", wantMessages)
	}
	if info := findSessionInfo(t, a, first); !info.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatal("chat advanced UpdatedAt")
	}
	if err := a.SwitchSession(ctx, "999"); !errors.Is(err, store2.ErrSessionNotFound) {
		t.Fatalf("switch missing: %v", err)
	}
	if info := findSessionInfo(t, a, first); !info.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatal("failed switch advanced UpdatedAt")
	}
	second, err := a.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	firstExit := findSessionInfo(t, a, first)
	collectAgentEvents(a.ChatStream(ctx, "second question"))
	secondCreated := findSessionInfo(t, a, second)
	if !firstExit.UpdatedAt.After(created.UpdatedAt) || !firstExit.CreatedAt.Equal(created.CreatedAt) {
		t.Fatal("new session did not record prior session exit")
	}
	if err := a.SwitchSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	if info := findSessionInfo(t, a, second); !info.UpdatedAt.After(secondCreated.UpdatedAt) {
		t.Fatal("switch did not record exited session time")
	}
	if info := findSessionInfo(t, a, first); !info.UpdatedAt.Equal(firstExit.UpdatedAt) {
		t.Fatal("resuming target changed its exit time")
	}
	if !reflect.DeepEqual(a.CurrentMessages(), wantMessages) {
		t.Fatal("metadata records polluted restored conversation")
	}
	if err := a.ClearCurrentSession(); err != nil {
		t.Fatal(err)
	}
	cleared := findSessionInfo(t, a, first)
	if a.CurrentSessionFileName() != "" || !cleared.UpdatedAt.After(firstExit.UpdatedAt) {
		t.Fatal("clear did not save exit time")
	}
	if err := a.SwitchSession(ctx, first); err != nil {
		t.Fatal(err)
	}
	// Signal cancellation must not prevent the graceful shutdown timestamp.
	cancel()
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	closed := findSessionInfo(t, a, first)
	if !closed.UpdatedAt.After(cleared.UpdatedAt) {
		t.Fatal("close did not update current session")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if info := findSessionInfo(t, a, first); !info.UpdatedAt.Equal(closed.UpdatedAt) {
		t.Fatal("repeated Close updated twice")
	}
	b := jsonlTestApp(t, dir)
	infos, err := b.ListSessions(context.Background())
	if err != nil || len(infos) != 2 {
		t.Fatalf("reopen: %v, %v", infos, err)
	}
	if infos[0].FirstPrompt != "question" || !infos[0].UpdatedAt.Equal(closed.UpdatedAt) {
		t.Fatalf("exit timestamp was not persisted: %+v", infos[0])
	}
	if err := b.SwitchSession(context.Background(), infos[0].FileName); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.CurrentMessages(), wantMessages) {
		t.Fatal("conversation did not survive app restart")
	}
}

type failingTouchStore struct {
	*memoryStore
	err error
}

func (s *failingTouchStore) TouchSession(context.Context, string) error { return s.err }

func TestAppPreservesSessionAndReportsExitWriteFailure(t *testing.T) {
	wantErr := errors.New("disk write failed")
	database := &failingTouchStore{memoryStore: newMemoryStore(), err: wantErr}
	registry := toolmanager.NewRegistry()
	manager, err := MCP.NewManager(nil)
	if err != nil {
		t.Fatal(err)
	}
	a, err := New(Agent.NewAgent(staticClient{}, registry, 1, toolmanager.New(nil)), database, manager, registry)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	first, err := a.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.AppendMessage(ctx, first, conversation.Message{Role: conversation.RoleUser, Content: "keep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.NewSession(ctx); !errors.Is(err, wantErr) {
		t.Fatalf("new session: %v", err)
	}
	if len(database.messages) != 1 || a.CurrentSessionFileName() != first {
		t.Fatal("failed exit switched sessions or left an orphan")
	}
	if err := a.ClearCurrentSession(); !errors.Is(err, wantErr) || a.CurrentSessionFileName() != first {
		t.Fatalf("clear lost session/error: %v", err)
	}
	if err := a.Close(); !errors.Is(err, wantErr) {
		t.Fatalf("close swallowed timestamp failure: %v", err)
	}
	if err := a.Close(); !errors.Is(err, wantErr) || database.closeCalls != 1 {
		t.Fatalf("repeated close: %v, count %v", err, database.closeCalls)
	}
}

func TestEmptySessionsAreRemovedOnNewSwitchClearAndClose(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	a := jsonlTestApp(t, dir)
	var previous string
	for i := 0; i < 3; i++ {
		id, err := a.NewSession(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Base(id) != id || filepath.Ext(id) != ".jsonl" {
			t.Fatalf("session key is not a filename: %q", id)
		}
		if previous != "" {
			if _, err := os.Stat(filepath.Join(dir, previous)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("previous empty session persisted: %v", err)
			}
		}
		previous = id
	}
	if err := a.ClearCurrentSession(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("clear left files: %v, %v", entries, err)
	}
	saved, err := a.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A user message without a successful assistant reply still counts.
	if err := a.currentSession.AddUser(ctx, "keep this question"); err != nil {
		t.Fatal(err)
	}
	empty, err := a.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SwitchSession(ctx, "missing.jsonl"); !errors.Is(err, store2.ErrSessionNotFound) {
		t.Fatal(err)
	}
	if a.CurrentSessionFileName() != empty {
		t.Fatal("failed switch discarded current session")
	}
	if err := a.SwitchSession(ctx, saved); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, empty)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("switch left empty file: %v", err)
	}
	if _, err := a.NewSession(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != saved {
		t.Fatalf("close did not preserve only nonempty history: %v, %v", entries, err)
	}
	reopened, err := store2.JSONLStoreNew(dir)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := reopened.LoadMessages(ctx, saved)
	if err != nil || len(messages) != 1 || messages[0].Content != "keep this question" {
		t.Fatalf("filename key failed after restart: %v, %v", messages, err)
	}
}

type failingDeleteStore struct {
	*memoryStore
}

func (s *failingDeleteStore) DeleteSession(context.Context, string) error {
	return errors.New("cannot remove empty session")
}

func TestEmptySessionCleanupFailureKeepsCurrentSession(t *testing.T) {
	a := jsonlTestApp(t, t.TempDir())
	original := a.store
	db := &failingDeleteStore{newMemoryStore()}
	a.store = db
	id, err := a.NewSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ClearCurrentSession(); err == nil || a.CurrentSessionFileName() != id {
		t.Fatal("failed cleanup discarded current session")
	}
	// Restore the test app so cleanup need not report the injected failure.
	a.currentSession = nil
	a.store = original
}
