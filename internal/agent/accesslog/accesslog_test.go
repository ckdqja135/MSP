package accesslog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const combined = `203.0.113.7 - alice [06/Nov/2026:21:35:00 +0900] "GET /api/posts?token=abc123&page=2 HTTP/1.1" 200 1534 "https://ref.example.test/" "Mozilla/5.0 secret-agent"`

func TestParseCombinedKeepsOnlyWhatIsNeeded(t *testing.T) {
	request, ok := Parse(combined, "nginx")
	if !ok {
		t.Fatal("combined 형식을 읽지 못했다")
	}
	if !request.At.Equal(time.Date(2026, 11, 6, 12, 35, 0, 0, time.UTC)) {
		t.Errorf("시각 = %v", request.At)
	}
	if request.Method != "GET" || request.Path != "/api/posts" || request.Proto != "HTTP/1.1" || request.Status != 200 || request.Service != "nginx" {
		t.Errorf("요청 = %+v", request)
	}
	if request.Bytes == nil || *request.Bytes != 1534 {
		t.Errorf("크기 = %v", request.Bytes)
	}
	for _, value := range []*int64{request.DurationMS, request.UpstreamMS} {
		if value != nil {
			t.Errorf("로그에 없는 소요 시간을 만들었다: %d", *value)
		}
	}
	if request.Upstream != "" {
		t.Errorf("로그에 없는 전달 대상을 만들었다: %s", request.Upstream)
	}
	text := strings.Join([]string{request.Method, request.Path, request.Proto, request.Upstream, request.Service}, " ")
	for _, secret := range []string{"203.0.113.7", "alice", "token", "abc123", "ref.example", "Mozilla", "secret-agent"} {
		if strings.Contains(text, secret) {
			t.Errorf("버려야 할 값 %q가 남았다: %s", secret, text)
		}
	}
}

func TestParseExtrasFromLogFormat(t *testing.T) {
	request, ok := Parse(combined+` rt=0.052 urt="0.048" ua="172.20.0.3:8080"`, "nginx")
	if !ok {
		t.Fatal("덧붙인 값이 있는 형식을 읽지 못했다")
	}
	if request.DurationMS == nil || *request.DurationMS != 52 {
		t.Errorf("소요 = %v", request.DurationMS)
	}
	if request.UpstreamMS == nil || *request.UpstreamMS != 48 {
		t.Errorf("전달 소요 = %v", request.UpstreamMS)
	}
	if request.Upstream != "172.20.0.3:8080" {
		t.Errorf("전달 대상 = %q", request.Upstream)
	}
}

func TestParseRetriedUpstreamUsesLastOne(t *testing.T) {
	request, ok := Parse(combined+` rt=0.5 urt="0.3, 0.1" ua="172.20.0.3:8080, 172.20.0.4:8080"`, "nginx")
	if !ok {
		t.Fatal("재시도 형식을 읽지 못했다")
	}
	if request.Upstream != "172.20.0.4:8080" {
		t.Errorf("전달 대상 = %q", request.Upstream)
	}
	if request.UpstreamMS == nil || *request.UpstreamMS != 100 {
		t.Errorf("전달 소요 = %v", request.UpstreamMS)
	}
}

func TestParseInternalRedirectUsesLastUpstream(t *testing.T) {
	request, ok := Parse(combined+` rt=0.2 urt="0.05 : 0.12" ua="127.0.0.1:8080 : [::1]:9000"`, "nginx")
	if !ok {
		t.Fatal("내부 이동 형식을 읽지 못했다")
	}
	if request.Upstream != "[::1]:9000" {
		t.Errorf("전달 대상 = %q", request.Upstream)
	}
	if request.UpstreamMS == nil || *request.UpstreamMS != 120 {
		t.Errorf("전달 소요 = %v", request.UpstreamMS)
	}
}

func TestParseUnknownUpstreamStaysEmpty(t *testing.T) {
	request, ok := Parse(combined+` rt=0.001 urt="-" ua="-"`, "nginx")
	if !ok {
		t.Fatal("읽지 못했다")
	}
	if request.Upstream != "" || request.UpstreamMS != nil {
		t.Errorf("없는 값을 만들었다: %+v", request)
	}
}

func TestParseMissingBytes(t *testing.T) {
	request, ok := Parse(`1.2.3.4 - - [06/Nov/2026:21:35:00 +0900] "HEAD / HTTP/1.1" 304 - "-" "-"`, "nginx")
	if !ok || request.Bytes != nil {
		t.Errorf("크기가 없는 줄 = %+v, %v", request, ok)
	}
}

