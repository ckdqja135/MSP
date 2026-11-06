package central

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	// 일시적인 지연을 장애로 보지 않기 위한 연속 횟수다.
	probeDownThreshold    = 3
	probeRecoverThreshold = 3
)

const (
	probeOK      = "ok"
	probeFailing = "failing"
	probeDown    = "down"
)

type probeState struct {
	state     string
	failures  int
	successes int
}

// nextProbeState는 응답 결과를 반영한 새 상태와 흐름표에 남길 기록을 돌려준다.
// 실패할 때마다 행을 만들지 않고 상태가 바뀌는 순간만 기록한다.
func nextProbeState(prev probeState, p protocol.Probe) (probeState, *protocol.Event) {
	next := prev
	if next.state == "" {
		next.state = probeOK
	}

	event := protocol.Event{
		OccurredAt: p.At,
		Category:   protocol.CategoryProbe,
		Source:     "probe:" + string(p.Vantage),
		Target:     p.ServiceKey,
		ServiceKey: p.ServiceKey,
		DurationMS: p.LatencyMS,
		Evidence:   "probe",
	}

	if p.OK {
		next.failures = 0
		next.successes++
		switch {
		case prev.state == probeFailing:
			next.state = probeOK
			event.Type = "probe.ok"
			event.Severity = protocol.SeverityInfo
			event.Result = "응답"
			event.Summary = p.Target + " 응답 (일시 실패 후)"
			return next, &event
		case prev.state == probeDown && next.successes >= probeRecoverThreshold:
			next.state = probeOK
			event.Type = "probe.recovered"
			event.Severity = protocol.SeverityInfo
			event.Result = "복구"
			event.Summary = fmt.Sprintf("%s %d회 연속 응답, 복구", p.Target, probeRecoverThreshold)
			return next, &event
		}
		return next, nil
	}

	next.successes = 0
	next.failures++
	reason := failureText(p.Failure)
	if p.Failure == "timeout" && p.TimeoutMS > 0 {
		limit := p.TimeoutMS
		event.DurationLimitMS = &limit
	}
	switch {
	case prev.state == probeOK || prev.state == "":
		next.state = probeFailing
		event.Type = "probe.failed"
		event.Severity = protocol.SeverityWarning
		event.Result = fmt.Sprintf("실패 1/%d", probeDownThreshold)
		event.Summary = fmt.Sprintf("%s 실패: %s", p.Target, reason)
		return next, &event
	case prev.state == probeFailing && next.failures >= probeDownThreshold:
		next.state = probeDown
		event.Type = "probe.down"
		event.Severity = protocol.SeverityCritical
		event.Result = fmt.Sprintf("실패 %d/%d", next.failures, probeDownThreshold)
		event.Summary = fmt.Sprintf("%s %d회 연속 실패: %s", p.Target, next.failures, reason)
		return next, &event
	}
	return next, nil
}

func failureText(failure string) string {
	switch failure {
	case "timeout":
		return "제한 시간 초과"
	case "refused":
		return "연결 거부"
	case "status":
		return "비정상 상태 코드"
	case "":
		return "원인 미상"
	}
	return failure
}

func applyProbe(ctx context.Context, tx *sql.Tx, serverID string, p protocol.Probe, receivedAt time.Time) error {
	var prev probeState
	err := tx.QueryRowContext(ctx, `
		SELECT state, consecutive_failures, consecutive_successes FROM probe_states
		WHERE server_id = ? AND service_key = ? AND vantage = ?`, serverID, p.ServiceKey, string(p.Vantage)).
		Scan(&prev.state, &prev.failures, &prev.successes)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	next, event := nextProbeState(prev, p)
	if event != nil {
		if err := insertEvent(ctx, tx, serverID, *event, receivedAt); err != nil {
			return err
		}
	}
	if p.LatencyMS != nil {
		sample := protocol.Sample{At: p.At, Scope: p.ServiceKey, Name: "probe." + string(p.Vantage) + ".latency_ms", Value: float64(*p.LatencyMS)}
		if err := insertSample(ctx, tx, serverID, sample); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO probe_states (server_id, service_key, vantage, target, state, consecutive_failures,
			consecutive_successes, last_checked, last_latency_ms, last_failure)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (server_id, service_key, vantage) DO UPDATE SET
			target = excluded.target, state = excluded.state,
			consecutive_failures = excluded.consecutive_failures,
			consecutive_successes = excluded.consecutive_successes,
			last_checked = excluded.last_checked, last_latency_ms = excluded.last_latency_ms,
			last_failure = excluded.last_failure`,
		serverID, p.ServiceKey, string(p.Vantage), p.Target, next.state, next.failures, next.successes,
		p.At.UnixMilli(), nullable(p.LatencyMS), p.Failure)
	return err
}
