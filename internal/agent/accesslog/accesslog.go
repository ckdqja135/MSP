// Package accesslog는 프록시 접근 로그에서 실제 API 요청을 읽는다.
//
// 파일을 읽기만 한다. 클라이언트 주소, 사용자, 리퍼러, User-Agent, 쿼리 문자열은 읽어 들인 뒤 바로 버린다.
package accesslog

import (
	"errors"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ckdqja135/MSP/internal/agent/mask"
	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	timeLayout    = "02/Jan/2006:15:04:05 -0700"
	maxPathLength = 200
	// 한 번에 읽는 양의 상한이다. 요청이 몰려도 수집이 서버에 부담을 주지 않게 한다.
	maxReadBytes = 1 << 20
	maxLineBytes = 64 << 10
)

// combined 형식에 rt, urt, ua 값을 덧붙일 수 있다. 값이 없으면 소요 시간과 전달 대상은 비워 둔다.
var (
	linePattern   = regexp.MustCompile(`^\S+ \S+ .*? \[([^\]]+)\] "([A-Z]+) (\S+)(?: (HTTP/[\d.]+))?" (\d{3}) (\d+|-)(?: "(?:[^"\\]|\\.)*" "(?:[^"\\]|\\.)*")?(.*)$`)
	schemePattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://[^/]*`)
	extraPattern  = regexp.MustCompile(`\b(rt|urt|ua)=(?:"([^"]*)"|(\S+))`)
)

// Parse는 한 줄을 요청 하나로 바꾼다. 형식이 다르면 false를 돌려준다.
func Parse(line, service string) (protocol.Request, bool) {
	match := linePattern.FindStringSubmatch(line)
	if match == nil {
		return protocol.Request{}, false
	}
	at, err := time.Parse(timeLayout, match[1])
	if err != nil {
		return protocol.Request{}, false
	}
	status, err := strconv.Atoi(match[5])
	if err != nil {
		return protocol.Request{}, false
	}
	request := protocol.Request{
		At: at.UTC(), Service: service, Method: match[2], Path: cleanPath(match[3]), Proto: match[4], Status: status,
	}
	if size, err := strconv.ParseInt(match[6], 10, 64); err == nil {
		request.Bytes = &size
	}
	for _, extra := range extraPattern.FindAllStringSubmatch(match[7], -1) {
		value := extra[2] + extra[3]
		switch extra[1] {
		case "rt":
			request.DurationMS = seconds(value)
		case "urt":
			request.UpstreamMS = seconds(value)
		case "ua":
			request.Upstream = lastUpstream(value)
		}
	}
	return request, true
}

// cleanPath는 쿼리 문자열을 버리고 경로에 남은 비밀값을 가린다. 쿼리에는 토큰이 들어 있는 경우가 많다.
func cleanPath(raw string) string {
	path := schemePattern.ReplaceAllString(raw, "")
	if index := strings.IndexAny(path, "?#"); index >= 0 {
		path = path[:index]
	}
	path = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, mask.Line(path))
	if len(path) > maxPathLength {
		path = strings.ToValidUTF8(path[:maxPathLength], "") + "…"
	}
	if path == "" {
		return "/"
	}
	return path
}

func seconds(value string) *int64 {
	// nginx는 재시도한 경우 값을 쉼표로 이어 적는다. 실제로 응답한 마지막 값을 쓴다.
	value = lastUpstream(value)
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil || parsed < 0 {
		return nil
	}
	ms := int64(parsed*1000 + 0.5)
	return &ms
}

func lastUpstream(value string) string {
	parts := strings.Split(value, ",")
	last := strings.TrimSpace(parts[len(parts)-1])
	if last == "-" {
		return ""
	}
	return last
}

// Tailer는 파일 끝을 따라 읽는다. 로그 회전과 잘림을 감지하면 처음부터 다시 읽는다.
type Tailer struct {
	path    string
	info    os.FileInfo
	offset  int64
	partial []byte
	started bool
}

func NewTailer(path string) *Tailer {
	return &Tailer{path: path}
}

// Next는 지난번 뒤로 추가된 완전한 줄을 돌려준다. 처음 호출에서는 지나간 내용을 읽지 않고 끝으로 건너뛴다.
func (t *Tailer) Next() ([]string, error) {
	info, err := os.Stat(t.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && t.started {
			// 회전 중에는 파일이 잠깐 없을 수 있다.
			return nil, nil
		}
		return nil, err
	}
	if !t.started {
		t.started, t.info, t.offset = true, info, info.Size()
		return nil, nil
	}
	if !os.SameFile(t.info, info) || info.Size() < t.offset {
		t.offset, t.partial = 0, nil
	}
	t.info = info
	if info.Size() == t.offset {
		return nil, nil
	}

	file, err := os.Open(t.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	size := info.Size() - t.offset
	if size > maxReadBytes {
		size = maxReadBytes
	}
	buffer := make([]byte, size)
	read, err := file.ReadAt(buffer, t.offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	t.offset += int64(read)

	body := string(append(t.partial, buffer[:read]...))
	t.partial = nil
	lines := strings.Split(body, "\n")
	last := lines[len(lines)-1]
	lines = lines[:len(lines)-1]
	if len(last) <= maxLineBytes {
		t.partial = []byte(last)
	}
	return lines, nil
}
