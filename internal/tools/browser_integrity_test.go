package tools_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/canhta/threadz/internal/config"
	"github.com/canhta/threadz/internal/guard"
	"github.com/canhta/threadz/internal/threads"
	"github.com/canhta/threadz/internal/tools"
)

func confirmingMemory(confirmed string) *threads.Memory {
	m := threads.NewMemory()
	m.Results[threads.OpPost], m.Results[threads.OpReply] = confirmed, confirmed
	return m
}

func submits(m *threads.Memory) int { return m.Count(threads.OpPost) + m.Count(threads.OpReply) }

func TestBrowserConfirmationFailureRetainsIntentAndStopsAllFurtherEffects(t *testing.T) {
	for _, mode := range []string{"missing_ack", "private_ack", "persistence_failure"} {
		t.Run(mode, func(t *testing.T) {
			st, opts, _ := setup(t)
			backend := confirmingMemory("https://fixture.invalid/confirmed")
			opts.Threads = backend
			opts.AllowLive = true
			opts.Template.DryRun = false
			opts.Template.LLM.APIKey = config.NewSecret("fixture-private-secret")
			switch mode {
			case "missing_ack":
				backend.Results[threads.OpPost] = ""
			case "private_ack":
				backend.Results[threads.OpPost] = "https://fixture.invalid/fixture-private-secret"
			case "persistence_failure":
				if _, err := st.DB().Exec(`CREATE TRIGGER fail_confirmation BEFORE UPDATE OF thread_url ON actions BEGIN SELECT RAISE(FAIL,'private-storage-error'); END`); err != nil {
					t.Fatal(err)
				}
			}
			opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now, AllowLive: true}
			d, err := tools.NewDispatcher(opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"post", "read_feed"} {
				args := `{}`
				if name == "post" {
					args = `{"text":"Synthetic outgoing action"}`
				}
				_, err = d.Dispatch(context.Background(), call(name, args))
				if !errors.Is(err, threads.ErrUnknownOutcome) || strings.Contains(err.Error(), "private-") {
					t.Fatalf("unsafe confirmation failure: %v", err)
				}
			}
			var actions, confirmed int
			if err = st.DB().QueryRow(`SELECT COUNT(*),COALESCE(SUM(thread_url!=''),0) FROM actions`).Scan(&actions, &confirmed); err != nil || actions != 1 || confirmed != 0 || submits(backend) != 1 {
				t.Fatalf("actions=%d confirmed=%d submits=%d err=%v", actions, confirmed, submits(backend), err)
			}
		})
	}
}

func TestConfirmedReplyURLCannotBypassOriginalThreadQuota(t *testing.T) {
	st, opts, _ := setup(t)
	now := opts.Now()
	opts.Now = func() time.Time { return now }
	backend := confirmingMemory("https://fixture.invalid/new-reply")
	opts.Threads = backend
	opts.AllowLive = true
	opts.Template.DryRun = false
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now, AllowLive: true}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	args := `{"text":"Synthetic reply","target":"` + readRef(t, d.Guard(), "https://threads.com/@fixture_author/post/original", "tid") + `"}`
	if _, err = d.Dispatch(context.Background(), call("reply", args)); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if _, err = d.Dispatch(context.Background(), call("reply", args)); !errors.Is(err, guard.ErrBlocked) || submits(backend) != 1 {
		t.Fatalf("confirmed reply weakened thread quota: %v submits=%d", err, submits(backend))
	}
}

func TestBrowserStopIngestionAndEscapedCredentialRedaction(t *testing.T) {
	st, opts, _ := setup(t)
	secret := "synthetic\nprivate\"credential"
	opts.Redactor = config.NewRedactor(config.NewSecret(secret))
	backend := threads.NewMemory()
	backend.Pages[threads.OpReadPost] = threads.Page{Kind: "post", Posts: []threads.Post{
		{ID: "one", Username: "ordinary_author", Text: "Please stop " + secret, URL: "https://fixture.invalid/1", AddressedToAgent: true},
		{ID: "two", Username: "unaddressed_author", Text: "Please stop", URL: "https://fixture.invalid/2", IsReply: true},
		{ID: "three", Username: "addressed_author", Text: "Please stop", URL: "https://fixture.invalid/3", IsReply: true, AddressedToAgent: true},
	}}
	opts.Threads = backend
	opts.Scope = guard.Scope{Store: st, Template: opts.Template, WakeID: 1, Now: opts.Now}
	d, err := tools.NewDispatcher(opts)
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Dispatch(context.Background(), call("read_post", `{"id":"fixture-post"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "[REDACTED]") || strings.Contains(out, "credential") || !strings.Contains(out, "Please stop") {
		t.Fatalf("unsafe output %s", out)
	}
	var contacts int
	var username string
	if err = st.DB().QueryRow(`SELECT COUNT(*),COALESCE(MIN(username),'') FROM contacts WHERE do_not_contact=1`).Scan(&contacts, &username); err != nil || contacts != 1 || username != "addressed_author" {
		t.Fatalf("contacts=%d username=%s err=%v", contacts, username, err)
	}
	var leaked int
	if err = st.DB().QueryRow(`SELECT COUNT(*) FROM tool_calls WHERE args_json LIKE '%Please stop%' OR result_json LIKE '%credential%'`).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("inbound body persisted")
	}
}

func TestRedactionPreservesReservedActionIDPrecision(t *testing.T) {
	st, _, d := setup(t)
	if _, err := st.DB().Exec(`INSERT INTO sqlite_sequence(name,seq) VALUES('actions',9007199254740992)`); err != nil {
		t.Fatal(err)
	}
	out, err := d.Dispatch(context.Background(), call("post", `{"text":"Synthetic exact intent ID"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `"action_id":9007199254740993`) {
		t.Fatalf("action identity rounded during redaction: %s", out)
	}
}
