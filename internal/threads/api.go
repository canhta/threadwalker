package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/canhta/threadz/internal/config"
)

// APIBaseURL is the Threads Graph API origin and version.
const APIBaseURL = "https://graph.threads.net/v1.0"

// ErrAPI means the Threads API answered with an error status or body.
var ErrAPI = errors.New("threads api error")

// ErrQuotaExhausted means the account's documented publishing quota is used up.
var ErrQuotaExhausted = errors.New("publishing quota exhausted")

// TokenSource yields the long-lived access token for one call.
type TokenSource interface {
	Token(context.Context) (config.Secret, error)
}

// APIOptions configures one API client. Wait must be injected so tests never
// sleep; production passes clock.Sleep.
type APIOptions struct {
	BaseURL string
	UserID  string
	Tokens  TokenSource
	HTTP    *http.Client
	Timeout time.Duration
	Wait    func(context.Context, time.Duration) error
	Now     func() time.Time
}

// API is the official Threads API client for the agent's own account.
type API struct {
	o APIOptions
}

// Publishing statuses of a media container.
const (
	statusFinished = "FINISHED"
	statusError    = "ERROR"
	statusExpired  = "EXPIRED"
)

const (
	statusPolls        = 5
	statusPollInterval = 2 * time.Second
	maxResponseBytes   = 4 << 20
	postFields         = "id,permalink,text,username,timestamp,is_reply"
	postMetrics        = "views,likes,replies,reposts,quotes,shares"
	accountMetrics     = "views,likes,replies,reposts,quotes"
)

// NewAPI returns a client; BaseURL defaults to APIBaseURL.
func NewAPI(o APIOptions) (*API, error) {
	if o.UserID == "" || o.Tokens == nil || o.Wait == nil {
		return nil, errors.New("configuring threads api: user id, token source and wait required")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.BaseURL == "" {
		o.BaseURL = APIBaseURL
	}
	if o.HTTP == nil {
		o.HTTP = &http.Client{}
	}
	if o.Timeout <= 0 {
		o.Timeout = operationTimeout
	}
	return &API{o: o}, nil
}

// get and post return the decoded body. The token travels in a header, never
// in a URL, so request URLs in errors and logs stay clean.
func (a *API) do(ctx context.Context, method, path string, q url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, a.o.Timeout)
	defer cancel()
	token, err := a.o.Tokens.Token(ctx)
	if err != nil {
		return fmt.Errorf("reading threads token: %w", err)
	}
	u := a.o.BaseURL + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return fmt.Errorf("building threads request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.Expose())
	resp, err := a.o.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("threads request failed")
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("reading threads response failed")
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Code int `json:"code"`
			} `json:"error"`
		}
		_ = json.Unmarshal(body, &e)
		return fmt.Errorf("%w: status %d code %d", ErrAPI, resp.StatusCode, e.Error.Code)
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: malformed response", ErrAPI)
	}
	return nil
}

type apiPost struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Username  string `json:"username"`
	Permalink string `json:"permalink"`
	IsReply   bool   `json:"is_reply"`
	Timestamp string `json:"timestamp"`
}

func (p apiPost) post() Post {
	return Post{ID: p.ID, Username: p.Username, Text: p.Text, URL: p.Permalink, Timestamp: p.Timestamp, IsReply: p.IsReply}
}

func posts(kind string, in []apiPost) Page {
	page := Page{Kind: kind, Posts: make([]Post, 0, len(in))}
	for _, p := range in {
		page.Posts = append(page.Posts, p.post())
	}
	return page
}

func (a *API) list(ctx context.Context, kind, path string, q url.Values) (Page, error) {
	var out struct {
		Data []apiPost `json:"data"`
	}
	if err := a.do(ctx, http.MethodGet, path, q, &out); err != nil {
		return Page{}, err
	}
	return posts(kind, out.Data), nil
}

// RecentWindow is how far back a recent search looks.
const RecentWindow = time.Hour

// Search is the keyword search (`threads_keyword_search`). A recent search is
// `search_type=RECENT` limited to the last RecentWindow.
func (a *API) Search(ctx context.Context, query string, recent bool) (Page, error) {
	q := url.Values{"q": {query}, "search_type": {"TOP"}, "fields": {postFields}}
	if recent {
		q.Set("search_type", "RECENT")
		q.Set("since", strconv.FormatInt(a.o.Now().Add(-RecentWindow).Unix(), 10))
	}
	page, err := a.list(ctx, "search", "/keyword_search", q)
	if err == nil && len(page.Posts) == 0 {
		page.LimitedAccess = "No results. Without Meta App Review for threads_keyword_search, search covers only your own posts. Use fetch_trends, web_search, read_feed and profile_posts to find what to post on or reply to."
	}
	return page, err
}

// ReadOwnPosts lists the account's own recent threads (`GET /{user}/threads`).
func (a *API) ReadOwnPosts(ctx context.Context) (Page, error) {
	return a.list(ctx, "own_posts", "/"+a.o.UserID+"/threads", url.Values{"fields": {postFields}, "limit": {"100"}})
}

