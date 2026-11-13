package eval

import (
	"log/slog"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/llm"
	"github.com/canhta/threadz/internal/store"
)

// offlineEmbedder is the deterministic embedder every eval dispatcher uses. A
// construction failure yields nil, which NewDispatcher rejects.
func offlineEmbedder(st *store.Store, tmpl config.Template, now func() time.Time, logger *slog.Logger) *llm.Embedder {
	e, _ := llm.NewOfflineEmbedder(tmpl.Limits, st, llm.Options{Now: now, Logger: logger})
	return e
}
