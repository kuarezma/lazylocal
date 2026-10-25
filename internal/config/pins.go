// Package config provides configuration loading for lazylocal, including
// static pinned services defined in pins.toml and fallback defaults.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/kuarezma/lazylocal/internal/model"
)

// DefaultPins returns the 10 default pinned services ported from local-apps-hub.
// Each service has Pinned set to true.
func DefaultPins() []model.Service {
	return []model.Service{
		{
			ID:          "matematik",
			Name:        "matematik",
			Host:        "matematik.localhost",
			Port:        8775,
			Pinned:      true,
			Category:    "Okul & Eğitim",
			Badge:       "Eğitim Motoru",
			Description: "MEB Matematik Master Doküman Merkezi — 5, 6, 7, 8. Sınıf & LGS/YKS ders materyalleri, çalışma kağıdı ve interaktif yönetim paneli.",
			Color:       "#3b82f6",
			AppPath:     "~/Applications/Matematik Doküman Merkezi.app",
			HealthURL:   "http://127.0.0.1:8775/api/health",
		},
		{
			ID:          "koruyucu",
			Name:        "koruyucu",
			Host:        "koruyucu.localhost",
			Port:        8765,
			Pinned:      true,
			Category:    "Sistem & Araçlar",
			Badge:       "Bekçi & Güvenlik",
			Description: "Mac Sistem Koruyucu — macOS arka plan süreçleri, bellek/CPU tüketimi ve anlık kilitlenme önleyici sistem kalkanı.",
			Color:       "#10b981",
			AppPath:     "/Applications/Mac Sistem Koruyucu.app",
			HealthURL:   "http://127.0.0.1:8765/api/stats",
		},
		{
			ID:          "donanim",
			Name:        "donanim",
			Host:        "donanim.localhost",
			Port:        8899,
			Pinned:      true,
			Category:    "Sistem & Araçlar",
			Badge:       "Donanım Analizi",
			Description: "Mac Donanım Özellikleri — Apple Silicon M3 çip mimarisi, termal durum, pil sağlığı ve detaylı donanım dökümü.",
			Color:       "#8b5cf6",
			AppPath:     "~/Applications/Mac Donanım Özellikleri.app",
			HealthURL:   "http://127.0.0.1:8899/api/health",
		},
		{
			ID:          "benchmark",
			Name:        "benchmark",
			Host:        "benchmark.localhost",
			Port:        8420,
			Pinned:      true,
			Category:    "Sistem & Araçlar",
			Badge:       "Performans Kıyaslama",
			Description: "Benchmark Hub — Yapay zeka modelleri, token hızları ve donanım performans kıyaslama dashboard'u.",
			Color:       "#f59e0b",
			AppPath:     "/Applications/Benchmark Hub.app",
			HealthURL:   "http://127.0.0.1:8420/api/status",
		},
		{
			ID:          "supertoto",
			Name:        "supertoto",
			Host:        "supertoto.localhost",
			Port:        4577,
			Pinned:      true,
			Category:    "Analiz & Bot",
			Badge:       "Tahmin & Simülasyon",
			Description: "SüperToto Dev — Next.js tabanlı bülten takip, formül filtreleme, kupon oluşturma ve maç simülasyon motoru.",
			Color:       "#ec4899",
			AppPath:     "~/Applications/SüperToto.app",
			HealthURL:   "http://127.0.0.1:4577",
		},
		{
			ID:          "fxpro",
			Name:        "fxpro",
			Host:        "fxpro.localhost",
			Port:        8787,
			Pinned:      true,
			Category:    "Analiz & Bot",
			Badge:       "Piyasa Tarayıcı",
			Description: "FxPro Dashboard — Otomatik trading botu kontrol paneli, tarayıcı servisleri ve canlı veri akışı.",
			Color:       "#06b6d4",
			AppPath:     "~/Applications/FxPro Dashboard.app",
			HealthURL:   "http://127.0.0.1:8787/api/status",
		},
		{
			ID:          "calisma_plani",
			Name:        "calisma-plani",
			Host:        "calisma-plani.localhost",
			Port:        1420,
			Pinned:      true,
			Category:    "Okul & Eğitim",
			Badge:       "Zaman Yönetimi",
			Description: "Haftalık Çalışma Planı — Tauri tabanlı yerel masaüstü haftalık ders, nöbet ve etkinlik takip uygulaması.",
			Color:       "#6366f1",
			AppPath:     "/Applications/Haftalık Çalışma Planı.app",
			HealthURL:   "http://127.0.0.1:1420",
		},
		{
			ID:          "hf_advisor",
			Name:        "hf-advisor",
			Host:        "hf-advisor.localhost",
			Port:        8000,
			Pinned:      true,
			Category:    "Yapay Zeka",
			Badge:       "AI Advisor",
			Description: "HF Model Danışmanı — Hugging Face modelleri ve Mac M3 donanımına uygun açık kaynak model öneri asistanı.",
			Color:       "#d946ef",
			AppPath:     "/Applications/HF Model Danışmanı.app",
			HealthURL:   "http://127.0.0.1:8000",
		},
		{
			ID:          "ozelders",
			Name:        "ozelders",
			Host:        "ozelders.localhost",
			Port:        0,
			Pinned:      true,
			Category:    "Okul & Eğitim",
			Badge:       "Bulut Kütüphane",
			Description: "Özel Ders Portalı — Bulut tabanlı özel ders materyalleri, interaktif çalışma fasikülleri ve online arşiv.",
			Color:       "#14b8a6",
			AppPath:     "/Applications/Özel Ders Portalı.app",
			HealthURL:   "",
		},
		{
			ID:          "icloud_google",
			Name:        "icloud-google",
			Host:        "icloud-google.localhost",
			Port:        8910,
			Pinned:      true,
			Category:    "Depolama & Bulut",
			Badge:       "20TB Ultra Aktarımı",
			Description: "iCloud2Google Migration Hub — 2TB iCloud Aile Planı verilerini Google Ultra 20TB depolamaya telefon-bilgisayar senkron ve Wi-Fi korumalı taşıma merkezi.",
			Color:       "#0ea5e9",
			AppPath:     "~/icloud-to-google-hub/start.sh",
			HealthURL:   "http://127.0.0.1:8910/api/status",
		},
	}
}

