package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/logging"
	"github.com/canhta/threadz/internal/store"
)

const (
	// EmbeddingBaseURL, EmbeddingModel and EmbeddingDimensions are OpenAI's
	// text-embedding-3-large endpoint and its full output size.
	EmbeddingBaseURL    = "https://api.openai.com/v1"
	EmbeddingModel      = "text-embedding-3-large"
	EmbeddingDimensions = 3072
	// embeddingUSDPerMTok is OpenAI's list price for text-embedding-3-large
	// input tokens; the API reports tokens, not cost.
	embeddingUSDPerMTok = 0.13
	embeddingBatch      = 64
	embeddingBodyLimit  = 16 << 20
)

// Reasons an embedding request fails; they are the reason codes logged and
// returned by EmbeddingFailureReason.
const (
	reasonEmbeddingTransport = "embedding_transport_error"
	reasonEmbeddingHTTP      = "embedding_http_error"
	reasonEmbeddingResponse  = "embedding_invalid_response"
	reasonEmbeddingAccount   = "embedding_accounting_failed"
	reasonEmbeddingBudget    = "budget_limit"
)

// Embedder turns text into text-embedding-3-large vectors and stores the ones
// the agent's notes and actions need. Its spend goes to the ledger under the
// phase of the wake that asked, against the same daily cap as chat.
type Embedder struct {
	baseURL string
	key     config.Secret
	store   *store.Store
	limits  config.LimitsConfig
	http    *http.Client
	now     func() time.Time
	timeout time.Duration
	logger  *slog.Logger
	phase   string
	capUSD  float64
}

// NewEmbedder uses opts.Phase, CapUSD, HTTPClient, Now, RequestTimeout and
// Logger exactly as New does.
func NewEmbedder(baseURL string, key config.Secret, limits config.LimitsConfig, st *store.Store, opts Options) (*Embedder, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("configuring embedder: invalid base URL")
	}
	if key.Expose() == "" || st == nil || opts.RequestTimeout < 0 {
		return nil, errors.New("configuring embedder: key, store and nonnegative timeout required")
	}
	phase, capUSD, err := resolvePhase(opts, limits)
	if err != nil {
		return nil, err
	}
	e := &Embedder{baseURL: baseURL, key: key, store: st, limits: limits, http: opts.HTTPClient, now: opts.Now, timeout: opts.RequestTimeout, logger: opts.Logger, phase: phase, capUSD: capUSD}
	if e.http == nil {
		e.http = &http.Client{}
	}
	client := *e.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	e.http = &client
	if e.now == nil {
		e.now = time.Now
	}
	if e.timeout == 0 {
		e.timeout = 30 * time.Second
	}
	return e, nil
}

// EmbeddingFailureReason is the logged reason code for an Embed or Sync error.
func EmbeddingFailureReason(err error) string { return errorReason(err) }

