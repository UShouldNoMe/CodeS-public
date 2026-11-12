package App

import (
	"CodeS/config"
	"CodeS/internal/conversation"
	"CodeS/internal/llm"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestModelSelectionPreservesSessionAndFailedSwitch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" || r.Header.Get("Authorization") != "Bearer test-key" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"data":[{"id":"test-model"}]}`))
	}))
	defer server.Close()
	t.Setenv("CODES_TEST_KEY", "test-key")
	t.Setenv("CODES_MISSING_KEY", "")
	a := jsonlTestApp(t, t.TempDir())
	a.setProviders([]config.ProviderConfig{
		{Name: "first", Protocol: "openai-compatible", BaseURL: server.URL + "/v1", APIKeyEnv: "CODES_TEST_KEY"},
		{Name: "second", Protocol: "openai-compatible", BaseURL: server.URL + "/v1", APIKeyEnv: "CODES_TEST_KEY"},
		{Name: "broken", Protocol: "openai-compatible", BaseURL: server.URL + "/v1", APIKeyEnv: "CODES_MISSING_KEY"},
	})
	ctx := context.Background()
	id, err := a.NewSession(ctx)
	if err != nil {
		t.Fatal(err)
	}
	collectAgentEvents(a.ChatStream(ctx, "keep this history"))
	before := a.CurrentMessages()
	models, err := a.ListModels(ctx, "second")
	if err != nil || len(models) != 1 || models[0].Provider != "second" {
		t.Fatalf("ListModels = %#v, %v", models, err)
	}
	for _, provider := range []string{"first", "second"} {
		if err := a.SwitchModel(ctx, provider, "test-model"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.SwitchModel(ctx, "broken", "test-model"); err == nil {
		t.Fatal("expected failure")
	}
	if a.CurrentModel().Provider != "second" {
		t.Fatal("failed switch replaced current model")
	}
	if a.CurrentSessionFileName() != id || !reflect.DeepEqual(before, a.CurrentMessages()) {
		t.Fatal("switch changed session/history")
	}
}

type blockingModelClient struct {
	staticClient
	started chan struct{}
	release chan struct{}
}

func (c *blockingModelClient) Stream(ctx context.Context, r *llm.Request, emit func(conversation.StreamEvent) error) (*conversation.Response, error) {
	close(c.started)
	<-c.release
	return c.staticClient.Stream(ctx, r, emit)
}

func TestSwitchModelRejectsActiveAgent(t *testing.T) {
	a := jsonlTestApp(t, t.TempDir())
	c := &blockingModelClient{started: make(chan struct{}), release: make(chan struct{})}
	if err := a.agent.SetClient(c); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODES_TEST_KEY", "test-key")
	a.setProviders([]config.ProviderConfig{{Name: "test", Protocol: "openai-compatible", BaseURL: "http://localhost:1234/v1", APIKeyEnv: "CODES_TEST_KEY"}})
	if _, err := a.NewSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	events := a.ChatStream(context.Background(), "hello")
	<-c.started
	err := a.SwitchModel(context.Background(), "test", "model")
	close(c.release)
	for range events {
	}
	if err == nil {
		t.Fatal("switch allowed during active turn")
	}
	if a.CurrentModel() != nil {
		t.Fatal("failed switch changed selection")
	}
}