// ProfilePosts reads `GET /profile_posts` (`threads_profile_discovery`): the
// recent public posts of a username, with the numeric ids a reply needs.
// Standard access serves only a few official Meta accounts, and the API
// refuses the rest; a refusal returns a limited_access page, not an error.
func (a *API) ProfilePosts(ctx context.Context, username string) (Page, error) {
	page, err := a.list(ctx, "profile_posts", "/profile_posts", url.Values{"username": {username}, "fields": {postFields}, "limit": {"10"}})
	if err != nil && errors.Is(err, ErrAPI) && (strings.Contains(err.Error(), "status 400") || strings.Contains(err.Error(), "status 403")) {
		return Page{Kind: "profile_posts", LimitedAccess: "The API refused this profile: profile_posts needs Meta App Review for threads_profile_discovery, and only public profiles with at least 100 followers are served."}, nil
	}
	return page, err
}

// ProfileLookup reads `GET /profile_lookup` (`threads_profile_discovery`).
func (a *API) ProfileLookup(ctx context.Context, username string) (Profile, error) {
	var out struct {
		Username  string `json:"username"`
		Followers *int64 `json:"follower_count"`
		Views     *int64 `json:"views_count"`
		Verified  bool   `json:"is_verified"`
	}
	if err := a.do(ctx, http.MethodGet, "/profile_lookup", url.Values{"username": {username}}, &out); err != nil {
		return Profile{}, err
	}
	return Profile{Username: out.Username, Followers: out.Followers, Views7d: out.Views, Verified: out.Verified}, nil
}

// ReadReplies returns the direct replies to one of the agent's posts, or the
// mentions of the agent when id is empty. Both address the agent.
func (a *API) ReadReplies(ctx context.Context, id string) (Page, error) {
	kind, path := "mentions", "/"+a.o.UserID+"/mentions"
	if id != "" {
		kind, path = "replies", "/"+url.PathEscape(id)+"/replies"
	}
	page, err := a.list(ctx, kind, path, url.Values{"fields": {postFields}})
	for i := range page.Posts {
		page.Posts[i].AddressedToAgent = true
	}
	return page, err
}

// OwnReplies lists the account's own recent replies, newest first.
func (a *API) OwnReplies(ctx context.Context) (Page, error) {
	return a.list(ctx, "own_replies", "/me/replies", url.Values{"fields": {postFields}, "limit": {"25"}})
}

// ReadPost reads one post by its media id.
func (a *API) ReadPost(ctx context.Context, id string) (Page, error) {
	var p apiPost
	if err := a.do(ctx, http.MethodGet, "/"+url.PathEscape(id), url.Values{"fields": {postFields}}, &p); err != nil {
		return Page{}, err
	}
	return posts("post", []apiPost{p}), nil
}

type insights struct {
	Data []struct {
		Name       string `json:"name"`
		TotalValue *struct {
			Value int64 `json:"value"`
		} `json:"total_value"`
		Values []struct {
			Value int64 `json:"value"`
		} `json:"values"`
	} `json:"data"`
}

func (i insights) has(name string) bool {
	for _, d := range i.Data {
		if d.Name == name {
			return true
		}
	}
	return false
}

func (i insights) metrics() (Metrics, error) {
	var m Metrics
	for _, d := range i.Data {
		// Measured live: account views is a per-day series while the other
		// account metrics carry total_value; a post metric has one lifetime value.
		var v int64
		switch {
		case d.TotalValue != nil:
			v = d.TotalValue.Value
		case len(d.Values) > 0:
			for _, p := range d.Values {
				v += p.Value
			}
		default:
			return Metrics{}, ErrMetricsUnavailable
		}
		if v < 0 || v > snapshotMaxMetric {
			return Metrics{}, ErrMetricsUnavailable
		}
		switch d.Name {
		case "views":
			m.Views = v
		case "likes":
			m.Likes = v
		case "replies":
			m.Replies = v
		case "reposts":
			m.Reposts = v
		case "quotes":
			m.Quotes = &v
		case "shares":
			m.Shares = &v
		case "followers_count":
			m.Followers = &v
		}
	}
	return m, nil
}

// ReadActionMetrics returns the per-post insights of the own post with media
// id. A response without views, likes, replies or reposts is unavailable.
func (a *API) ReadActionMetrics(ctx context.Context, id string) (Metrics, error) {
	var in insights
	if err := a.do(ctx, http.MethodGet, "/"+url.PathEscape(id)+"/insights", url.Values{"metric": {postMetrics}}, &in); err != nil {
		if errors.Is(err, ErrAPI) && strings.Contains(err.Error(), "status 400 code 100") {
			return Metrics{}, ErrMediaNotFound
		}
		return Metrics{}, err
	}
	for _, name := range []string{"views", "likes", "replies", "reposts"} {
		if !in.has(name) {
			return Metrics{}, ErrMetricsUnavailable
		}
	}
	return in.metrics()
}

