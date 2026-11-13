package threads

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/canhta/threadz/internal/agentbrowser"
)

const taggedTarget = "https://www.threads.com/@synthetic_author/post/fixtureTaggedPost"

// A post with a topic tag nests its permalink; a reply to it still finds the
// composer, types, clicks once and waits for the acknowledgement.
func TestAgentBrowserReplyOnTopicTaggedPost(t *testing.T) {
	full := liveFixture(t, "post-tagged.ax.txt")
	filled := strings.Replace(full, `[ref=e51]: `, `[ref=e51]: Synthetic reply.`, 1)
	filled = strings.Replace(filled, `button "Expand composer" [ref=e42]`, `button "Expand composer" [ref=e42]`+"\n"+`            - button "Reply" [ref=e590]`, 1)
	cli := &fakeAgentCLI{openURL: taggedTarget, snapshots: []agentbrowser.Snapshot{
		{Origin: taggedTarget, Text: full}, {Origin: taggedTarget, Text: full}, {Origin: taggedTarget, Text: filled}, {Origin: taggedTarget, Text: filled},
	}}
	_, err := testAgentBrowser(t, cli, nil).Reply(context.Background(), taggedTarget, "Synthetic reply.")
	if errors.Is(err, ErrMalformedPage) || cli.clicks != 1 || len(cli.fills) != 1 {
		t.Fatalf("tagged post refused: err=%v clicks=%d fills=%v", err, cli.clicks, cli.fills)
	}
	// The fixture has no acknowledgement, so the submit ends as an unknown outcome at its last step.
	if se := StageOf(err); !errors.Is(err, ErrUnknownOutcome) || se == nil || se.Stage != StageAcknowledgement || se.Snapshot != filled {
		t.Fatalf("stage error %+v for %v", se, err)
	}
}

func TestAgentBrowserReplyWithoutComposerNamesTheStageAndKeepsTheSnapshot(t *testing.T) {
	full := liveFixture(t, "post-tagged.ax.txt")
	start, end := strings.Index(full, `textbox "Empty text field`), strings.Index(full, `button "Expand composer"`)
	if start < 0 || end < start {
		t.Fatal("composer lines not found in fixture")
	}
	bare := full[:start] + full[end:]
	cli := &fakeAgentCLI{openURL: taggedTarget, snapshot: agentbrowser.Snapshot{Origin: taggedTarget, Text: bare}}
	b := testAgentBrowser(t, cli, nil)
	b.driver.(*agentBrowserDriver).renderWait = 0
	_, err := b.Reply(context.Background(), taggedTarget, "Synthetic reply.")
	se := StageOf(err)
	if !errors.Is(err, ErrMalformedPage) || Reason(err) != "malformed_page" || se == nil || se.Stage != StageComposerMissing || se.Snapshot != bare || se.Origin != taggedTarget {
		t.Fatalf("stage error %+v for %v", se, err)
	}
	if strings.Contains(err.Error(), "textbox") || cli.clicks != 0 {
		t.Fatalf("the snapshot reached the message or a click was sent: %v", err)
	}
}

func TestBrowserFailuresNameTheirStage(t *testing.T) {
	for name, tc := range map[string]struct {
		f      *fakeDriver
		stage  string
		reason string
	}{
		"read_page":       {&fakeDriver{pageErr: ErrUnknownPage}, StageReadPage, "unknown_page"},
		"composer":        {&fakeDriver{read: Page{Kind: "post"}}, StageComposerMissing, "malformed_page"},
		"driver_named":    {&fakeDriver{read: Page{Kind: "post", Form: replyForm}, submitErr: errors.Join(ErrWriteRefused, &StageError{Stage: StageType, Err: ErrMalformedPage})}, StageType, "write_refused"},
		"submit":          {&fakeDriver{read: Page{Kind: "post", Form: replyForm}, submitErr: errors.New("click failed")}, StageSubmit, "unknown_outcome"},
		"acknowledgement": {&fakeDriver{read: Page{Kind: "post", Form: replyForm}, ackErr: context.DeadlineExceeded}, StageAcknowledgement, "unknown_outcome"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := testBrowser(tc.f).Reply(context.Background(), "/@a/post/P1", "text")
			if se := StageOf(err); se == nil || se.Stage != tc.stage || Reason(err) != tc.reason {
				t.Fatalf("stage %+v reason %q for %v", se, Reason(err), err)
			}
		})
	}
	_, err := testBrowser(&fakeDriver{pageErr: ErrMalformedPage}).ReadFeed(context.Background())
	if se := StageOf(err); se == nil || se.Stage != StageReadPage {
		t.Fatalf("read stage %+v for %v", se, err)
	}
}
