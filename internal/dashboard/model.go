package dashboard

// Page is the canonical public view model for the single dashboard route.
// It carries operator metadata, bounded rows, numeric aggregates and fixed
// reason codes only. No post text, notes, prompts, tool payloads, credentials
// or foreign usernames are projected here except in opt-in content mode, which
// adds the agent's own bounded text.
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
	Learned   LearnedPanel
	Return    ReturnPanel
	Followers FollowersPanel
	Outcomes  OutcomesPanel
	Blocked   BlockedPanel
	Content   bool
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
	Views     string
	Likes     string
	Replies   string
	Reposts   string
	Rate      string
	Mark      string
	HasSample bool
	// Label is viral, flop or normal; empty until the outcome model scored the post.
	Label string
	// Why is set only in content mode.
	Why []string
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

// OutcomesPanel counts the posts the outcome model called viral or flop.
type OutcomesPanel struct {
	HasData     bool
	Scored      int
	Viral, Flop int
}

// BlockedPanel counts the posts and replies the guard refused, by reason.
type BlockedPanel struct {
	Total int
	Rows  []CodeCount
}

type LearnedPanel struct {
	HasRows         bool
	Testing         int
	Established     int
	Rejected        int
	Samples         int64
	Notes           int
	Posteriors      []PosteriorRow
	Content         bool
	Hypotheses      []HypothesisRow
	OlderHypotheses []HypothesisRow
	NotesRows       []NoteRow
	OlderNotes      []NoteRow
	Trends          []TrendRow
	MemoryIndex     string
	MemoryJournal   string
	Strategy        []StrategyRow
	// Penalties are the curated knowledge entries that collide with a Meta
	// demotion; Candidates are techniques the agent found while studying.
	Penalties  []PenaltyRow
	Candidates []CandidateRow
}

// StrategyRow is one strategy revision with its line diff against the one before.
type StrategyRow struct {
	Version   int64
	When      string
	Citations string
	Diff      []DiffLine
}

// DiffLine is one line of a revision diff; Mark is "+", "-" or " ".
type DiffLine struct {
	Mark string
	Text string
}

type PenaltyRow struct {
	Risk   string
	Claim  string
	Source string
}

type CandidateRow struct {
	Text   string
	Source string
	When   string
}

// Tried reads "2 times".
func (h HypothesisRow) Tried() string { return countText(int(h.N), "time", "times") }

type HypothesisRow struct {
	ID        int64
	Status    string
	Text      string
	N         int64
	ChangedAt string
	Evidence  string
	Outcomes  int64
}

type NoteRow struct {
	Text string
	When string
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

// PosteriorRow is a testing idea with how it stands so far.
type PosteriorRow struct {
	Text string
	N    int64
	// Verdict is ahead, behind or unclear.
	Verdict string
}

// Standing reads "Tried 3 times, ahead so far".
func (r PosteriorRow) Standing() string {
	return "Tried " + countText(int(r.N), "time", "times") + ", " + r.Verdict + " so far"
}
