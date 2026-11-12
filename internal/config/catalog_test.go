package config_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/canhta/threadwalker/internal/config"
)

const catalogYAML = `providers:
  my.gateway:
    protocol: openai
    base_url: https://gateway.example.test/custom/v1
    api_key_env: MY_UNUSUAL_GATEWAY_KEY
  other-vendor:
    protocol: anthropic
    base_url: https://other.example.test
    api_key_env: ANOTHER_VENDOR_SECRET
models:
  quick.profile:
    provider: my.gateway
    model: vendor/arbitrary-model-2099
    price_per_mtok_in_usd: 0.2
    price_per_mtok_out_usd: 0.4
  thoughtful:
    provider: my.gateway
    model: custom-reasoner
    price_per_mtok_in_usd: 0
    price_per_mtok_out_usd: 2
  fallback:
    provider: other-vendor
    model: anything-at-all
    price_per_mtok_in_usd: 1
    price_per_mtok_out_usd: 3
`

func writeCatalog(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.yaml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCatalogMultipleProvidersAndSharedModels(t *testing.T) {
	c, err := config.LoadCatalog(writeCatalog(t, catalogYAML))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		profile, protocol, endpoint, model, keyEnv string
		in, out                                    float64
	}{
		{"quick.profile", "openai", "https://gateway.example.test/custom/v1", "vendor/arbitrary-model-2099", "MY_UNUSUAL_GATEWAY_KEY", .2, .4},
		{"thoughtful", "openai", "https://gateway.example.test/custom/v1", "custom-reasoner", "MY_UNUSUAL_GATEWAY_KEY", 0, 2},
		{"fallback", "anthropic", "https://other.example.test", "anything-at-all", "ANOTHER_VENDOR_SECRET", 1, 3},
	}
	for _, tc := range cases {
		t.Run(tc.profile, func(t *testing.T) {
			var reads []string
			llm, err := c.Resolve(tc.profile, func(name string) string { reads = append(reads, name); return "secret-" + name })
			if err != nil {
				t.Fatal(err)
			}
			if len(reads) != 1 || reads[0] != tc.keyEnv {
				t.Fatalf("environment reads = %v", reads)
			}
			if llm.Protocol != tc.protocol || llm.BaseURL != tc.endpoint || llm.Model != tc.model || llm.APIKeyEnv != tc.keyEnv || llm.PricePerMTokInUSD != tc.in || llm.PricePerMTokOutUSD != tc.out {
				t.Fatalf("resolved config = %+v", llm)
			}
			if llm.APIKey.Expose() != "secret-"+tc.keyEnv {
				t.Fatal("selected secret was not resolved")
			}
			if strings.Contains(fmt.Sprintf("%v %+v %#v", llm, llm, llm), "secret-") {
				t.Fatal("formatted config leaks secret")
			}
		})
	}
}

func TestCatalogReasoningEffort(t *testing.T) {
	text := `providers:
  chosen:
    protocol: openai
    base_url: https://gateway.example.test/v1
    api_key_env: CHOSEN_KEY
  native:
    protocol: anthropic
    base_url: https://native.example.test
    api_key_env: NATIVE_KEY
models:
  reasoning:
    provider: chosen
    model: arbitrary-model
    price_per_mtok_in_usd: 0.1
    price_per_mtok_out_usd: 0.5
    reasoning_effort: none
  plain:
    provider: chosen
    model: arbitrary-model
    price_per_mtok_in_usd: 0.1
    price_per_mtok_out_usd: 0.5
`
	c, err := config.LoadCatalog(writeCatalog(t, text))
	if err != nil {
		t.Fatal(err)
	}
	llm, err := c.Resolve("reasoning", func(string) string { return "secret" })
	if err != nil || llm.ReasoningEffort != "none" {
		t.Fatalf("resolved reasoning effort = %q, %v", llm.ReasoningEffort, err)
	}
	llm, err = c.Resolve("plain", func(string) string { return "secret" })
	if err != nil || llm.ReasoningEffort != "" {
		t.Fatalf("unset reasoning effort = %q, %v", llm.ReasoningEffort, err)
	}

	native := `providers:
  native:
    protocol: anthropic
    base_url: https://native.example.test
    api_key_env: NATIVE_KEY
models:
  native:
    provider: native
    model: arbitrary-model
    price_per_mtok_in_usd: 0.1
    price_per_mtok_out_usd: 0.5
    reasoning_effort: none
`
	if _, err := config.LoadCatalog(writeCatalog(t, native)); err == nil || !strings.Contains(err.Error(), "reasoning_effort") {
		t.Fatalf("anthropic reasoning effort error = %v", err)
	}
}