// ReadOwnMetrics returns the account insights and the follower count.
func (a *API) ReadOwnMetrics(ctx context.Context) (Metrics, error) {
	var acct, fol insights
	path := "/" + a.o.UserID + "/threads_insights"
	if err := a.do(ctx, http.MethodGet, path, url.Values{"metric": {accountMetrics}}, &acct); err != nil {
		return Metrics{}, err
	}
	if err := a.do(ctx, http.MethodGet, path, url.Values{"metric": {"followers_count"}}, &fol); err != nil {
		return Metrics{}, err
	}
	m, err := acct.metrics()
	if err != nil {
		return Metrics{}, err
	}
	f, err := fol.metrics()
	m.Followers = f.Followers
	return m, err
}

// Quota is the documented publishing limit and its use over its window.
type Quota struct {
	PostsUsed, PostsTotal     int64
	RepliesUsed, RepliesTotal int64
}

// PublishingLimit reads `threads_publishing_limit`.
func (a *API) PublishingLimit(ctx context.Context) (Quota, error) {
	var out struct {
		Data []struct {
			QuotaUsage int64 `json:"quota_usage"`
			Config     struct {
				Total int64 `json:"quota_total"`
			} `json:"config"`
			ReplyUsage  int64 `json:"reply_quota_usage"`
			ReplyConfig struct {
				Total int64 `json:"quota_total"`
			} `json:"reply_config"`
		} `json:"data"`
	}
	q := url.Values{"fields": {"quota_usage,config,reply_quota_usage,reply_config"}}
	if err := a.do(ctx, http.MethodGet, "/"+a.o.UserID+"/threads_publishing_limit", q, &out); err != nil {
		return Quota{}, err
	}
	if len(out.Data) != 1 {
		return Quota{}, fmt.Errorf("%w: malformed response", ErrAPI)
	}
	d := out.Data[0]
	return Quota{d.QuotaUsage, d.Config.Total, d.ReplyUsage, d.ReplyConfig.Total}, nil
}

// Post publishes a text post.
func (a *API) Post(ctx context.Context, text string) (Published, error) {
	return a.publish(ctx, "post", url.Values{"media_type": {"TEXT"}, "text": {text}})
}

// Reply publishes a reply to the media id replyTo.
func (a *API) Reply(ctx context.Context, replyTo, text string) (Published, error) {
	return a.publish(ctx, "reply", url.Values{"media_type": {"TEXT"}, "text": {text}, "reply_to_id": {replyTo}})
}

type idBody struct {
	ID string `json:"id"`
}

// publish creates a container, waits until it is FINISHED, publishes it and
// returns the permalink and media id. Everything before the publish call has no public
// effect and fails plainly; once the publish call is sent, any failure is an
// unknown outcome and is never retried.
func (a *API) publish(ctx context.Context, op string, q url.Values) (pub Published, err error) {
	defer func() { err = operationError(op, err) }()
	var c idBody
	if err = a.do(ctx, http.MethodPost, "/"+a.o.UserID+"/threads", q, &c); err != nil {
		return Published{}, err
	}
	if c.ID == "" {
		return Published{}, fmt.Errorf("%w: malformed response", ErrAPI)
	}
	if err = a.awaitContainer(ctx, c.ID); err != nil {
		return Published{}, err
	}
	var m idBody
	if err = a.do(ctx, http.MethodPost, "/"+a.o.UserID+"/threads_publish", url.Values{"creation_id": {c.ID}}, &m); err != nil {
		return Published{}, errors.Join(ErrUnknownOutcome, err)
	}
	if m.ID == "" {
		return Published{}, errors.Join(ErrUnknownOutcome, ErrAPI)
	}
	var p apiPost
	if err = a.do(ctx, http.MethodGet, "/"+url.PathEscape(m.ID), url.Values{"fields": {"id,permalink"}}, &p); err != nil || p.Permalink == "" {
		return Published{}, errors.Join(ErrUnknownOutcome, err)
	}
	return Published{URL: p.Permalink, ID: m.ID}, nil
}

func (a *API) awaitContainer(ctx context.Context, id string) error {
	for i := 0; i < statusPolls; i++ {
		var s struct {
			Status string `json:"status"`
		}
		if err := a.do(ctx, http.MethodGet, "/"+url.PathEscape(id), url.Values{"fields": {"status"}}, &s); err != nil {
			return err
		}
		switch strings.ToUpper(s.Status) {
		case statusFinished:
			return nil
		case statusError, statusExpired:
			return fmt.Errorf("%w: container %s", ErrWriteRefused, strings.ToLower(s.Status))
		}
		if err := a.o.Wait(ctx, statusPollInterval); err != nil {
			return err
		}
	}
	return fmt.Errorf("%w: container not finished", ErrWriteRefused)
}
