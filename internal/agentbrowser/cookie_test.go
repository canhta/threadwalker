package agentbrowser

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestSetCookieIsAFixedWriteOnlyCommand(t *testing.T) {
	client, record := testClient(t, fakeProgramFor(map[string]fakeCommand{
		"cookies": {Stdout: success(`{"set":true}`)},
		"close":   {Stdout: success(`{"closed":true}`)},
	}), nil)
	ctx := context.Background()
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ck := Cookie{Name: "sessionid", Value: "synthetic-value", Domain: ".threads.com", Path: "/", Secure: true, HTTPOnly: true, SameSite: "Lax", Expires: 1893456000}
	if err := client.SetCookie(ctx, ck); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Cookie{
		{Name: "a b", Value: "v", Domain: ".threads.com", Path: "/"},
		{Name: "a", Value: "-flag", Domain: ".threads.com", Path: "/"},
		{Name: "a", Value: "v w", Domain: ".threads.com", Path: "/"},
		{Name: "a", Value: "v", Domain: "evil;.com", Path: "/"},
		{Name: "a", Value: "v", Domain: ".threads.com", Path: "/", SameSite: "Bogus"},
	} {
		if err := client.SetCookie(ctx, bad); !errors.Is(err, ErrUnsafe) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
	_ = client.Close(ctx)
	var argv string
	for _, r := range readRecords(t, record) {
		if fakeCommandName(r.Argv) == "cookies" {
			argv = strings.Join(r.Argv, " ")
		}
	}
	if !strings.Contains(argv, "cookies set sessionid synthetic-value --domain .threads.com --path / --secure --http-only --same-site Lax --expires 1893456000") {
		t.Fatalf("argv %q", argv)
	}
}
