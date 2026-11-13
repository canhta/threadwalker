package config

import "testing"

func TestCatalogProviderIDDoesNotResolveSecrets(t *testing.T) {
	c := &Catalog{models: map[string]catalogModel{"chosen": {provider: "configured-provider"}}}
	if id, ok := c.ProviderID("chosen"); !ok || id != "configured-provider" {
		t.Fatal("selected provider metadata missing")
	}
	if _, ok := c.ProviderID("missing"); ok {
		t.Fatal("unknown profile accepted")
	}
	var absent *Catalog
	if _, ok := absent.ProviderID("chosen"); ok {
		t.Fatal("nil catalog accepted")
	}
}
