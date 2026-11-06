package demo

import (
	"context"
	"strings"
	"time"

	"github.com/ckdqja135/MSP/internal/agent/mask"
	"github.com/ckdqja135/MSP/internal/protocol"
)

const logEvery = 10 * time.Second

// logLineAt은 시각마다 같은 줄을 만든다. 민감정보가 섞인 줄은 실제 경로처럼 마스킹을 거친다.
func (s *Scenario) logLineAt(service string, t time.Time) (protocol.LogLine, bool) {
	running := false
	for _, c := range s.topologyAt(t).Containers {
		if c.ServiceKey() == service {
			running = c.State == "running"
		}
	}
	if !running {
		return protocol.LogLine{}, false
	}
	_, phase := s.position(t)
	stream, text := "stdout", "INFO request handled status=200"
	if service == "blog/api" {
		switch {
		case phase >= apiOOM-20*time.Second && phase < apiOOM:
			stream, text = "stderr", "FATAL out of memory: cannot allocate"
		case phase >= apiHangStart && phase < apiOOM:
			stream, text = "stderr", "ERROR request timeout after 30s path=/api/posts"
		case phase >= apiMemWarn-30*time.Second && phase < apiHangStart:
			stream, text = "stdout", "WARN heap usage is high"
		case phase >= apiRestart && phase < apiHealthy:
			stream, text = "stdout", "INFO server starting on :8080"
		}
	}
	if t.Unix()%60 == 0 {
		text = "INFO db connect postgres://app:demo-password@db:5432/app token=demo-token"
	}
	return protocol.LogLine{At: t, Stream: stream, Text: mask.Line(text)}, true
}

func (s *Scenario) logLines(service, query string, from, to time.Time) []protocol.LogLine {
	var lines []protocol.LogLine
	needle := strings.ToLower(query)
	for t := nextGrid(from, logEvery); !t.After(to); t = t.Add(logEvery) {
		line, ok := s.logLineAt(service, t)
		if ok && strings.Contains(strings.ToLower(line.Text), needle) {
			lines = append(lines, line)
		}
	}
	return lines
}

// StreamLogs는 에이전트 대신 가짜 로그를 전달한다.
func (s *Scenario) StreamLogs(ctx context.Context, _ string, request protocol.LogRequest, emit func(protocol.LogChunk) bool) error {
	now := time.Now().UTC()
	to := now
	if request.Until != nil && request.Until.Before(to) {
		to = *request.Until
	}
	from := to.Add(-time.Duration(request.Tail) * logEvery)
	if request.Since != nil {
		from = *request.Since
	}
	lines := s.logLines(request.ServiceKey, request.Query, from, to)
	if request.Tail > 0 && len(lines) > request.Tail {
		lines = lines[len(lines)-request.Tail:]
	}
	if !emit(protocol.LogChunk{Lines: lines, Done: !request.Follow}) || !request.Follow {
		return nil
	}

	ticker := time.NewTicker(logEvery)
	defer ticker.Stop()
	cursor := to
	for {
		select {
		case <-ctx.Done():
			return nil
		case current := <-ticker.C:
			current = current.UTC()
			next := s.logLines(request.ServiceKey, request.Query, cursor, current)
			cursor = current
			if len(next) > 0 && !emit(protocol.LogChunk{Lines: next}) {
				return nil
			}
		}
	}
}
