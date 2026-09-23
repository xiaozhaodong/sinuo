package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type codexCapabilityFixture struct {
	Base  map[string]json.RawMessage `json:"base"`
	Cases []struct {
		Name   string                     `json:"name"`
		Set    map[string]json.RawMessage `json:"set"`
		Remove []string                   `json:"remove"`
		Valid  bool                       `json:"valid"`
	} `json:"cases"`
}

func loadCodexCapabilityFixture(t *testing.T) codexCapabilityFixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "codex_capabilities_cases.json"))
	if err != nil {
		t.Fatalf("read fixture failed: %v", err)
	}
	var fixture codexCapabilityFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("parse fixture failed: %v", err)
	}
	return fixture
}

func buildCodexCapabilities(t *testing.T, base map[string]json.RawMessage, set map[string]json.RawMessage, remove []string) string {
	t.Helper()
	fields := make(map[string]json.RawMessage, len(base)+len(set))
	for key, value := range base {
		fields[key] = value
	}
	for _, key := range remove {
		delete(fields, key)
	}
	for key, value := range set {
		fields[key] = value
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal capabilities failed: %v", err)
	}
	return string(payload)
}

func validCodexModelEntry(t *testing.T, id string) CodexModelEntry {
	t.Helper()
	fixture := loadCodexCapabilityFixture(t)
	return CodexModelEntry{ID: id, Enabled: true, DisplayName: strings.ToUpper(id), CapabilitiesJSON: buildCodexCapabilities(t, fixture.Base, nil, nil)}
}

func TestCodexCapabilityValidation_SharedCases(t *testing.T) {
	fixture := loadCodexCapabilityFixture(t)
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			entry := CodexModelEntry{ID: "fixture-model", Enabled: true, DisplayName: "Fixture", CapabilitiesJSON: buildCodexCapabilities(t, fixture.Base, tc.Set, tc.Remove)}
			err := ValidateCodexModelCatalog(CodexModelCatalog{Enabled: true, Mode: CodexModelsModeLocal, Models: []CodexModelEntry{entry}})
			if tc.Valid && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !tc.Valid && err == nil {
				t.Fatal("expected validation error")
			}
			if (entry.CapabilitiesError() == "") != tc.Valid {
				t.Fatalf("CapabilitiesError disagrees with validation: %q", entry.CapabilitiesError())
			}
		})
	}
}

func TestValidateCodexModelCatalog_ModeAndIDs(t *testing.T) {
	for _, mode := range []string{"", "Local", " passthrough "} {
		if err := ValidateCodexModelCatalog(CodexModelCatalog{Mode: mode}); err != nil {
			t.Fatalf("mode %q should be accepted like NormalizeCodexModelCatalog, got %v", mode, err)
		}
	}
	if err := ValidateCodexModelCatalog(CodexModelCatalog{Mode: "remote"}); err == nil {
		t.Fatal("expected invalid mode error")
	}
	for _, id := range []string{"my model", "tab\tmodel", "nbsp\u00a0model", "c1\u0085model", strings.Repeat("m", 257)} {
		if err := ValidateCodexModelCatalog(CodexModelCatalog{Models: []CodexModelEntry{{ID: id}}}); err == nil {
			t.Fatalf("expected invalid ID %q to be rejected", id)
		}
	}
	duplicated := CodexModelCatalog{Models: []CodexModelEntry{{ID: "gpt-x"}, {ID: " gpt-x "}}}
	if err := ValidateCodexModelCatalog(duplicated); err == nil || !strings.Contains(err.Error(), "重复") {
		t.Fatalf("expected duplicate ID error, got %v", err)
	}
}

