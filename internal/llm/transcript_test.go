package llm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
)

func TestTranscriptRecordsNudgeAndStoresSystemPromptOnce(t *testing.T) {
	const secret = "TOPSECRET-value"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(textReply)) }))
	defer server.Close()
	type row struct{ kind, body string }
	var rows []row
	client, err := llm.New(config.LLMConfig{BaseURL: server.URL + "/v1", Model: "m", APIKey: config.NewSecret("k")}, config.LimitsConfig{MaxStepsPerWake: 5, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{Now: time.Now, Redactor: config.NewRedactor(config.NewSecret(secret)),
		StopCheck:  func(map[string]bool, map[string]int) string { return "also " + secret },
		Transcript: func(_ context.Context, kind, body string) { rows = append(rows, row{kind, body}) }})
	if err != nil {
		t.Fatal(err)
	}
	tools := []llm.Tool{{Type: "function", Function: llm.Function{Name: "note_write", Parameters: json.RawMessage(`{"type":"object"}`)}}}
	messages := []llm.Message{{Role: "system", Content: "SYSTEM-PROMPT-TEXT"}, {Role: "user", Content: "go"}}
	if _, err = client.Run(context.Background(), "agent", 1, messages, tools, llm.DispatchFunc(func(context.Context, llm.ToolCall) (string, error) { return "ok", nil })); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	system := 0
	for _, r := range rows {
		kinds = append(kinds, r.kind)
		if strings.Contains(r.body, secret) {
			t.Fatalf("secret in %s row: %s", r.kind, r.body)
		}
		if strings.Contains(r.body, "SYSTEM-PROMPT-TEXT") {
			system++
		}
	}
	i := slices.Index(kinds, "user")
	if i < 1 || !slices.Contains(kinds[:i], "assistant") || !slices.Contains(kinds[i+1:], "assistant") {
		t.Fatalf("want a user row between two assistant rows, kinds=%v", kinds)
	}
	if !strings.Contains(rows[i].body, "Save what you learned") || system != 1 {
		t.Fatalf("user row %s, system prompt stored %d times", rows[i].body, system)
	}
}
