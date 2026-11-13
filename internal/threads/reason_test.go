package threads

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

func TestReasonCoversEverySentinel(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{ErrUnknownOutcome, "unknown_outcome"},
		{ErrLoggedOut, "logged_out"},
		{ErrCaptcha, "captcha"},
		{ErrUnknownPage, "unknown_page"},
		{ErrMalformedPage, "malformed_page"},
		{ErrMetricsUnavailable, "metrics_unavailable"},
		{ErrMediaNotFound, "media_not_found"},
		{ErrWriteRefused, "write_refused"},
		{ErrQuotaExhausted, "quota_exhausted"},
		{ErrAPI, "api_error"},
		{errors.Join(ErrUnknownOutcome, ErrAPI), "unknown_outcome"},
		{ErrCloseFailed, "close_failed"},
		{context.Canceled, "cancelled"},
		{context.DeadlineExceeded, "timeout"},
		{fmt.Errorf("wrapped: %w", ErrCaptcha), "captcha"},
		{errors.Join(ErrUnknownOutcome, ErrCaptcha), "unknown_outcome"},
		{errors.Join(ErrUnknownOutcome, context.Canceled), "unknown_outcome"},
		{errors.New("other"), ""},
		{nil, ""},
	} {
		if got := Reason(tc.err); got != tc.want {
			t.Errorf("Reason(%v) = %q, want %q", tc.err, got, tc.want)
		}
	}
	if len(reasons) != 13 {
		t.Fatalf("table has %d sentinels; add a row above for the new one", len(reasons))
	}
}
