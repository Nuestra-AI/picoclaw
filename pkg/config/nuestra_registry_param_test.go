package config

import (
	"encoding/json"
	"testing"
)

// Registry params reach SkillRegistryConfig through its custom UnmarshalJSON,
// which folds unrecognized keys into Param. The strict unknown-field walker
// matches struct tags and cannot see that, so without an exemption it rejects
// every param -- including upstream's own documented github "proxy" and
// clawhub "search_path" keys -- and startup fails.
func TestRegistryParamsAreNotReportedAsUnknownFields(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "nested param block",
			body: `{"tools":{"skills":{"enabled":true,"registries":[
				{"name":"github","enabled":true,"base_url":"https://github.com",
				 "param":{"allow":["owner/repo"]}}]}}}`,
		},
		{
			name: "upstream github proxy as a sibling key",
			body: `{"tools":{"skills":{"enabled":true,"registries":[
				{"name":"github","enabled":true,"base_url":"https://github.com",
				 "proxy":"http://127.0.0.1:7890"}]}}}`,
		},
		{
			name: "upstream clawhub search_path as a sibling key",
			body: `{"tools":{"skills":{"enabled":true,"registries":[
				{"name":"clawhub","enabled":true,"base_url":"https://clawhub.ai",
				 "search_path":"/api/v1/search"}]}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var cfg Config
			if err := decodeJSONWithDiagnostics([]byte(tc.body), &cfg, "config.json"); err != nil {
				t.Fatalf("registry param rejected: %v", err)
			}
		})
	}
}

// The exemption must not extend past the registry entry: a typo elsewhere in
// the config should still be caught.
func TestUnknownFieldsOutsideRegistriesStillReported(t *testing.T) {
	body := `{"tools":{"skills":{"enabled":true,"registriez":[]}}}`
	var cfg Config
	if err := decodeJSONWithDiagnostics([]byte(body), &cfg, "config.json"); err == nil {
		t.Fatal("a misspelled key outside a registry entry was accepted")
	}
}

// The params must survive the decode, not merely be tolerated by the walker.
func TestRegistryParamsSurviveDecoding(t *testing.T) {
	body := `{"name":"github","enabled":true,"base_url":"https://github.com",
	          "param":{"allow":["owner/repo@v1"]}}`
	var registry SkillRegistryConfig
	if err := json.Unmarshal([]byte(body), &registry); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var decoded struct {
		Allow []string `json:"allow"`
	}
	if err := registry.DecodeParam(&decoded); err != nil {
		t.Fatalf("decode param: %v", err)
	}
	if len(decoded.Allow) != 1 || decoded.Allow[0] != "owner/repo@v1" {
		t.Fatalf("allow = %v, want [owner/repo@v1]", decoded.Allow)
	}
}