func TestBuildCodexModelMetadata_DerivesIdentityAndPreservesRawFields(t *testing.T) {
	fixture := loadCodexCapabilityFixture(t)
	entry := CodexModelEntry{
		ID:               " gpt-custom ",
		Description:      "自定义模型",
		CapabilitiesJSON: buildCodexCapabilities(t, fixture.Base, map[string]json.RawMessage{"future_number": json.RawMessage(`12345678901234567890`)}, nil),
	}
	metadata, err := entry.BuildCodexModelMetadata()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &fields); err != nil {
		t.Fatal(err)
	}
	expected := map[string]string{"slug": `"gpt-custom"`, "display_name": `"gpt-custom"`, "description": `"自定义模型"`, "future_number": `12345678901234567890`}
	for key, want := range expected {
		if got := string(fields[key]); got != want {
			t.Fatalf("%s = %s, want %s", key, got, want)
		}
	}
}

func TestBuildListResponse_SkipsInvalidCapabilitiesButKeepsData(t *testing.T) {
	broken := validCodexModelEntry(t, "gpt-broken")
	broken.CapabilitiesJSON = strings.Replace(broken.CapabilitiesJSON, `"freeform"`, `"function"`, 1)
	catalog := CodexModelCatalog{Enabled: true, Mode: CodexModelsModeLocal, Models: []CodexModelEntry{
		validCodexModelEntry(t, "gpt-valid"),
		broken,
		{ID: "gpt-plain", Enabled: true},
		{ID: "gpt-disabled", Enabled: false, CapabilitiesJSON: "not json"},
	}}

	resp, skipped := catalog.BuildListResponse()
	if len(resp.Data) != 3 {
		t.Fatalf("expected 3 enabled models in data, got %d", len(resp.Data))
	}
	if len(resp.Models) != 1 || !strings.Contains(string(resp.Models[0]), `"slug":"gpt-valid"`) {
		t.Fatalf("expected only gpt-valid capabilities, got %s", resp.Models)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0].Error(), "gpt-broken") {
		t.Fatalf("expected gpt-broken to be reported as skipped, got %v", skipped)
	}
}

func TestBuildListResponse_EmptyCapabilitiesStillSerializesArray(t *testing.T) {
	resp, skipped := DefaultCodexModelCatalog().BuildListResponse()
	if len(skipped) != 0 {
		t.Fatalf("unexpected skipped models: %v", skipped)
	}
	payload, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	// Codex 收到空 models 时会回落到内置目录，因此必须输出空数组而不是 null。
	if !strings.Contains(string(payload), `"models":[]`) {
		t.Fatalf("expected empty models array, got %s", payload)
	}
}

func TestGetCodexModelCatalog_ToleratesInvalidStoredData(t *testing.T) {
	_, settings, _ := newCodexSubscriptionProxySettingsTestEnv(t)
	ctx := context.Background()
	legacy := validCodexModelEntry(t, "gpt-legacy")
	legacy.CapabilitiesJSON = strings.Replace(legacy.CapabilitiesJSON, `"list"`, `"public"`, 1)
	stored := CodexModelCatalog{Enabled: true, Mode: CodexModelsModeLocal, Models: []CodexModelEntry{
		{ID: "my model", Enabled: true, Source: "custom"},
		legacy,
	}}
	payload, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	if err := settings.Set(ctx, CategoryCodexModels, CodexModelsCatalogKey, string(payload)); err != nil {
		t.Fatal(err)
	}

	catalog, err := settings.GetCodexModelCatalog(ctx)
	if err != nil {
		t.Fatalf("stored data with legacy entries must stay readable, got %v", err)
	}
	if len(catalog.Models) != 2 {
		t.Fatalf("expected both legacy entries to be kept, got %d", len(catalog.Models))
	}
	for _, model := range catalog.Models {
		if model.ID == "gpt-legacy" && model.CapabilitiesError() == "" {
			t.Fatal("expected invalid stored capabilities to be flagged")
		}
	}
	if _, err := settings.SaveCodexModelCatalog(ctx, catalog); err == nil {
		t.Fatal("expected strict validation when saving the legacy catalog")
	}
}
