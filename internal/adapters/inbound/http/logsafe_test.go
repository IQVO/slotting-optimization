package http

import "testing"

func TestSanitizeLogStripsNewlines(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"lf":     {"a\nb", "ab"},
		"cr":     {"a\rb", "ab"},
		"crlf":   {"a\r\nb", "ab"},
		"header": {"GET /x HTTP/1.1\nAuthorization: admin", "GET /x HTTP/1.1Authorization: admin"},
		"clean":  {"/api/v1/reports", "/api/v1/reports"},
		"empty":  {"", ""},
	}
	for name, tc := range cases {
		if got := sanitizeLog(tc.in); got != tc.want {
			t.Errorf("%s: sanitizeLog(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}
