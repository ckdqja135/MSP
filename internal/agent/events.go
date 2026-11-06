package agent

import (
	"fmt"
	"strings"
	"time"

	"github.com/ckdqja135/MSP/internal/agent/dockerapi"
	"github.com/ckdqja135/MSP/internal/protocol"
)

// 흐름표에 남길 Docker 이벤트만 고른다. exec_* 같은 이벤트는 헬스체크마다 발생해 기록을 덮는다.
var containerActions = map[string]bool{
	"create": true, "start": true, "restart": true, "stop": true, "kill": true, "die": true,
	"oom": true, "destroy": true, "pause": true, "unpause": true, "rename": true, "health_status": true,
}

var networkActions = map[string]bool{"connect": true, "disconnect": true}

func shortContainerID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// convertEvent는 Docker 이벤트를 흐름표 행으로 바꾼다. services는 컨테이너 ID로 서비스 키를 찾는 표다.
func convertEvent(e dockerapi.Event, services map[string]string) (protocol.Event, bool) {
	action, detail, _ := strings.Cut(e.Action, ": ")
	out := protocol.Event{
		OccurredAt: e.At,
		Category:   protocol.CategoryOps,
		Severity:   protocol.SeverityInfo,
		Source:     "docker",
		Evidence:   "docker-event",
	}

	switch e.Type {
	case "container":
		if !containerActions[action] {
			return out, false
		}
		out.Type = "container." + action
		out.Target = e.Name
		out.ContainerID = e.ActorID
		if e.Service != "" {
			out.ServiceKey = protocol.ServiceKey(e.Project, e.Service)
		} else if e.Name != "" {
			out.ServiceKey = protocol.ServiceKey("", e.Name)
		}
	case "network":
		if !networkActions[action] {
			return out, false
		}
		out.Type = "network." + action
		out.Target = e.Name
		out.ContainerID = e.ContainerID
		out.ServiceKey = services[e.ContainerID]
	default:
		return out, false
	}

	switch out.Type {
	case "container.create":
		out.Summary, out.Result = "컨테이너 생성", "생성"
	case "container.start":
		out.Summary, out.Result = "컨테이너 시작", "시작"
	case "container.restart":
		out.Summary, out.Result, out.Severity = "컨테이너 재시작", "재시작", protocol.SeverityWarning
	case "container.stop":
		out.Summary, out.Result = "컨테이너 중지", "중지"
	case "container.kill":
		out.Summary, out.Result = "컨테이너에 신호 전송", "신호 "+e.Signal
	case "container.die":
		out.Summary = "컨테이너 종료, exit " + e.ExitCode
		out.Result = "exit " + e.ExitCode
		if e.ExitCode != "0" && e.ExitCode != "" {
			out.Severity = protocol.SeverityCritical
		}
	case "container.oom":
		out.Summary, out.Result, out.Severity = "메모리 부족(OOM) 기록", "OOM", protocol.SeverityCritical
	case "container.destroy":
		out.Summary, out.Result = "컨테이너 삭제", "삭제"
	case "container.pause":
		out.Summary, out.Result = "컨테이너 일시 정지", "일시 정지"
	case "container.unpause":
		out.Summary, out.Result = "컨테이너 정지 해제", "정지 해제"
	case "container.rename":
		out.Summary, out.Result = "컨테이너 이름 변경", "변경"
	case "container.health":
		out.Type = "container.health"
	case "container.health_status":
		out.Type = "container.health"
		out.Summary, out.Result = "헬스체크 상태 "+detail, detail
		if detail == "unhealthy" {
			out.Severity = protocol.SeverityWarning
		}
	case "network.connect":
		out.Summary, out.Result = fmt.Sprintf("컨테이너 %s를 네트워크에 연결", shortContainerID(e.ContainerID)), "연결"
	case "network.disconnect":
		out.Summary, out.Result = fmt.Sprintf("컨테이너 %s를 네트워크에서 해제", shortContainerID(e.ContainerID)), "해제"
	}
	return out, true
}

const (
	limiterWindow = 10 * time.Second
	limiterBurst  = 5
)

type limiterBucket struct {
	started  time.Time
	admitted int
	dropped  int
	last     protocol.Event
}

// eventLimiter는 이벤트가 급증할 때 같은 종류의 행을 하나로 묶는다.
// 버리는 것이 아니라 건수를 남겨 누락이 드러나게 한다.
type eventLimiter struct {
	buckets map[string]*limiterBucket
}

func newEventLimiter() *eventLimiter {
	return &eventLimiter{buckets: map[string]*limiterBucket{}}
}

// admit이 false를 돌려주면 이 행은 집계에 포함된 것이다.
func (l *eventLimiter) admit(e protocol.Event, now time.Time) bool {
	key := e.ServiceKey + "|" + e.Type
	bucket := l.buckets[key]
	if bucket == nil || now.Sub(bucket.started) >= limiterWindow {
		if bucket != nil && bucket.dropped > 0 {
			return l.overflow(key, bucket, e, now)
		}
		l.buckets[key] = &limiterBucket{started: now, admitted: 1}
		return true
	}
	if bucket.admitted < limiterBurst {
		bucket.admitted++
		return true
	}
	bucket.dropped++
	bucket.last = e
	return false
}

// overflow는 끝난 구간의 집계를 남겨 둔 채 새 구간을 시작한다. 집계 행은 drain에서 나간다.
func (l *eventLimiter) overflow(key string, old *limiterBucket, e protocol.Event, now time.Time) bool {
	l.buckets[key+"|"+old.started.String()] = old
	l.buckets[key] = &limiterBucket{started: now, admitted: 1}
	return true
}

// drain은 구간이 끝난 집계를 행으로 돌려준다.
func (l *eventLimiter) drain(now time.Time) []protocol.Event {
	var rows []protocol.Event
	for key, bucket := range l.buckets {
		if now.Sub(bucket.started) < limiterWindow {
			continue
		}
		if bucket.dropped > 0 {
			row := bucket.last
			row.Aggregated = bucket.dropped
			row.Summary = fmt.Sprintf("%s (급증으로 묶음)", row.Summary)
			rows = append(rows, row)
		}
		delete(l.buckets, key)
	}
	return rows
}
