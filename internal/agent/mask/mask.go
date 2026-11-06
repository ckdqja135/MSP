// Package mask는 로그가 대상 서버를 벗어나기 전에 민감정보를 가린다.
//
// 패턴 기반이라 모든 비밀값을 잡지는 못한다. 로그를 장기 보관할 때는 이 한계를 전제로 한다.
package mask

import "regexp"

const redacted = "[가려짐]"

var rules = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`(?i)\b(authorization|proxy-authorization|cookie|set-cookie)(["']?\s*[:=]\s*).*`), "${1}${2}" + redacted},
	{regexp.MustCompile(`(?i)\bbearer\s+[a-z0-9._~+/=-]+`), "Bearer " + redacted},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]+`), redacted},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), redacted},
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+:)[^\s@/]+(@)`), "${1}" + redacted + "${2}"},
	{regexp.MustCompile(`(?i)([a-z0-9_.-]*(?:password|passwd|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)[a-z0-9_.-]*["']?\s*[:=]\s*["']?)[^\s&"',;}]+`), "${1}" + redacted},
}

// Line은 한 줄의 로그에서 민감정보로 보이는 값을 가린다.
func Line(line string) string {
	for _, rule := range rules {
		line = rule.pattern.ReplaceAllString(line, rule.replace)
	}
	return line
}
