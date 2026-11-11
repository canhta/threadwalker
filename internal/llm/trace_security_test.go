package llm_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
	"github.com/canhta/threadwalker/internal/llm"
)

func TestTransportErrorDoesNotExposeProviderData(t *testing.T) {
	const poison = "fake-secret-private-note-third-party-user"
	client, err := llm.New(config.LLMConfig{BaseURL: "https://example.test/" + poison, Model: "test", APIKey: config.NewSecret(poison)}, config.LimitsConfig{MaxStepsPerWake: 1, MaxUSDPerDay: 1}, &memoryLedger{}, llm.Options{HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New(poison) })}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Run(context.Background(), "agent", 1, []llm.Message{{Role: "user", Content: poison}}, nil, nil)
	if err == nil || strings.Contains(err.Error(), poison) {
		t.Fatal("provider error was not sanitized")
	}
}