func TestParseRejectsOtherLines(t *testing.T) {
	for _, line := range []string{
		"",
		"plain text",
		`1.2.3.4 - - [not a time] "GET / HTTP/1.1" 200 1`,
		`1.2.3.4 - - [06/Nov/2026:21:35:00 +0900] "\x16\x03\x01" 400 157 "-" "-"`,
	} {
		if request, ok := Parse(line, "nginx"); ok {
			t.Errorf("%q를 요청으로 읽었다: %+v", line, request)
		}
	}
}

func TestCleanPath(t *testing.T) {
	for raw, want := range map[string]string{
		"/a/b?x=1":                 "/a/b",
		"/a#frag":                  "/a",
		"http://evil.test/a?x=1":   "/a",
		"http://evil.test":         "/",
		"/reset?token=abc":         "/reset",
		"/login/token=abc123/next": "/login/token=[가려짐]",
		"/a\x1b[31mb":              "/a[31mb",
	} {
		if got := cleanPath(raw); got != want {
			t.Errorf("cleanPath(%q) = %q, want %q", raw, got, want)
		}
	}
	long := cleanPath("/" + strings.Repeat("가", 200))
	if !strings.HasSuffix(long, "…") || !strings.HasPrefix(long, "/가") || strings.ContainsRune(long, '�') {
		t.Errorf("긴 경로를 글자 중간에서 자르면 안 된다: %q", long)
	}
}

func writeLog(t *testing.T, path, text string, flag int) {
	t.Helper()
	file, err := os.OpenFile(path, flag, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(text); err != nil {
		t.Fatal(err)
	}
}

func TestTailerSkipsHistoryAndFollows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	writeLog(t, path, "옛 줄\n", os.O_CREATE|os.O_WRONLY)
	tailer := NewTailer(path)
	if lines, err := tailer.Next(); err != nil || len(lines) != 0 {
		t.Fatalf("지나간 내용을 읽었다: %v, %v", lines, err)
	}
	writeLog(t, path, "하나\n둘\n반쪽", os.O_APPEND|os.O_WRONLY)
	lines, err := tailer.Next()
	if err != nil || strings.Join(lines, ",") != "하나,둘" {
		t.Fatalf("lines = %v, %v", lines, err)
	}
	writeLog(t, path, " 줄\n", os.O_APPEND|os.O_WRONLY)
	lines, err = tailer.Next()
	if err != nil || strings.Join(lines, ",") != "반쪽 줄" {
		t.Fatalf("끊긴 줄을 잇지 못했다: %v, %v", lines, err)
	}
}

func TestTailerHandlesRotationAndTruncate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	writeLog(t, path, "처음 내용이 충분히 길다\n", os.O_CREATE|os.O_WRONLY)
	tailer := NewTailer(path)
	tailer.Next()

	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if lines, err := tailer.Next(); err != nil || len(lines) != 0 {
		t.Fatalf("회전 중 파일이 없는 것을 오류로 봤다: %v, %v", lines, err)
	}
	writeLog(t, path, "새 파일\n", os.O_CREATE|os.O_WRONLY)
	lines, err := tailer.Next()
	if err != nil || strings.Join(lines, ",") != "새 파일" {
		t.Fatalf("회전 후 = %v, %v", lines, err)
	}

	if err := os.Truncate(path, 0); err != nil {
		t.Fatal(err)
	}
	if lines, err := tailer.Next(); err != nil || len(lines) != 0 {
		t.Fatalf("잘린 직후 = %v, %v", lines, err)
	}
	writeLog(t, path, "잘린 뒤\n", os.O_APPEND|os.O_WRONLY)
	lines, err = tailer.Next()
	if err != nil || strings.Join(lines, ",") != "잘린 뒤" {
		t.Fatalf("잘린 뒤 = %v, %v", lines, err)
	}
}

func TestTailerReportsMissingFileAtStart(t *testing.T) {
	tailer := NewTailer(filepath.Join(t.TempDir(), "none.log"))
	if _, err := tailer.Next(); err == nil {
		t.Error("처음부터 없는 파일은 알려야 한다")
	}
}

func TestTailerLimitsOneRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	writeLog(t, path, "", os.O_CREATE|os.O_WRONLY)
	tailer := NewTailer(path)
	tailer.Next()
	line := strings.Repeat("x", 999) + "\n"
	writeLog(t, path, strings.Repeat(line, 3000), os.O_APPEND|os.O_WRONLY)
	first, err := tailer.Next()
	if err != nil {
		t.Fatal(err)
	}
	if total := len(first) * len(line); total > maxReadBytes {
		t.Errorf("한 번에 %d바이트를 읽었다", total)
	}
	second, _ := tailer.Next()
	third, _ := tailer.Next()
	if len(first)+len(second)+len(third) != 3000 {
		t.Errorf("읽은 줄 = %d", len(first)+len(second)+len(third))
	}
}