// LoadPins parses a TOML-formatted pins configuration file at path using
// only standard library components and returns a list of pinned model.Service.
func LoadPins(path string) ([]model.Service, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("empty pins path")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open pins config %s: %w", path, err)
	}
	defer f.Close()

	return ParsePins(f)
}

// ParsePins parses TOML pin definitions from an io.Reader.
// Supports both `[[pins]]` array-of-tables and `[pins.<id>]` / `[<id>]` sections.
func ParsePins(r io.Reader) ([]model.Service, error) {
	scanner := bufio.NewScanner(r)
	var svcs []model.Service
	var cur *model.Service

	flush := func() {
		if cur == nil {
			return
		}
		if cur.ID == "" && cur.Name != "" {
			cur.ID = cur.Name
		}
		if cur.Name == "" && cur.ID != "" {
			cur.Name = cur.ID
		}
		if cur.Host == "" && cur.Name != "" {
			cur.Host = cur.Name + ".localhost"
		}
		cur.Pinned = true
		if cur.ID != "" || cur.Name != "" || cur.Port > 0 {
			svcs = append(svcs, *cur)
		}
		cur = nil
	}

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())
		line = stripComment(line)
		if line == "" {
			continue
		}

		// Array-of-tables: [[pins]] or [[pin]]
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			flush()
			cur = &model.Service{Pinned: true}
			continue
		}

		// Table: [pins.name] or [name]
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			flush()
			sec := strings.TrimSpace(line[1 : len(line)-1])
			sec = strings.TrimPrefix(sec, "pins.")
			sec = strings.TrimPrefix(sec, "pin.")
			cur = &model.Service{
				ID:     sec,
				Pinned: true,
			}
			continue
		}

		// Key = Value
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		val = unquote(val)

		if cur == nil {
			cur = &model.Service{Pinned: true}
		}

		switch key {
		case "id":
			cur.ID = val
		case "name", "slug":
			cur.Name = val
		case "host", "hostname":
			cur.Host = val
		case "port":
			p, err := strconv.Atoi(val)
			if err == nil {
				cur.Port = p
			}
		case "category":
			cur.Category = val
		case "badge":
			cur.Badge = val
		case "description", "desc":
			cur.Description = val
		case "color":
			cur.Color = val
		case "app_path", "apppath":
			cur.AppPath = val
		case "health_url", "healthurl", "health_endpoint":
			cur.HealthURL = val
		case "pinned":
			cur.Pinned = (val == "true" || val == "1")
		case "running":
			cur.Running = (val == "true" || val == "1")
		}
	}
	flush()

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading pins config: %w", err)
	}
	return svcs, nil
}

// stripComment strips trailing # comments, respecting single and double quotes.
func stripComment(s string) string {
	inQuote := false
	var quoteChar rune
	escaped := false

	for i, r := range s {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if (r == '"' || r == '\'') && !inQuote {
			inQuote = true
			quoteChar = r
			continue
		}
		if inQuote && r == quoteChar {
			inQuote = false
			continue
		}
		if !inQuote && r == '#' {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

// unquote strips matching surrounding single or double quotes and unescapes double quotes.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			if s[0] == '"' {
				if unq, err := strconv.Unquote(s); err == nil {
					return unq
				}
			}
			return s[1 : len(s)-1]
		}
	}
	return s
}
