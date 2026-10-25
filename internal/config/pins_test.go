package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultPins(t *testing.T) {
	pins := DefaultPins()
	if len(pins) != 10 {
		t.Fatalf("expected 10 default pins, got %d", len(pins))
	}

	expectedIDs := []string{
		"matematik",
		"koruyucu",
		"donanim",
		"benchmark",
		"supertoto",
		"fxpro",
		"calisma_plani",
		"hf_advisor",
		"ozelders",
		"icloud_google",
	}

	expectedPorts := map[string]int{
		"matematik":     8775,
		"koruyucu":      8765,
		"donanim":       8899,
		"benchmark":     8420,
		"supertoto":     4577,
		"fxpro":         8787,
		"calisma_plani": 1420,
		"hf_advisor":    8000,
		"ozelders":      0,
		"icloud_google": 8910,
	}

	for i, id := range expectedIDs {
		svc := pins[i]
		if svc.ID != id {
			t.Errorf("pin[%d].ID = %q, want %q", i, svc.ID, id)
		}
		if !svc.Pinned {
			t.Errorf("pin[%d] (%s) Pinned = false, want true", i, id)
		}
		if svc.Port != expectedPorts[id] {
			t.Errorf("pin[%d] (%s) Port = %d, want %d", i, id, svc.Port, expectedPorts[id])
		}
		if svc.Name == "" {
			t.Errorf("pin[%d] (%s) Name is empty", i, id)
		}
		if svc.Host == "" {
			t.Errorf("pin[%d] (%s) Host is empty", i, id)
		}
		if svc.Category == "" {
			t.Errorf("pin[%d] (%s) Category is empty", i, id)
		}
		if svc.Badge == "" {
			t.Errorf("pin[%d] (%s) Badge is empty", i, id)
		}
		if svc.Description == "" {
			t.Errorf("pin[%d] (%s) Description is empty", i, id)
		}
		if svc.Color == "" {
			t.Errorf("pin[%d] (%s) Color is empty", i, id)
		}
		if svc.AppPath == "" {
			t.Errorf("pin[%d] (%s) AppPath is empty", i, id)
		}
	}

	// Verify isolation (modifying returned slice does not corrupt subsequent calls)
	pins[0].ID = "modified"
	freshPins := DefaultPins()
	if freshPins[0].ID != "matematik" {
		t.Fatalf("DefaultPins slice mutation contaminated next call")
	}
}

func TestLoadPinsFromFile(t *testing.T) {
	rootPath := filepath.Join("..", "..", "pins.toml")
	loaded, err := LoadPins(rootPath)
	if err != nil {
		t.Fatalf("LoadPins(%q) failed: %v", rootPath, err)
	}

	defaults := DefaultPins()
	if len(loaded) != len(defaults) {
		t.Fatalf("loaded %d pins, expected %d matching DefaultPins", len(loaded), len(defaults))
	}

	for i := range defaults {
		want := defaults[i]
		got := loaded[i]

		if got.ID != want.ID {
			t.Errorf("[%d] ID = %q, want %q", i, got.ID, want.ID)
		}
		if got.Name != want.Name {
			t.Errorf("[%d] Name = %q, want %q", i, got.Name, want.Name)
		}
		if got.Host != want.Host {
			t.Errorf("[%d] Host = %q, want %q", i, got.Host, want.Host)
		}
		if got.Port != want.Port {
			t.Errorf("[%d] Port = %d, want %d", i, got.Port, want.Port)
		}
		if !got.Pinned {
			t.Errorf("[%d] Pinned = false, want true", i)
		}
		if got.Category != want.Category {
			t.Errorf("[%d] Category = %q, want %q", i, got.Category, want.Category)
		}
		if got.Badge != want.Badge {
			t.Errorf("[%d] Badge = %q, want %q", i, got.Badge, want.Badge)
		}
		if got.Description != want.Description {
			t.Errorf("[%d] Description = %q, want %q", i, got.Description, want.Description)
		}
		if got.Color != want.Color {
			t.Errorf("[%d] Color = %q, want %q", i, got.Color, want.Color)
		}
		if got.AppPath != want.AppPath {
			t.Errorf("[%d] AppPath = %q, want %q", i, got.AppPath, want.AppPath)
		}
		if got.HealthURL != want.HealthURL {
			t.Errorf("[%d] HealthURL = %q, want %q", i, got.HealthURL, want.HealthURL)
		}
	}
}

func TestLoadPinsErrors(t *testing.T) {
	if _, err := LoadPins(""); err == nil {
		t.Error("expected error loading empty path, got nil")
	}

	if _, err := LoadPins("non_existent_file.toml"); err == nil {
		t.Error("expected error loading non-existent path, got nil")
	}
}

func TestParsePinsTableSyntax(t *testing.T) {
	tomlData := `
# Custom table syntax
[pins.demo_app]
name = "demo-app"
port = 3000
category = "Test"
badge = "Dev"
color = "#123456" # trailing hex comment
app_path = "/path/to/demo.app"
health_url = "http://127.0.0.1:3000/health"

[another_app]
name = "another"
port = 4000
description = 'Single quoted description with # inside quote'
`
	svcs, err := ParsePins(strings.NewReader(tomlData))
	if err != nil {
		t.Fatalf("ParsePins failed: %v", err)
	}

	if len(svcs) != 2 {
		t.Fatalf("expected 2 services, got %d", len(svcs))
	}

	first := svcs[0]
	if first.ID != "demo_app" || first.Name != "demo-app" || first.Port != 3000 {
		t.Errorf("first service mismatch: %+v", first)
	}
	if first.Color != "#123456" {
		t.Errorf("first.Color = %q, want '#123456'", first.Color)
	}
	if first.Host != "demo-app.localhost" {
		t.Errorf("first.Host = %q, want 'demo-app.localhost'", first.Host)
	}
	if !first.Pinned {
		t.Errorf("first.Pinned = false, want true")
	}

	second := svcs[1]
	if second.ID != "another_app" || second.Name != "another" || second.Port != 4000 {
		t.Errorf("second service mismatch: %+v", second)
	}
	if second.Description != "Single quoted description with # inside quote" {
		t.Errorf("second.Description = %q", second.Description)
	}
	if second.Host != "another.localhost" {
		t.Errorf("second.Host = %q, want 'another.localhost'", second.Host)
	}
}
