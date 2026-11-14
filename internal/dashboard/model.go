package dashboard

import "time"

// Page is the canonical public view model for the single dashboard route.
// It carries operator metadata, bounded rows, numeric aggregates and fixed
// reason codes only. No post text, notes, prompts, tool payloads, credentials
// or foreign usernames are projected here.
type Page struct {
	Agent      AgentInfo
	Agents     []AgentLink
	Range      int
	RangeLabel string
	Canonical  string
	OGImage    string
	Ranges     []RangeLink
	Orders     []FilterLink
	// MoreURL loads one step more of the list; empty when it is all shown.
	MoreURL   string
	MoreCount int
	KPIs      []KPI
	ToolCalls ToolPanel
	Wakes     WakesPanel
	Trending  TrendingPanel
	Burn      BurnPanel
	Growth    *GrowthPanel
	Return    ReturnPanel
	Followers FollowersPanel
	Blocked   BlockedPanel
	Lessons   LessonsPanel
}

type AgentInfo struct {
	ID           string
	Status       string
	DryRun       bool
	NextWake     string
	LastWake     string
	LastWakeCode string
}

type AgentLink struct {
	ID      string
	URL     string
	Current bool
}

// FilterLink is one choice in the posted-list filter or order switch.
type FilterLink struct {
	Label   string
	URL     string
	Current bool
}

type RangeLink struct {
	Label   string
	Value   int
	URL     string
	Current bool
}

type KPI struct {
	Label string
	Value string
	Note  string
	Tone  string // up, down or empty: the direction the note reports
	Bad   bool
}

type ToolPanel struct {
	Rows       []ToolStat
	RangeLabel string
}

type ToolStat struct {
	Name    string
	OK      int
	Failed  int
	Blocked int
	Other   int
	Total   int
}

type WakesPanel struct {
	Rows       []WakeRow
	Older      []WakeRow
	RangeLabel string
	Count      int
}

type WakeRow struct {
	ID          int64
	When        string
	WhenTitle   string
	At, EndAt   time.Time // UTC moments the browser reformats
	Kind        string
	Steps       int
	Cost        string
	EndReason   string
	EndCode     string
	Bad         bool
	InProgress  bool
	ToolSummary []WakeTool
	Blocked     []CodeCount
}

type CodeCount struct {
	Code  string
	Raw   string
	Count int
}

// WakeTool is one tool that failed or was blocked during a run.
type WakeTool struct {
	Name    string
	Failed  int
	Blocked int
	Why     string
}

type BurnPanel struct {
	HasRows     bool
	MultiDay    bool
	Chart       *Chart
	BarPct      float64
	PercentText string
	PhaseText   string
	BudgetText  string
	RangeLabel  string
}

type GrowthPanel struct {
	HasMetrics   bool
	EmptyText    string
	Views        int64
	Likes        int64
	Replies      int64
	Reposts      int64
	Rate         string
	ViewsDelta   string
	LikesDelta   string
	RepliesDelta string
	RepostsDelta string
	RateDelta    string
	ViewsChart   *Chart
	RateChart    *Chart
	Posts        []PostRow
	Shown        int
	Total        int
	Top          []PostRow
	LastMeasured string
	MeasuredAt   time.Time
	Unmeasured   int
	RangeLabel   string
}

type TrendingPanel struct {
	Rows       []TrendRow
	Checks     int
	More       bool
	RangeLabel string
}

type PostRow struct {
	// views and rate are the unformatted sort keys; unexported, so they never
	// reach the JSON projection.
	views int64
	rate  float64

	ID        int64
	Link      string
	URL       string
	Kind      string
	Age       string
	AgeTitle  string
	PostedAt  time.Time
	Views     string
	Likes     string
	Replies   string
	Reposts   string
	Rate      string
	Mark      string
	HasSample bool
	// Label is Deleted for a post gone from Threads; empty otherwise.
	Label string
	// Band is top, mid or bottom among the agent's recent actions of its kind;
	// empty while the action is unranked. BandTitle says how it ranked.
	Band      string
	BandTitle string
}

// LessonsPanel is what the agent has learned: the active lessons and the
// latest retired ones.
type LessonsPanel struct {
	Active  []LessonRow
	Retired []LessonRow
}

type LessonRow struct {
	ID       int64
	Kind     string
	Polarity string
	Text     string
	Use      string
	// Cited are the agent's own actions the lesson rests on.
	Cited []LessonCite
	// RetiredBy names who retired it; RetiredAt is a UTC moment the browser
	// reformats, RetiredOn the server text shown without it.
	RetiredBy string
	Reason    string
	RetiredAt time.Time
	RetiredOn string
}

type LessonCite struct {
	Label string
	URL   string
}

// FollowersPanel is the account's follower count over time and what each post
// and each 1,000 views brought.
type FollowersPanel struct {
	HasData bool
	Now     string
	Gained  string
	PerPost string
	Per1000 string
	Chart   *Chart

	gain    int64
	hasGain bool
}

// BlockedPanel counts the posts and replies the guard refused, by reason.
type BlockedPanel struct {
	Total int
	Rows  []CodeCount
}

type TrendRow struct {
	Title      string
	Source     string
	SourceName string
	Seen       string
	Pct        float64
	Rank       int
}

type ReturnPanel struct {
	Empty         bool
	CostPer1000   string
	CostPerPost   string
	CostPerFollow string
	SpentTotal    string
}

// Chart is a server-rendered inline SVG view. Geometry is computed in Go;
// labels are HTML so they keep a readable size at every viewport width.
type Chart struct {
	ID      string
	Title   string
	YMax    string
	YMid    string
	YZero   string
	Path    string
	Rects   []ChartRect
	BudgetY float64
	Budget  string
	Height  string
	XLabels []ChartLabel
	Head    []string
	Rows    [][]string
	HasData bool
}

type ChartRect struct {
	X, Y, W, H float64
	Class      string
}

type ChartLabel struct {
	Text  string
	Class string
}