func TestCatalogResolveErrorsDoNotReadUnselectedKeys(t *testing.T) {
	c, err := config.LoadCatalog(writeCatalog(t, catalogYAML))
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range []string{"", "missing"} {
		_, err := c.Resolve(profile, func(string) string { t.Fatal("unknown profile read environment"); return "" })
		if err == nil || !strings.Contains(err.Error(), "models") {
			t.Errorf("Resolve(%q) = %v", profile, err)
		}
	}
	for _, getenv := range []func(string) string{nil, func(name string) string {
		if name != "MY_UNUSUAL_GATEWAY_KEY" {
			t.Fatalf("read unused key %s", name)
		}
		return ""
	}} {
		_, err := c.Resolve("quick.profile", getenv)
		if err == nil || !strings.Contains(err.Error(), "providers.my.gateway.api_key_env") {
			t.Errorf("missing selected key = %v", err)
		}
	}
	var empty config.Catalog
	if _, err := empty.Resolve("missing", nil); err == nil {
		t.Error("zero catalog accepted unknown profile")
	}
	var absent *config.Catalog
	if _, err := absent.Resolve("missing", nil); err == nil {
		t.Error("nil catalog accepted unknown profile")
	}
}

func TestCatalogRejectsInvalidDeclarations(t *testing.T) {
	cases := []struct{ name, text, field string }{
		{"empty", "", "providers"},
		{"missing providers", "models:\n", "providers"},
		{"missing models", strings.Split(catalogYAML, "models:")[0], "models"},
		{"provider list", "providers:\n  - vendor\nmodels:\n", "providers"},
		{"provider scalar", strings.Replace(catalogYAML, "  my.gateway:\n", "  my.gateway: literal\n", 1), "providers.my.gateway"},
		{"unknown root", catalogYAML + "unexpected: private-value\n", "unexpected"},
		{"unknown provider field", strings.Replace(catalogYAML, "    protocol: openai", "    api_key: private-value\n    protocol: openai", 1), "providers.my.gateway.api_key"},
		{"unknown model field", catalogYAML + "    typo: private-value\n", "models.fallback.typo"},
		{"missing protocol", strings.Replace(catalogYAML, "    protocol: openai\n", "", 1), "providers.my.gateway.protocol"},
		{"missing endpoint", strings.Replace(catalogYAML, "    base_url: https://gateway.example.test/custom/v1\n", "", 1), "providers.my.gateway.base_url"},
		{"missing key name", strings.Replace(catalogYAML, "    api_key_env: MY_UNUSUAL_GATEWAY_KEY\n", "", 1), "providers.my.gateway.api_key_env"},
		{"unsupported protocol", strings.Replace(catalogYAML, "protocol: openai", "protocol: private-value", 1), "providers.my.gateway.protocol"},
		{"relative endpoint", strings.Replace(catalogYAML, "https://gateway.example.test/custom/v1", "/relative", 1), "providers.my.gateway.base_url"},
		{"unsupported scheme", strings.Replace(catalogYAML, "https://gateway.example.test/custom/v1", "ftp://example.test", 1), "providers.my.gateway.base_url"},
		{"endpoint credentials", strings.Replace(catalogYAML, "https://gateway.example.test/custom/v1", "https://user:private-value@example.test", 1), "providers.my.gateway.base_url"},
		{"empty endpoint host", strings.Replace(catalogYAML, "https://gateway.example.test/custom/v1", "https:///v1", 1), "providers.my.gateway.base_url"},
		{"invalid endpoint", strings.Replace(catalogYAML, "https://gateway.example.test/custom/v1", "http://%private-value", 1), "providers.my.gateway.base_url"},
		{"invalid key name", strings.Replace(catalogYAML, "MY_UNUSUAL_GATEWAY_KEY", "BAD=private-value", 1), "providers.my.gateway.api_key_env"},
		{"unknown provider", strings.Replace(catalogYAML, "provider: my.gateway", "provider: missing", 1), "models.quick.profile.provider"},
		{"missing provider reference", strings.Replace(catalogYAML, "    provider: my.gateway\n", "", 1), "models.quick.profile.provider"},
		{"missing model", strings.Replace(catalogYAML, "    model: vendor/arbitrary-model-2099\n", "", 1), "models.quick.profile.model"},
		{"empty model", strings.Replace(catalogYAML, "model: vendor/arbitrary-model-2099", "model: ''", 1), "models.quick.profile.model"},
		{"model list", strings.Replace(catalogYAML, "    model: vendor/arbitrary-model-2099", "    model:\n      - private-value", 1), "models.quick.profile.model"},
		{"flow model", strings.Replace(catalogYAML, "model: vendor/arbitrary-model-2099", "model: [private-value]", 1), "models.quick.profile.model"},
		{"model null", strings.Replace(catalogYAML, "model: vendor/arbitrary-model-2099", "model: null", 1), "models.quick.profile.model"},
		{"unclosed quote", strings.Replace(catalogYAML, "model: vendor/arbitrary-model-2099", "model: 'private-value", 1), "models.quick.profile.model"},
	}
	for _, priceField := range []string{"price_per_mtok_in_usd", "price_per_mtok_out_usd"} {
		old := priceField + ": 0.2"
		if priceField == "price_per_mtok_out_usd" {
			old = priceField + ": 0.4"
		}
		for _, value := range []string{"-1", "NaN", "+Inf", "-Inf", ".nan", ".inf", "1e9999", "private-value", "''"} {
			cases = append(cases, struct{ name, text, field string }{priceField + " " + value, strings.Replace(catalogYAML, old, priceField+": "+value, 1), "models.quick.profile." + priceField})
		}
		cases = append(cases, struct{ name, text, field string }{priceField + " missing", strings.Replace(catalogYAML, "    "+old+"\n", "", 1), "models.quick.profile." + priceField})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.LoadCatalog(writeCatalog(t, tc.text))
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error = %v, want field %s", err, tc.field)
			}
			if strings.Contains(err.Error(), "private-value") {
				t.Fatalf("error echoes private input: %v", err)
			}
		})
	}
}

