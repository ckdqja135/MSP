package mask

import (
	"strings"
	"testing"
)

func TestLineHidesSecrets(t *testing.T) {
	cases := []struct{ name, input, secret string }{
		{"authorization 헤더", "Authorization: Basic dXNlcjpwYXNz", "dXNlcjpwYXNz"},
		{"bearer 토큰", "calling api with Bearer abc.def-123", "abc.def-123"},
		{"cookie 헤더", `cookie=session=s3cr3tvalue; other=1`, "s3cr3tvalue"},
		{"JSON 비밀번호", `{"user":"kim","password":"hunter2"}`, "hunter2"},
		{"쿼리 토큰", "GET /callback?code=1&access_token=tok123&x=1", "tok123"},
		{"환경 변수 형식", "DB_PASSWORD=p4ss failed to connect", "p4ss"},
		{"API 키", "api_key: 0123456789", "0123456789"},
		{"접속 URL 비밀번호", "connect postgres://app:dbpass@db:5432/blog", "dbpass"},
		{"JWT", "user eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjMifQ.abcdefgh logged in", "eyJhbGci"},
		{"AWS 키", "using AKIAIOSFODNN7EXAMPLE", "AKIAIOSFODNN7EXAMPLE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Line(tc.input)
			if strings.Contains(got, tc.secret) {
				t.Errorf("비밀값이 남아 있다: %q", got)
			}
			if !strings.Contains(got, redacted) {
				t.Errorf("가림 표시가 없다: %q", got)
			}
		})
	}
}

func TestLineKeepsOrdinaryText(t *testing.T) {
	cases := []string{
		"GET /health 200 12ms",
		"container started on port 8080",
		"connect postgres://db:5432/blog",
		"password reset page rendered",
		"2026-11-06T01:00:00Z ERROR out of memory",
	}
	for _, line := range cases {
		if got := Line(line); got != line {
			t.Errorf("Line(%q) = %q", line, got)
		}
	}
}
