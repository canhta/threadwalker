package main

import "time"

// These types are the wire and --json shape of the operator commands.

type wakeSummary struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at,omitzero"`
	Steps     int       `json:"steps"`
	EndReason string    `json:"end_reason"`
	CostUSD   float64   `json:"cost_usd"`
}

type agentStatus struct {
	Agent             string         `json:"agent"`
	State             string         `json:"state"`
	DryRun            bool           `json:"dry_run"`
	NextWake          time.Time      `json:"next_wake,omitzero"`
	NextKind          string         `json:"next_kind,omitempty"`
	SpentTodayUSD     float64        `json:"spent_today_usd"`
	DailyCapUSD       float64        `json:"daily_cap_usd"`
	DreamSpentUSD     float64        `json:"dream_spent_today_usd"`
	DreamCapUSD       float64        `json:"dream_cap_usd"`
	LastWake          *wakeSummary   `json:"last_wake,omitempty"`
	PendingMetricJobs int            `json:"pending_metric_jobs"`
	Health            []healthResult `json:"health"`
}

type wakeOutcome struct {
	Agent     string `json:"agent"`
	Phase     string `json:"phase"`
	DryRun    bool   `json:"dry_run"`
	WakeID    int64  `json:"wake_id"`
	EndReason string `json:"end_reason"`
	Steps     int    `json:"steps"`
}

type controlOutcome struct {
	Agent string `json:"agent"`
	State string `json:"state"`
}

type maintenanceOutcome struct {
	Step   string           `json:"step"`
	Agent  string           `json:"agent,omitempty"`
	Count  int              `json:"count"`
	Tables map[string]int64 `json:"tables,omitempty"`
}

type cookiesOutcome struct {
	Agent   string `json:"agent"`
	Message string `json:"message"`
}

type contextMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type contextView struct {
	Agent    string           `json:"agent"`
	Phase    string           `json:"phase"`
	Messages []contextMessage `json:"messages"`
}
