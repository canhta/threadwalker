package config

import "testing"

func TestYAMLDeclaredKeysIncludeBlankSelections(t *testing.T) {
	doc, err := parseSimpleYAML("llm:\n  profile:\n  base_url: ''\n  model: custom\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"llm", "llm.profile", "llm.base_url", "llm.model"} {
		if !doc.keys[field] {
			t.Errorf("declaration of %s was lost", field)
		}
	}
	if doc.keys["llm.api_key_env"] {
		t.Error("undeclared field is present")
	}
}
