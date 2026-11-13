package llm

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/store"
)

// OfflineEmbeddingsResponse answers an embeddings request with one
// deterministic unit vector per input, so identical text always embeds
// identically.
func OfflineEmbeddingsResponse(req *http.Request) (*http.Response, error) {
	var in struct {
		Input []string `json:"input"`
	}
	if err := json.NewDecoder(io.LimitReader(req.Body, 8<<20)).Decode(&in); err != nil {
		return nil, errors.New("offline transport: invalid embedding request")
	}
	data := make([]map[string]any, len(in.Input))
	for i, text := range in.Input {
		vec := make([]float32, EmbeddingDimensions)
		vec[int(sha256.Sum256([]byte(text))[0])*8%len(vec)] = 1
		data[i] = map[string]any{"index": i, "embedding": vec}
	}
	body, _ := json.Marshal(map[string]any{"data": data, "usage": map[string]int{"total_tokens": len(in.Input)}})
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
}

type offlineEmbeddings struct{}

func (offlineEmbeddings) RoundTrip(req *http.Request) (*http.Response, error) {
	return OfflineEmbeddingsResponse(req)
}

// NewOfflineEmbedder is an Embedder whose vectors come from
// OfflineEmbeddingsResponse; it never touches the network. Offline runs and
// evals use it so memory search takes the same path as in production.
func NewOfflineEmbedder(limits config.LimitsConfig, st *store.Store, opts Options) (*Embedder, error) {
	opts.HTTPClient = &http.Client{Transport: offlineEmbeddings{}}
	if opts.CapUSD == 0 && limits.MaxUSDPerDay <= 0 {
		opts.CapUSD = 1
	}
	return NewEmbedder("https://fixture.invalid", config.NewSecret("offline-dummy-key"), limits, st, opts)
}