// Embed returns one vector per text and records the billed tokens under the
// wake's phase; wakeID 0 records them under the agent phase.
func (e *Embedder) Embed(ctx context.Context, agentID string, wakeID int64, texts []string) ([][]float32, error) {
	day := e.limits.Day(e.now())
	spent, err := spentForPhase(ctx, e.store, e.phase, agentID, day)
	if err != nil {
		return nil, failure("checking daily embedding budget", reasonEmbeddingAccount, err)
	}
	if spent.CostUSD >= e.capUSD {
		return nil, failure("requesting embeddings", reasonEmbeddingBudget, ErrBudget)
	}
	body, _ := json.Marshal(map[string]any{"model": EmbeddingModel, "input": texts})
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(e.baseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, failure("creating embedding request", reasonEmbeddingTransport, err)
	}
	openAIAdapter{}.authorize(req, e.key.Expose())
	req.Header.Set("Content-Type", "application/json")
	res, err := e.http.Do(req)
	if err != nil {
		return nil, failure("sending embedding request", reasonEmbeddingTransport, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, failure("requesting embeddings", reasonEmbeddingHTTP, nil)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, embeddingBodyLimit+1))
	if err != nil || len(raw) > embeddingBodyLimit {
		return nil, failure("reading embedding response", reasonEmbeddingResponse, err)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			TotalTokens int64 `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, failure("decoding embedding response", reasonEmbeddingResponse, err)
	}
	if len(out.Data) != len(texts) || out.Usage.TotalTokens < 0 {
		return nil, failure("reading embedding response", reasonEmbeddingResponse, nil)
	}
	vectors := make([][]float32, len(texts))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(texts) || vectors[d.Index] != nil || len(d.Embedding) != EmbeddingDimensions {
			return nil, failure("reading embedding response", reasonEmbeddingResponse, nil)
		}
		vectors[d.Index] = d.Embedding
	}
	usage := Usage{TokensIn: out.Usage.TotalTokens, CostUSD: float64(out.Usage.TotalTokens) / 1e6 * embeddingUSDPerMTok}
	// Completed usage survives wake cancellation; bound this write separately.
	accountingCtx, cancelAccounting := context.WithTimeout(context.WithoutCancel(ctx), e.timeout)
	defer cancelAccounting()
	if wakeID == 0 {
		err = e.store.RecordUnattributedLLMUsage(accountingCtx, agentID, day, usage)
	} else {
		err = e.store.RecordLLMUsage(accountingCtx, agentID, wakeID, day, usage)
	}
	if err != nil {
		return nil, failure("recording embedding usage", reasonEmbeddingAccount, err)
	}
	return vectors, nil
}

// Sync embeds every note and action of the agent that has no vector, once per
// distinct text. A failure leaves the remaining rows pending for the next
// call. It returns how many rows gained a vector.
func (e *Embedder) Sync(ctx context.Context, agentID string, wakeID int64) (int, error) {
	started := e.now()
	synced, err := e.syncPending(ctx, agentID, wakeID)
	event, outcome, reason := logging.EmbeddingSynced, "success", "completed"
	if wakeID == 0 {
		event = logging.EmbeddingBackfilled
	}
	level := slog.LevelInfo
	if err != nil {
		level, outcome, reason = slog.LevelWarn, "error", errorReason(err)
	}
	logging.Emit(ctx, e.logger, level, logging.Record{Event: event, AgentID: agentID, WakeID: wakeID, Model: EmbeddingModel, Operation: "embedding", Outcome: outcome, Reason: reason, Count: synced, DurationMS: e.now().Sub(started).Milliseconds()})
	return synced, err
}

func (e *Embedder) syncPending(ctx context.Context, agentID string, wakeID int64) (int, error) {
	synced := 0
	for {
		pending, err := e.store.PendingEmbeddings(ctx, agentID, embeddingBatch)
		if err != nil {
			return synced, failure("listing pending embeddings", reasonEmbeddingAccount, err)
		}
		if len(pending) == 0 {
			return synced, nil
		}
		hashes := make([]string, len(pending))
		for i, p := range pending {
			hashes[i] = store.TextHash(p.Text)
		}
		known, err := e.store.VectorsByHash(ctx, hashes)
		if err != nil {
			return synced, failure("reading stored embeddings", reasonEmbeddingAccount, err)
		}
		var texts []string
		for i, p := range pending {
			if _, ok := known[hashes[i]]; !ok {
				known[hashes[i]] = nil
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 {
			vectors, err := e.Embed(ctx, agentID, wakeID, texts)
			if err != nil {
				return synced, err
			}
			for i, text := range texts {
				known[store.TextHash(text)] = vectors[i]
			}
		}
		rows := make([]store.EmbeddingRow, len(pending))
		for i, p := range pending {
			rows[i] = store.EmbeddingRow{Source: p.Source, ID: p.ID, Text: p.Text, TextHash: hashes[i], Vector: known[hashes[i]]}
		}
		if err := e.store.SaveEmbeddings(ctx, rows); err != nil {
			return synced, failure("saving embeddings", reasonEmbeddingAccount, err)
		}
		synced += len(rows)
	}
}
