package threads

import (
	"context"
	"errors"
	"testing"
)

func TestActionMetricsRequireExactThreadBinding(t *testing.T) {
	const requested = "https://fixture.test/post/own?reply=one"
	for _, tc := range []struct {
		name, kind, binding string
		want                error
	}{
		{"bound", "action_metrics", "/post/own?reply=one", nil},
		{"other_action", "action_metrics", "/post/other", ErrMalformedPage},
		{"other_reply", "action_metrics", "/post/own?reply=two", ErrMalformedPage},
		{"aggregate", "metrics", "", ErrUnknownPage},
		{"missing", "action_metrics", "", ErrMalformedPage},
		{"foreign_origin", "action_metrics", "https://foreign.test/post/own", ErrMalformedPage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeDriver{raw: []byte(`{"contract":"synthetic-v1","page":"` + tc.kind + `","thread_url":"` + tc.binding + `","metrics":{"views":"123","likes":"7","replies":"3","reposts":"1"}}`)}
			got, err := testBrowser(f).ReadActionMetrics(context.Background(), requested)
			if !errors.Is(err, tc.want) || (tc.want == nil && got != (Metrics{Views: 123, Likes: 7, Replies: 3, Reposts: 1})) {
				t.Fatalf("got=%+v err=%v want=%v", got, err, tc.want)
			}
			if f.submits != 0 {
				t.Fatal("metrics submitted a write")
			}
		})
	}
}
