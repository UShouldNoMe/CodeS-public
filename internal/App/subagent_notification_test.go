package App

import (
	"CodeS/internal/Agent"
	"CodeS/internal/MCP"
	"CodeS/internal/conversation"
	"CodeS/internal/subAgent"
	toolmanager "CodeS/internal/tools/manager"
	"context"
	"strings"
	"testing"
)

func TestForkCompletionNotificationFollowsParentSession(t *testing.T) {
	ctx := context.Background()
	database := newMemoryStore()
	registry := toolmanager.NewRegistry()
	client := &compactRecordingClient{}
	agent := Agent.NewAgent(client, registry, 2, toolmanager.New(nil))
	mcpManager, err := MCP.NewManager(nil)
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(agent, database, mcpManager, registry)
	if err != nil {
		t.Fatal(err)
	}
	agent.BeforeRequest = application.deliverTaskNotifications
	t.Cleanup(func() {
		if err := application.Close(); err != nil {
			t.Error(err)
		}
	})

	parentID, err := application.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	collectAgentEvents(application.ChatStream(ctx, "parent prompt"))
	application.enqueueTaskNotification(subAgent.Task{
		ID: "fork-success", ParentSessionID: parentID, Status: "completed",
		Result: Agent.RunResult{FinalContent: "child finding"},
	})
	application.enqueueTaskNotification(subAgent.Task{
		ID: "fork-failure", ParentSessionID: parentID, Status: "failed",
		Error: "provider unavailable",
	})

	otherID, err := application.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	collectAgentEvents(application.ChatStream(ctx, "other prompt"))
	otherRequest := client.streamRequests[len(client.streamRequests)-1]
	if containsTaskNotification(otherRequest.Conversation.Messages) {
		t.Fatalf("task notification leaked into session %s", otherID)
	}
	if err := application.SwitchSession(ctx, parentID); err != nil {
		t.Fatal(err)
	}
	collectAgentEvents(application.ChatStream(ctx, "continue parent"))
	parentRequest := client.streamRequests[len(client.streamRequests)-1]
	var successCount, failureCount int
	for _, message := range parentRequest.Conversation.Messages {
		if message.Role != conversation.RoleSystem {
			continue
		}
		if strings.Contains(message.Content, "<task_id>fork-success</task_id>") &&
			strings.Contains(message.Content, "<status>completed</status>") &&
			strings.Contains(message.Content, "child finding") {
			successCount++
		}
		if strings.Contains(message.Content, "<task_id>fork-failure</task_id>") &&
			strings.Contains(message.Content, "<status>failed</status>") &&
			strings.Contains(message.Content, "provider unavailable") {
			failureCount++
		}
	}
	if successCount != 1 || failureCount != 1 {
		t.Fatalf("parent notifications: success = %d, failure = %d, messages = %#v", successCount, failureCount, parentRequest.Conversation.Messages)
	}

	collectAgentEvents(application.ChatStream(ctx, "continue again"))
	var persistedNotifications int
	for _, message := range application.CurrentMessages() {
		if message.Role == conversation.RoleSystem && strings.Contains(message.Content, "<task-notification>") {
			persistedNotifications++
		}
	}
	if persistedNotifications != 2 {
		t.Fatalf("persisted task notifications = %d, want 2", persistedNotifications)
	}
}

func containsTaskNotification(messages []conversation.Message) bool {
	for _, message := range messages {
		if strings.Contains(message.Content, "<task-notification>") {
			return true
		}
	}
	return false
}
