package service

import "testing"

func TestNormalizeCodexModelCatalog_CustomModelWinsDuplicate(t *testing.T) {
	catalog := NormalizeCodexModelCatalog(CodexModelCatalog{
		Enabled: true,
		Mode:    "invalid",
		Models: []CodexModelEntry{
			{ID: "gpt-5.3-codex", Source: "official", Enabled: true, DisplayName: "Official"},
			{ID: "gpt-5.3-codex", Source: "custom", Enabled: false, DisplayName: "Custom"},
			{ID: "  my-model  ", Enabled: true},
			{ID: "   ", Enabled: true},
		},
	})

	if catalog.Mode != CodexModelsModeLocal {
		t.Fatalf("expected invalid mode to normalize to local, got %s", catalog.Mode)
	}
	if len(catalog.Models) != 2 {
		t.Fatalf("expected 2 normalized models, got %d", len(catalog.Models))
	}

	var codex *CodexModelEntry
	var custom *CodexModelEntry
	for i := range catalog.Models {
		model := &catalog.Models[i]
		switch model.ID {
		case "gpt-5.3-codex":
			codex = model
		case "my-model":
			custom = model
		}
	}

	if codex == nil || codex.Source != "custom" || codex.Enabled {
		t.Fatalf("expected custom duplicate to win, got %+v", codex)
	}
	if custom == nil || custom.Source != "custom" || custom.Object != "model" || custom.OwnedBy != "openai" {
		t.Fatalf("expected custom defaults to be applied, got %+v", custom)
	}
}

func TestCodexModelCatalogBuildListResponse_OnlyEnabledModels(t *testing.T) {
	catalog := NormalizeCodexModelCatalog(CodexModelCatalog{
		Enabled: true,
		Mode:    CodexModelsModeLocal,
		Models: []CodexModelEntry{
			{ID: "disabled", Enabled: false},
			{ID: "enabled", Enabled: true, OwnedBy: "custom-owner"},
		},
	})

	resp, skipped := catalog.BuildListResponse()
	if len(skipped) != 0 {
		t.Fatalf("unexpected skipped models: %v", skipped)
	}
	if resp.Object != "list" {
		t.Fatalf("expected list response, got %s", resp.Object)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("expected one enabled model, got %d", len(resp.Data))
	}
	if resp.Data[0].ID != "enabled" || resp.Data[0].Object != "model" || resp.Data[0].OwnedBy != "custom-owner" {
		t.Fatalf("unexpected model response: %+v", resp.Data[0])
	}
}
