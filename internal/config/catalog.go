package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Catalog holds named provider and model configurations without resolved secrets.
type Catalog struct {
	providers map[string]LLMConfig
	models    map[string]catalogModel
}

type catalogModel struct {
	provider string
	model    string
	in, out  float64
}

// ProviderID returns configured metadata without resolving any credentials.
func (c *Catalog) ProviderID(profile string) (string, bool) {
	if c == nil {
		return "", false
	}
	model, ok := c.models[profile]
	return model.provider, ok
}

// LoadCatalog loads provider and model declarations without reading environment variables.
func LoadCatalog(path string) (*Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading catalog %s: %w", path, err)
	}
	doc, err := parseSimpleYAML(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing catalog %s: %w", path, err)
	}
	if err := catalogFields(doc.root, "", "providers", "models"); err != nil {
		return nil, err
	}
	c := &Catalog{providers: make(map[string]LLMConfig), models: make(map[string]catalogModel)}
	providers, err := catalogMapping(doc.root.children["providers"], "providers")
	if err != nil {
		return nil, err
	}
	models, err := catalogMapping(doc.root.children["models"], "models")
	if err != nil {
		return nil, err
	}
	for _, id := range catalogKeys(providers) {
		node, field := providers[id], "providers."+id
		if err := catalogFields(node, field, "protocol", "base_url", "api_key_env"); err != nil {
			return nil, err
		}
		protocol, err := catalogString(node.children["protocol"], field+".protocol")
		if err != nil {
			return nil, err
		}
		if protocol != "openai" && protocol != "anthropic" {
			return nil, catalogError(field+".protocol", "must be openai or anthropic")
		}
		baseURL, err := catalogString(node.children["base_url"], field+".base_url")
		if err != nil {
			return nil, err
		}
		if err := validateCatalogURL(baseURL); err != nil {
			return nil, catalogError(field+".base_url", "must be an absolute HTTP(S) URL without credentials, query or fragment")
		}
		keyEnv, err := catalogString(node.children["api_key_env"], field+".api_key_env")
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(keyEnv, "= \t\r\n\x00") {
			return nil, catalogError(field+".api_key_env", "must name a valid environment variable")
		}
		c.providers[id] = LLMConfig{Protocol: protocol, BaseURL: baseURL, APIKeyEnv: keyEnv}
	}
	for _, id := range catalogKeys(models) {
		node, field := models[id], "models."+id
		if err := catalogFields(node, field, "provider", "model", "price_per_mtok_in_usd", "price_per_mtok_out_usd"); err != nil {
			return nil, err
		}
		provider, err := catalogString(node.children["provider"], field+".provider")
		if err != nil {
			return nil, err
		}
		if _, exists := c.providers[provider]; !exists {
			return nil, catalogError(field+".provider", "unknown provider reference")
		}
		model, err := catalogString(node.children["model"], field+".model")
		if err != nil {
			return nil, err
		}
		in, err := catalogPrice(node.children["price_per_mtok_in_usd"], field+".price_per_mtok_in_usd")
		if err != nil {
			return nil, err
		}
		out, err := catalogPrice(node.children["price_per_mtok_out_usd"], field+".price_per_mtok_out_usd")
		if err != nil {
			return nil, err
		}
		c.models[id] = catalogModel{provider: provider, model: model, in: in, out: out}
	}
	return c, nil
}

func catalogError(field, message string) error {
	return fmt.Errorf("catalog field %s: %s", field, message)
}

func catalogKeys(nodes map[string]*yamlNode) []string {
	keys := make([]string, 0, len(nodes))
	for key := range nodes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func catalogMapping(node *yamlNode, field string) (map[string]*yamlNode, error) {
	if node == nil || node.kind != "mapping" || len(node.children) == 0 {
		return nil, catalogError(field, "must be a non-empty mapping")
	}
	return node.children, nil
}

func catalogFields(node *yamlNode, field string, allowed ...string) error {
	if node == nil || node.kind != "mapping" {
		return catalogError(field, "must be a mapping")
	}
	for _, key := range catalogKeys(node.children) {
		known := false
		for _, candidate := range allowed {
			if key == candidate {
				known = true
				break
			}
		}
		if !known {
			fullKey := key
			if field != "" {
				fullKey = field + "." + key
			}
			return catalogError(fullKey, "unknown field")
		}
	}
	return nil
}

func catalogString(node *yamlNode, field string) (string, error) {
	if node == nil || node.kind != "scalar" || strings.TrimSpace(node.value) == "" {
		return "", catalogError(field, "must be a non-empty scalar")
	}
	raw := node.raw
	if strings.HasPrefix(raw, "'") || strings.HasPrefix(raw, "\"") {
		if !catalogQuotedScalar(raw) {
			return "", catalogError(field, "invalid quoted scalar")
		}
	} else if strings.Contains(raw, ": ") || strings.Contains(raw, ":\t") || strings.HasPrefix(raw, "- ") || strings.ContainsAny(raw[:1], "[{&*!}") || raw == "~" || strings.EqualFold(raw, "null") || strings.EqualFold(raw, "true") || strings.EqualFold(raw, "false") {
		return "", catalogError(field, "must be a string scalar")
	}
	return strings.TrimSpace(node.value), nil
}

func catalogQuotedScalar(raw string) bool {
	quote := raw[0]
	for i := 1; i < len(raw); i++ {
		if quote == '"' && raw[i] == '\\' {
			i++
			continue
		}
		if raw[i] != quote {
			continue
		}
		if quote == '\'' && i+1 < len(raw) && raw[i+1] == quote {
			i++
			continue
		}
		return i == len(raw)-1
	}
	return false
}

func catalogPrice(node *yamlNode, field string) (float64, error) {
	raw, err := catalogString(node, field)
	if err != nil {
		return 0, err
	}
	price, err := strconv.ParseFloat(raw, 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 {
		return 0, catalogError(field, "must be a finite non-negative number")
	}
	return price, nil
}

func validateCatalogURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("invalid base URL")
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("invalid URL port")
		}
	}
	return nil
}

// Resolve resolves only the selected model profile's provider secret.
func (c *Catalog) Resolve(profile string, getenv func(string) string) (LLMConfig, error) {
	if c == nil {
		return LLMConfig{}, fmt.Errorf("catalog field models: unknown model profile %q", profile)
	}
	model, ok := c.models[profile]
	if !ok {
		return LLMConfig{}, fmt.Errorf("catalog field models: unknown model profile %q", profile)
	}
	llm, ok := c.providers[model.provider]
	if !ok {
		return LLMConfig{}, fmt.Errorf("catalog field models.%s.provider: unknown provider", profile)
	}
	llm.Model, llm.PricePerMTokInUSD, llm.PricePerMTokOutUSD = model.model, model.in, model.out
	var key string
	if getenv != nil {
		key = getenv(llm.APIKeyEnv)
	}
	if key == "" {
		return LLMConfig{}, fmt.Errorf("catalog field providers.%s.api_key_env: environment variable %s is unset or empty", model.provider, llm.APIKeyEnv)
	}
	llm.APIKey = NewSecret(key)
	return llm, nil
}
