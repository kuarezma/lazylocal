package proxy

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/kuarezma/lazylocal/internal/model"
)

const syntheticSecret = "sk-proj-fuzzsecret1234567890"

func FuzzRedactSensitiveBody(f *testing.F) {
	// 1. Nested JSON
	f.Add([]byte(`{"user":{"id":1,"profile":{"name":"alice","token":"secret123"}},"active":true}`), "application/json")
	// 2. Truncated JSON
	f.Add([]byte(`{"user": "alice", "password": "supersecret123", "data": [1, 2, `), "application/json")
	// 3. Form-urlencoded
	f.Add([]byte(`username=admin&password=secretpass123&refresh_token=tok456`), "application/x-www-form-urlencoded")
	// 4. JWT
	f.Add([]byte(`eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6IkpvaG4gRG9lIiwiaWF0IjoxNTE2MjM5MDIyfQ.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c`), "text/plain")
	// 5. sk-... OpenAI key
	f.Add([]byte(`{"apiKey":"sk-abcdefghijklmnopqrstuvwxyz123456"}`), "application/json")
	// 6. ghp_... GitHub token
	f.Add([]byte(`ghp_1234567890abcdefghijklmnopqrstuvwxyz`), "text/plain")
	// 7. Bearer token
	f.Add([]byte(`Authorization: Bearer secret_access_token_12345678`), "text/plain")
	// 8. PEM private key
	f.Add([]byte("-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA0Y3fakekeyfakekey\n-----END RSA PRIVATE KEY-----"), "application/x-pem-file")
	// 9. Binary bytes
	f.Add([]byte{0x00, 0xff, 0xfe, 0x12, 0x34, 0x56, 0x78, 0x9a}, "application/octet-stream")
	// 10. Empty strings
	f.Add([]byte{}, "")
	// 11. Known synthetic secret in JSON
	f.Add([]byte(fmt.Sprintf(`{"key":"%s"}`, syntheticSecret)), "application/json")
	// 12. Truncated form
	f.Add([]byte("password=secret&"), "application/x-www-form-urlencoded")
	// 13. Basic auth
	f.Add([]byte("Basic YWxhZGRpbjpvcGVuc2VzYW1l"), "text/plain")

	f.Fuzz(func(t *testing.T, data []byte, contentType string) {
		// 1. Verify redactSensitiveBody never panics on arbitrary inputs
		out := redactSensitiveBody(data, contentType)
		_ = out

		// 2. Verify that if a known synthetic secret is injected into a JSON value,
		// the output never contains the secret.
		jsonVal := []byte(fmt.Sprintf(`{"token":"%s","data":%q}`, syntheticSecret, string(data)))
		redactedJSON := redactSensitiveBody(jsonVal, "application/json")
		if bytes.Contains(redactedJSON, []byte(syntheticSecret)) {
			t.Fatalf("redactSensitiveBody leaked synthetic secret in JSON payload: %s", string(redactedJSON))
		}

		// 3. Verify that if a known synthetic secret is injected into a form string,
		// the output never contains the secret.
		formVal := []byte(fmt.Sprintf("secret=%s&token=%s&user=test", syntheticSecret, syntheticSecret))
		redactedForm := redactSensitiveBody(formVal, "application/x-www-form-urlencoded")
		if bytes.Contains(redactedForm, []byte(syntheticSecret)) {
			t.Fatalf("redactSensitiveBody leaked synthetic secret in form string: %s", string(redactedForm))
		}
	})
}

// verifyShellQuoted ensures that an argument quoted by shellQuote has enclosing
// single quotes and that all inner single quotes are properly escaped as '"'"'
// without any unescaped single quotes.
func verifyShellQuoted(t *testing.T, quoted string) {
	t.Helper()
	if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
		t.Fatalf("shell-quoted argument missing enclosing single quotes: %q", quoted)
	}
	// Removing valid '"'"' sequences must leave exactly the 2 outer enclosing quotes
	withoutEscapes := strings.ReplaceAll(quoted, `'"'"'`, "")
	if strings.Count(withoutEscapes, "'") != 2 {
		t.Fatalf("unescaped single quote detected in argument: %q", quoted)
	}
}

func FuzzCurlCommand(f *testing.F) {
	f.Add("GET", "app.localhost", "/search", "q=test", "Authorization", "Bearer sk-1234567890123456", []byte(""))
	f.Add("POST", "shop.localhost", "/checkout", "", "Content-Type", "application/json", []byte(`{"item":"book","qty":1}`))
	f.Add("PUT", "api.localhost", "/users/42", "force=true", "X-Custom-Header", "Bob's Computer", []byte("name=O'Connor"))
	f.Add("DELETE", "admin.localhost", "/items/it's", "query='drop table'", "Cookie", "session='secret'", []byte("body with 'single' and \"double\" quotes"))
	f.Add("POST", "test.localhost", "/exec;rm -rf", "cmd=$(id)", "X-Injection", "'; rm -rf /; '", []byte("`touch /tmp/pwned`"))
	f.Add("", "", "", "", "", "", []byte{})

	f.Fuzz(func(t *testing.T, method, host, path, query, headerKey, headerVal string, body []byte) {
		headers := make(http.Header)
		if headerKey != "" {
			headers[headerKey] = []string{headerVal}
		}

		ex := model.Exchange{
			Req: model.HTTPRequestData{
				Method:  method,
				Host:    host,
				Path:    path,
				Query:   query,
				Headers: headers,
				Body:    body,
			},
		}

		cmd := CurlCommand(ex)
		if cmd == "" {
			t.Fatal("CurlCommand returned empty string")
		}

		// 1. Verify URL is properly single-quoted and escaped
		url := fmt.Sprintf("http://%s%s", ex.Req.Host, ex.Req.Path)
		if ex.Req.Query != "" {
			url += "?" + ex.Req.Query
		}
		expectedQuotedURL := shellQuote(url)
		verifyShellQuoted(t, expectedQuotedURL)
		if !strings.Contains(cmd, expectedQuotedURL) {
			t.Fatalf("cmd does not contain properly escaped URL %s", expectedQuotedURL)
		}

		// 2. Verify headers are properly single-quoted and escaped
		for k, vals := range ex.Req.Headers {
			for _, v := range vals {
				expectedQuotedHeader := shellQuote(k + ": " + v)
				verifyShellQuoted(t, expectedQuotedHeader)
				if !strings.Contains(cmd, expectedQuotedHeader) {
					t.Fatalf("cmd does not contain properly escaped header %s", expectedQuotedHeader)
				}
			}
		}

		// 3. Verify body is properly single-quoted and escaped
		if len(body) > 0 {
			expectedQuotedBody := shellQuote(string(body))
			verifyShellQuoted(t, expectedQuotedBody)
			if !strings.Contains(cmd, expectedQuotedBody) {
				t.Fatalf("cmd does not contain properly escaped body %s", expectedQuotedBody)
			}
		}
	})
}
