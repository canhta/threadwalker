package threads

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The files in testdata/live are responses of the live API (redacted by
// `ctl capture-api`, or the hand-written stand-ins the directory ships with).
// These tests decode them through the real API code paths.

func liveFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "live", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func fileID(t *testing.T, name string) string {
	t.Helper()
	var v struct{ ID string }
	if err := json.Unmarshal([]byte(liveFile(t, name)), &v); err != nil || v.ID == "" {
		t.Fatalf("%s has no id: %v", name, err)
	}
	return v.ID
}

// liveAPI serves the live files; mutate may alter one file's text first.
func liveAPI(t *testing.T, mutate func(name, body string) string) (*fakeThreads, *API, string, string) {
	t.Helper()
	if mutate == nil {
		mutate = func(_, body string) string { return body }
	}
	body := func(name string) string { return mutate(name, liveFile(t, name)) }
	f, api, _ := newFake(t)
	api.o.UserID = fileID(t, CaptureMe)
	post, reply := fileID(t, CaptureOwnPost), fileID(t, CaptureOwnReply)
	f.route("GET", "/"+post, body(CaptureOwnPost))
	f.route("GET", "/"+post+"/insights", body(CaptureOwnPostMetric))
	f.route("GET", "/"+reply, body(CaptureOwnReply))
	f.route("GET", "/"+reply+"/insights", body(CaptureOwnReplyMetric))
	f.route("GET", "/"+reply+"/replies", body(CaptureOwnReplyReplies))
	f.route("GET", "/"+api.o.UserID+"/mentions", body(CaptureMentions))
	return f, api, post, reply
}

func TestLiveContractDecodesThroughAPI(t *testing.T) {
	ctx := context.Background()
	f, api, post, reply := liveAPI(t, nil)

	page, err := api.ReadPost(ctx, post)
	if err != nil || page.Posts[0].Timestamp.IsZero() || page.Posts[0].IsReply || page.Posts[0].URL == "" {
		t.Fatalf("own post=%+v err=%v", page, err)
	}
	if page, err = api.ReadReplies(ctx, reply); err != nil || page.Skipped != 0 || len(page.Posts) == 0 {
		t.Fatalf("replies=%+v err=%v", page, err)
	}
	for _, p := range page.Posts {
		if p.Timestamp.IsZero() || !p.IsReply || p.ID == "" || p.Username == "" {
			t.Fatalf("reply %+v", p)
		}
	}
	if page, err = api.ReadReplies(ctx, ""); err != nil || page.Skipped != 0 || len(page.Posts) == 0 || page.Posts[0].Timestamp.IsZero() {
		t.Fatalf("mentions=%+v err=%v", page, err)
	}

	// A post keeps the replies number its insights report and never reads /replies.
	m, err := api.ReadActionMetrics(ctx, post)
	if err != nil || m.Views == 0 || f.count("GET /"+post+"/replies") != 0 {
		t.Fatalf("post metrics=%+v err=%v", m, err)
	}
	// Insights report replies=0 for an own reply; the /replies count replaces it.
	var insightReplies int64 = -1
	var ins insights
	if err := json.Unmarshal([]byte(liveFile(t, CaptureOwnReplyMetric)), &ins); err != nil {
		t.Fatal(err)
	}
	for _, d := range ins.Data {
		if d.Name == "replies" {
			insightReplies = d.Values[0].Value
		}
	}
	var direct struct{ Data []json.RawMessage }
	if err := json.Unmarshal([]byte(liveFile(t, CaptureOwnReplyReplies)), &direct); err != nil {
		t.Fatal(err)
	}
	m, err = api.ReadActionMetrics(ctx, reply)
	if insightReplies != 0 || err != nil || m.Replies != int64(len(direct.Data)) || m.Replies == 0 {
		t.Fatalf("own reply: insights replies=%d, metrics=%+v err=%v", insightReplies, m, err)
	}
}

func TestLiveContractRejectsAlteredTimestamp(t *testing.T) {
	ctx := context.Background()
	for _, format := range []string{"2026-01-02T03:04:05Z", "2026-01-02T03:04:05+00:00", "Fri Jan 02 03:04:05 2026", "1767323045"} {
		alter := func(name, body string) string {
			return regexp.MustCompile(`"timestamp": "[^"]*"`).ReplaceAllString(body, `"timestamp": "`+format+`"`)
		}
		_, api, post, reply := liveAPI(t, alter)
		if _, err := api.ReadPost(ctx, post); err == nil {
			t.Errorf("%q: ReadPost accepted an unknown timestamp format", format)
		}
		if page, err := api.ReadReplies(ctx, reply); err != nil || len(page.Posts) != 0 || page.Skipped == 0 {
			t.Errorf("%q: replies=%+v err=%v; want them skipped and counted", format, page, err)
		}
	}
}

var stampShape = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\+0000$`)

func TestLiveContractShapes(t *testing.T) {
	object := func(name string) map[string]any {
		var v map[string]any
		if err := json.Unmarshal([]byte(liveFile(t, name)), &v); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return v
	}
	postShape := func(file string, p map[string]any) {
		for _, k := range []string{"id", "permalink", "text", "username", "timestamp"} {
			if _, ok := p[k].(string); !ok {
				t.Errorf("%s: %s is %T, want string", file, k, p[k])
			}
		}
		if _, ok := p["is_reply"].(bool); !ok {
			t.Errorf("%s: is_reply is %T, want bool", file, p["is_reply"])
		}
		if ts, _ := p["timestamp"].(string); !stampShape.MatchString(ts) {
			t.Errorf("%s: timestamp %q is not the API format", file, ts)
		}
	}
	me := object(CaptureMe)
	if _, ok := me["id"].(string); !ok || me["username"] == "" {
		t.Errorf("me: %v", me)
	}
	for _, f := range []string{CaptureOwnPost, CaptureOwnReply} {
		postShape(f, object(f))
	}
	for _, f := range []string{CaptureOwnReplyReplies, CaptureMentions} {
		list, _ := object(f)["data"].([]any)
		if len(list) == 0 {
			t.Errorf("%s: no data", f)
		}
		for _, p := range list {
			postShape(f, p.(map[string]any))
		}
	}
	for _, f := range []string{CaptureOwnPostMetric, CaptureOwnReplyMetric} {
		for _, d := range object(f)["data"].([]any) {
			m := d.(map[string]any)
			vals, _ := m["values"].([]any)
			if len(vals) != 1 {
				t.Fatalf("%s: %v has %d values, want 1", f, m["name"], len(vals))
			}
			if _, ok := vals[0].(map[string]any)["value"].(float64); !ok || m["name"] == nil || m["period"] != "lifetime" {
				t.Errorf("%s: insight %v is not data[].name/values[0].value", f, m)
			}
		}
	}
	if _, err := os.Stat(filepath.Join("testdata", "live", "STANDIN.txt")); err == nil {
		t.Log("testdata/live holds hand-written stand-ins; run `threadz ctl capture-api` on the host to record real responses")
	}
}

func TestCaptureRedactsEverythingPrivate(t *testing.T) {
	const ownPost, ownReply = "17841400000000001", "17841400000000002"
	f, api, _ := newFake(t)
	post := func(id, user, text string, reply bool) string {
		b, _ := json.Marshal(map[string]any{"id": id, "text": text, "username": user, "is_reply": reply,
			"permalink": "https://www.threads.com/@" + user + "/post/Cabc" + id[:4] + "?igshid=SECRETQUERY", "timestamp": "2026-11-13T15:56:44+0000"})
		return string(b)
	}
	list := func(items ...string) string {
		return `{"data":[` + strings.Join(items, ",") + `],"paging":{"cursors":{"before":"QVFHcursor","after":"QVFHcursor2"},"next":"https://graph.threads.net/v1.0/x?access_token=` + testToken + `"}}`
	}
	insight := `{"data":[{"name":"replies","period":"lifetime","values":[{"value":0}],"title":"Replies","description":"x","id":"` + ownReply + `/insights/replies/lifetime"}]}`
	f.route("GET", "/me", `{"id":"1000000000000001","username":"real_owner","threads_profile_picture_url":"https://scontent.cdn.net/pic.jpg?token=SECRETQUERY"}`)
	f.route("GET", "/1000000000000001/threads", list(post(ownPost, "real_owner", "my private words", false)))
	f.route("GET", "/me/replies", list(post(ownReply, "real_owner", "another private line", true)))
	f.route("GET", "/"+ownPost, post(ownPost, "real_owner", "my private words", false))
	f.route("GET", "/"+ownPost+"/insights", insight)
	f.route("GET", "/"+ownReply, post(ownReply, "real_owner", "another private line", true))
	f.route("GET", "/"+ownReply+"/insights", insight)
	f.route("GET", "/"+ownReply+"/replies", list(post("17841400000000009", "stranger_one", "a stranger says things", true)))
	f.route("GET", "/1000000000000001/mentions", list(post("17841400000000010", "stranger_two", "@real_owner hello there", false)))
	f.route("GET", "/1000000000000001/threads_insights", `{"data":[{"name":"followers_count","period":"day","title":"followers_count","description":"x","total_value":{"value":1},"id":"1000000000000001/insights/followers_count/day"}]}`)

	files, err := api.Capture(context.Background())
	if err != nil || len(files) != 9 {
		t.Fatalf("capture: %d files, err=%v", len(files), err)
	}
	for name, b := range files {
		s := string(b)
		for _, bad := range []string{testToken, "SECRETQUERY", "stranger_", "private", "says things", "hello there", "17841400000000009", "access_token", "QVFH", "?"} {
			if strings.Contains(s, bad) {
				t.Errorf("%s still holds %q:\n%s", name, bad, s)
			}
		}
		if !json.Valid(b) {
			t.Errorf("%s is not JSON", name)
		}
	}
	replies := string(files[CaptureOwnReplyReplies])
	for _, want := range []string{`"username": "user_1"`, `"timestamp": "2026-11-13T15:56:44+0000"`, `"is_reply": true`} {
		if !strings.Contains(replies, want) {
			t.Errorf("replies lost %q:\n%s", want, replies)
		}
	}
	if !strings.Contains(string(files[CaptureOwnPost]), `"username": "real_owner"`) {
		t.Errorf("own username must stay:\n%s", files[CaptureOwnPost])
	}
	// The redacted files still decode through the API; the own reply's id is the same in each file.
	if !strings.Contains(string(files[CaptureOwnReplyMetric]), fileIDOf(t, files[CaptureOwnReply])+"/insights/replies") {
		t.Errorf("synthetic id differs between files:\n%s", files[CaptureOwnReplyMetric])
	}
	if got := placeholder("héllo wörld, this is text"); len([]rune(got)) != 25 || strings.Contains(got, "llo") {
		t.Errorf("placeholder %q", got)
	}
}

func fileIDOf(t *testing.T, b []byte) string {
	t.Helper()
	var v struct{ ID string }
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v.ID
}
