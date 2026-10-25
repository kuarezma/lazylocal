package tui

import (
	"strings"
	"testing"
)

func FuzzSanitizeTerm(f *testing.F) {
	// Standard ASCII text
	f.Add("normal ascii string")
	f.Add("")
	// OSC 52 clipboard hijacking sequence
	f.Add("\x1b]52;c;Y2FsYw==\x07")
	// CSI cursor movement and clear screen
	f.Add("\x1b[2J\x1b[H")
	// SGR color escape sequences
	f.Add("\x1b[31;1mRed Text\x1b[0m")
	// C0 control characters (\x00 to \x1f)
	f.Add("\x00\x01\x02\x03\x04\x05\x06\x07\x08\r\n\t")
	// DEL character
	f.Add("Hello\x7fWorld")
	// Allowed safe whitespace
	f.Add("valid\tpath\nline2")
	// Isolated / partial escape markers
	f.Add("\x1b")
	f.Add("\x1b[")
	f.Add("\x1b]")
	// Unicode and Turkish characters
	f.Add("Hata: \x1b]52;c;... \x1b\x07 Türkçe çşğüıö ÇŞĞÜİÖ \x1b[1mKalın\x1b[0m")
	// Repeated control codes
	f.Add(strings.Repeat("\x1b[0m", 50))
	f.Add("\x00\xff\xfe\x1b\x7f\r\n")

	f.Fuzz(func(t *testing.T, input string) {
		out := sanitizeTerm(input)

		// sanitizeTerm must never panic and must strictly ensure:
		// 1. No raw ESC (\x1b)
		// 2. No raw DEL (\x7f)
		// 3. No C0 control characters (< 0x20) except allowed whitespace (\n, \t)
		for i := 0; i < len(out); i++ {
			c := out[i]
			if c == 0x1b {
				t.Fatalf("sanitizeTerm leaked raw ESC (0x1b) at byte %d: %q", i, out)
			}
			if c == 0x7f {
				t.Fatalf("sanitizeTerm leaked raw DEL (0x7f) at byte %d: %q", i, out)
			}
			if c < 0x20 && c != '\n' && c != '\t' {
				t.Fatalf("sanitizeTerm leaked C0 control character 0x%02x at byte %d: %q", c, i, out)
			}
		}
	})
}
