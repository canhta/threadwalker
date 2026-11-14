package config

import "testing"

func TestRedactorCollectsEverySecretFieldByConstruction(t *testing.T) {
	type nested struct{ Token Secret }
	type fixture struct {
		Template
		Added   Secret // a new Secret field needs no call-site change
		Inner   nested
		List    []Secret
		ByName  map[string]Secret
		Ptr     *Secret
		private Secret
	}
	p := NewSecret("ptr-secret")
	f := fixture{Added: NewSecret("added-secret"), Inner: nested{NewSecret("inner-secret")}, List: []Secret{NewSecret("list-secret")}, ByName: map[string]Secret{"k": NewSecret("map-secret")}, Ptr: &p, private: NewSecret("hidden")}
	f.LLM.APIKey = NewSecret("llm-secret")
	f.Telegram.BotToken = NewSecret("bot-secret")
	f.Telegram.OwnerChatID = NewSecret("12345")
	r := NewRedactor(f, (*fixture)(nil), nil)
	for _, raw := range []string{"added-secret", "inner-secret", "list-secret", "map-secret", "ptr-secret", "llm-secret", "bot-secret", "12345"} {
		if got := r.String("a " + raw + " b"); got != "a [REDACTED] b" {
			t.Errorf("%s: got %q", raw, got)
		}
		if !r.Contains(raw) {
			t.Errorf("%s: Contains false", raw)
		}
	}
	if got := r.String("hidden"); got != "hidden" {
		t.Errorf("unexported field redacted: %q", got)
	}
}

func TestRedactorMergesAnotherRedactorAndIgnoresEmptySecrets(t *testing.T) {
	base := NewRedactor(NewSecret("one"), NewSecret(""))
	r := NewRedactor(base, NewSecret("one"), NewSecret("two"))
	if got := r.String("one two"); got != "[REDACTED] [REDACTED]" || len(r.values) != 2 {
		t.Fatalf("got %q values %v", got, r.values)
	}
	var nilR *Redactor
	if nilR.String("x") != "x" || nilR.Contains("x") {
		t.Fatal("nil redactor must pass through")
	}
}

func TestRedactorReplacesLongerSecretWhole(t *testing.T) {
	r := NewRedactor(struct{ A, B Secret }{NewSecret("abc"), NewSecret("xxabcxx")})
	if got := r.String("key=xxabcxx"); got != "key=[REDACTED]" {
		t.Fatalf("got %q", got)
	}
}

func TestRedactorAddCoversALaterSecret(t *testing.T) {
	r := NewRedactor(NewSecret("first-secret"))
	r.Add(NewSecret("refreshed-secret"))
	r.Add(NewSecret("refreshed-secret"))
	r.Add(Secret{})
	var nilRedactor *Redactor
	nilRedactor.Add(NewSecret("x"))
	if got := r.String("a first-secret b refreshed-secret"); got != "a [REDACTED] b [REDACTED]" || !r.Contains("refreshed-secret") || len(r.values) != 2 {
		t.Fatalf("got %q values=%d", got, len(r.values))
	}
}
