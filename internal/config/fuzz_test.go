package config

import (
	"strings"
	"testing"
)

func FuzzParsePins(f *testing.F) {
	// 1. Standard [[pins]] array-of-tables
	f.Add(`[[pins]]
id = "matematik"
name = "MEB Matematik"
port = 8775
category = "Education"
badge = "Master"
color = "#3730A3"
pinned = true

[[pins]]
id = "koruyucu"
name = "Mac Sistem Koruyucu"
port = 8765
pinned = true
`)

	// 2. Table-based [pins.app] format
	f.Add(`[pins.matematik]
name = "MEB Matematik"
port = 8775

[pins.shop]
port = 3000
host = "shop.localhost"
`)

	// 3. Simple table [app] format
	f.Add(`[matematik]
port = 8775

[shop]
port = 3000
`)

	// 4. Malformed syntax, unclosed quotes, comments
	f.Add(`# Comment line
[incomplete
port = notanumber
name = "unclosed
invalid =
= no_key
`)

	// 5. Empty configuration
	f.Add("")

	// 6. Whitespace and non-ASCII characters
	f.Add(`
# Türkçe Pin Tanımı
[pins.e_okul]
name = "e-Okul Yönetimi"
port = 8080
pinned = 1
category = "Okul & Eğitim"
`)

	f.Fuzz(func(t *testing.T, tomlData string) {
		// ParsePins must handle arbitrary and malformed TOML inputs without panicking
		svcs, err := ParsePins(strings.NewReader(tomlData))
		if err != nil {
			// Non-fatal parse errors are valid on fuzzed input
			return
		}

		// Ensure returned services maintain safe invariants
		for i, svc := range svcs {
			if svc.Port < 0 {
				t.Errorf("svc[%d].Port is negative: %d", i, svc.Port)
			}
			_ = svc.ID
			_ = svc.Name
			_ = svc.Host
			_ = svc.Pinned
		}
	})
}