func TestCatalogRejectsDuplicateAndMalformedNesting(t *testing.T) {
	cases := []struct{ name, text, field string }{
		{"duplicate section", catalogYAML + "providers:\n", "providers"},
		{"duplicate provider", strings.Replace(catalogYAML, "  other-vendor:\n", "  my.gateway:\n", 1), "providers.my.gateway"},
		{"duplicate quoted provider", strings.Replace(catalogYAML, "  other-vendor:\n", "  'my.gateway':\n", 1), "providers.my.gateway"},
		{"duplicate profile", strings.Replace(catalogYAML, "  thoughtful:\n", "  quick.profile:\n", 1), "models.quick.profile"},
		{"duplicate provider field", strings.Replace(catalogYAML, "    protocol: openai", "    protocol: anthropic\n    protocol: openai", 1), "providers.my.gateway.protocol"},
		{"duplicate model field", strings.Replace(catalogYAML, "    model: custom-reasoner", "    model: first\n    model: custom-reasoner", 1), "models.thoughtful.model"},
		{"field too deep", strings.Replace(catalogYAML, "    base_url: https://gateway", "      base_url: https://gateway", 1), "providers.my.gateway"},
		{"field too shallow", strings.Replace(catalogYAML, "    base_url: https://gateway", "   base_url: https://gateway", 1), "providers.my.gateway"},
		{"field at root", strings.Replace(catalogYAML, "    base_url: https://gateway", "base_url: https://gateway", 1), "base_url"},
		{"nested model value", strings.Replace(catalogYAML, "model: custom-reasoner", "model:\n      child: value", 1), "models.thoughtful.model"},
		{"mixed list and map", strings.Replace(catalogYAML, "  my.gateway:\n", "  - orphan\n  my.gateway:\n", 1), "providers"},
		{"tab indentation", strings.Replace(catalogYAML, "    protocol: openai", "\tprotocol: openai", 1), "indentation"},
		{"no colon", "providers:\n  invalid-line\n", "expected key"},
		{"empty id", strings.Replace(catalogYAML, "  my.gateway:\n", "  '':\n", 1), "empty mapping key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.LoadCatalog(writeCatalog(t, tc.text))
			if err == nil || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("error = %v, want %s", err, tc.field)
			}
		})
	}
}

func TestCatalogQuotedArbitraryIDsAndComments(t *testing.T) {
	text := strings.ReplaceAll(catalogYAML, "my.gateway", "gateway:custom")
	text = strings.Replace(text, "  gateway:custom:", "  'gateway:custom':", 1)
	text = strings.Replace(text, "  quick.profile:", "  \"fast:custom\":", 1)
	text = strings.Replace(text, "protocol: openai", "protocol: 'openai' # protocol comment", 1)
	text = strings.Replace(text, "model: vendor/arbitrary-model-2099", "model: 'vendor/model#1' # model comment", 1)
	c, err := config.LoadCatalog(writeCatalog(t, strings.ReplaceAll(text, "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	llm, err := c.Resolve("fast:custom", func(string) string { return "selected-secret" })
	if err != nil {
		t.Fatal(err)
	}
	if llm.Model != "vendor/model#1" || llm.Protocol != "openai" {
		t.Fatalf("config = %+v", llm)
	}
}

func TestCatalogUnreadableFile(t *testing.T) {
	if _, err := config.LoadCatalog(filepath.Join(t.TempDir(), "absent.yaml")); err == nil || !strings.Contains(err.Error(), "reading catalog") {
		t.Fatalf("error = %v", err)
	}
}

func TestCatalogRejectsMalformedScalarValuesAndPorts(t *testing.T) {
	cases := []struct{ old, value, field string }{
		{"model: custom-reasoner", "model: 'private-value' trailing'", "models.thoughtful.model"},
		{"model: custom-reasoner", "model: value: private-value", "models.thoughtful.model"},
		{"https://gateway.example.test/custom/v1", "https://example.test:99999", "providers.my.gateway.base_url"},
		{"https://gateway.example.test/custom/v1", "https://example.test:0", "providers.my.gateway.base_url"},
		{"https://gateway.example.test/custom/v1", "https://example.test?key=private-value", "providers.my.gateway.base_url"},
		{"https://gateway.example.test/custom/v1", "https://example.test#private-value", "providers.my.gateway.base_url"},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			_, err := config.LoadCatalog(writeCatalog(t, strings.Replace(catalogYAML, tc.old, tc.value, 1)))
			if err == nil || !strings.Contains(err.Error(), tc.field) || strings.Contains(err.Error(), "private-value") {
				t.Fatalf("error = %v, want secret-free field %s", err, tc.field)
			}
		})
	}
}
