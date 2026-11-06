package central

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const alertSchema = `
CREATE TABLE IF NOT EXISTS alerts (
  id INTEGER PRIMARY KEY,
  server_id TEXT NOT NULL,
  dedup_key TEXT NOT NULL,
  rule TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  severity TEXT NOT NULL,
  state TEXT NOT NULL,
  title TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  pending_since INTEGER,
  clear_since INTEGER,
  fired_at INTEGER,
  resolved_at INTEGER,
  last_notified_at INTEGER,
  notify_count INTEGER NOT NULL DEFAULT 0,
  transitions TEXT NOT NULL DEFAULT '[]',
  UNIQUE (server_id, dedup_key)
);
CREATE TABLE IF NOT EXISTS notification_deliveries (
  id INTEGER PRIMARY KEY,
  alert_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  at INTEGER NOT NULL,
  ok INTEGER NOT NULL,
  error TEXT NOT NULL DEFAULT ''
);
`

const (
	alertIdle     = "idle"
	alertPending  = "pending"
	alertFiring   = "firing"
	alertFlapping = "flapping"
	alertResolved = "resolved"
)

const (
	notifyFired    = "fired"
	notifyResolved = "resolved"
	notifyReminder = "reminder"
	notifyFlapping = "flapping"
)

const (
	firstReminderAfter = time.Hour
	nextReminderAfter  = 4 * time.Hour
	flapWindow         = 30 * time.Minute
	flapTransitions    = 4
	// 시작 직후에는 에이전트가 재연결할 시간을 준다. 중앙 서버 재시작을 서버 장애로 오판하지 않기 위해서다.
	startupGrace = 90 * time.Second
	// 이보다 늦게 도착한 기록으로는 알림을 보내지 않는다. 지나간 일을 지금 일처럼 알리지 않기 위해서다.
	lateRecordAfter = 2 * time.Minute
)

// AlertCondition은 평가 시점의 판단 결과다.
type AlertCondition struct {
	Key      string
	Rule     string
	Target   string
	Severity protocol.Severity
	Title    string
	Detail   string
	Active   bool
	HoldFor  time.Duration
	ClearFor time.Duration
}

// Alert는 규칙과 대상별 현재 상태다. 같은 키는 한 행으로 유지해 중복을 만들지 않는다.
type Alert struct {
	ID             int64             `json:"id"`
	ServerID       string            `json:"server_id"`
	Key            string            `json:"-"`
	Rule           string            `json:"rule"`
	Target         string            `json:"target"`
	Severity       protocol.Severity `json:"severity"`
	State          string            `json:"state"`
	Title          string            `json:"title"`
	Detail         string            `json:"detail"`
	PendingSince   *time.Time        `json:"-"`
	ClearSince     *time.Time        `json:"-"`
	FiredAt        *time.Time        `json:"fired_at"`
	ResolvedAt     *time.Time        `json:"resolved_at"`
	LastNotifiedAt *time.Time        `json:"last_notified_at"`
	NotifyCount    int               `json:"notify_count"`
	Transitions    []time.Time       `json:"-"`
}

func recentTransitions(list []time.Time, now time.Time) []time.Time {
	kept := []time.Time{}
	for _, at := range list {
		if now.Sub(at) <= flapWindow {
			kept = append(kept, at)
		}
	}
	return kept
}

func sameMoment(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func alertChanged(before, after Alert) bool {
	return before.State != after.State || before.Title != after.Title || before.Detail != after.Detail ||
		before.Severity != after.Severity || before.NotifyCount != after.NotifyCount ||
		!sameMoment(before.PendingSince, after.PendingSince) || !sameMoment(before.ClearSince, after.ClearSince) ||
		!sameMoment(before.FiredAt, after.FiredAt) || !sameMoment(before.ResolvedAt, after.ResolvedAt) ||
		!sameMoment(before.LastNotifiedAt, after.LastNotifiedAt) ||
		!slices.EqualFunc(before.Transitions, after.Transitions, time.Time.Equal)
}

// stepAlert는 한 번의 평가를 반영하고 보낼 알림 종류를 돌려준다. 시간을 인자로 받아 시험할 수 있다.
func stepAlert(alert Alert, condition AlertCondition, now time.Time) (Alert, string) {
	next := alert
	// 조건이 활성일 때만 내용을 갱신한다. 복구 알림이 무엇이 복구됐는지 말할 수 있어야 한다.
	if condition.Active {
		next.Title, next.Detail, next.Severity = condition.Title, condition.Detail, condition.Severity
	}
	next.Transitions = recentTransitions(alert.Transitions, now)
	moment := now

	switch alert.State {
	case alertFiring, alertFlapping:
		if condition.Active {
			next.ClearSince = nil
			if alert.State == alertFlapping {
				return next, ""
			}
			wait := firstReminderAfter
			if alert.NotifyCount > 1 {
				wait = nextReminderAfter
			}
			if alert.LastNotifiedAt != nil && now.Sub(*alert.LastNotifiedAt) >= wait {
				return next, notifyReminder
			}
			return next, ""
		}
		if next.ClearSince == nil {
			next.ClearSince = &moment
		}
		wait := condition.ClearFor
		// 반복 중에는 충분히 안정된 뒤에만 해결로 본다.
		if alert.State == alertFlapping {
			wait = flapWindow
		}
		if now.Sub(*next.ClearSince) < wait {
			return next, ""
		}
		next.State = alertResolved
		next.ResolvedAt = &moment
		next.PendingSince, next.ClearSince = nil, nil
		next.Transitions = append(next.Transitions, now)
		return next, notifyResolved
	default:
		if !condition.Active {
			next.PendingSince = nil
			if alert.State == alertPending {
				next.State = alertIdle
			}
			return next, ""
		}
		if next.PendingSince == nil {
			next.PendingSince = &moment
		}
		if now.Sub(*next.PendingSince) < condition.HoldFor {
			if alert.State != alertResolved {
				next.State = alertPending
			}
			return next, ""
		}
		next.Transitions = append(next.Transitions, now)
		next.FiredAt = &moment
		next.ResolvedAt = nil
		next.ClearSince = nil
		next.PendingSince = nil
		if len(next.Transitions) >= flapTransitions {
			next.State = alertFlapping
			return next, notifyFlapping
		}
		next.State = alertFiring
		next.NotifyCount = 0
		return next, notifyFired
	}
}

// Notification은 외부 채널로 보낼 내용이다.
type Notification struct {
	Kind  string
	Alert Alert
	Link  string
}

func (n Notification) Text() string {
	label := map[string]string{notifyFired: "발생", notifyResolved: "복구", notifyReminder: "지속", notifyFlapping: "반복 발생"}[n.Kind]
	text := fmt.Sprintf("[%s] %s: %s", label, n.Alert.ServerID, n.Alert.Title)
	// 복구 알림에 지난 측정값을 현재 값처럼 싣지 않는다.
	if n.Alert.Detail != "" && n.Kind != notifyResolved {
		text += "\n" + n.Alert.Detail
	}
	if n.Link != "" {
		text += "\n" + n.Link
	}
	return text
}

type Notifier interface {
	Send(ctx context.Context, notification Notification) error
}

// WebhookNotifier는 text와 content를 함께 보낸다. Slack과 Discord 형식을 모두 받는 채널에 쓸 수 있다.
type WebhookNotifier struct {
	URL      string
	Client   *http.Client
	Attempts int
	Backoff  time.Duration
}

func (w WebhookNotifier) Send(ctx context.Context, notification Notification) error {
	text := notification.Text()
	body, err := json.Marshal(map[string]string{"text": text, "content": text})
	if err != nil {
		return err
	}
	client := w.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	attempts := max(w.Attempts, 1)
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(w.Backoff << (attempt - 1)):
			}
		}
		if lastErr = w.post(ctx, client, body); lastErr == nil {
			return nil
		}
	}
	return lastErr
}

func (w WebhookNotifier) post(ctx context.Context, client *http.Client, body []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return errors.New("알림 주소가 잘못되었습니다")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		// 웹훅 주소에는 비밀값이 들어 있어 주소가 포함된 원문 오류를 기록하지 않는다.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			return fmt.Errorf("알림 전송 실패: %v", urlErr.Err)
		}
		return errors.New("알림 전송 실패")
	}
	defer response.Body.Close()
	io.Copy(io.Discard, io.LimitReader(response.Body, 1024))
	if response.StatusCode >= http.StatusBadRequest {
		return fmt.Errorf("알림 채널 응답 %d", response.StatusCode)
	}
	return nil
}

// AlertEngine은 중앙 서버에서 주기적으로 상태를 평가한다.
type AlertEngine struct {
	store     *Store
	notifier  Notifier
	hub       *Hub
	publicURL string
	startedAt time.Time
	// SelfCheck가 false를 돌려주면 중앙 서버 자신의 연결 문제로 보고 도달 불가 판정을 미룬다.
	SelfCheck func(ctx context.Context) bool
}

func NewAlertEngine(store *Store, notifier Notifier, hub *Hub, publicURL string, startedAt time.Time) *AlertEngine {
	return &AlertEngine{store: store, notifier: notifier, hub: hub, publicURL: publicURL, startedAt: startedAt}
}

func numberOf(metrics map[string]LatestValue, name string) (float64, bool) {
	value, ok := metrics[name]
	return value.Value, ok
}

// conditions는 서버 하나의 판단 결과를 만든다. 서버에 닿지 못하면 하위 서비스 조건은 만들지 않아
// 알림이 쏟아지지 않게 하고, 진행 중인 알림의 상태도 바꾸지 않는다.
func (e *AlertEngine) conditions(ctx context.Context, server ServerView, now time.Time, firing map[string]bool) ([]AlertCondition, error) {
	lost := server.Reachability.State == "lost"
	agentLostTitle := "에이전트 수신 끊김, 서비스는 응답 중"
	if server.SameHost {
		agentLostTitle = agentLostSameHost
	}
	list := []AlertCondition{{
		Key: "server_unreachable", Rule: "server_unreachable", Target: "host", Severity: protocol.SeverityCritical,
		Title:  "서버 응답 없음, 원인 미상",
		Detail: server.Reachability.Detail,
		Active: lost, ClearFor: time.Minute,
	}, {
		Key: "agent_lost", Rule: "agent_lost", Target: "host", Severity: protocol.SeverityWarning,
		Title:  agentLostTitle,
		Detail: server.Reachability.Detail,
		Active: server.Reachability.State == "agent_lost", ClearFor: time.Minute,
	}}
	if lost || server.Reachability.State == "agent_lost" {
		return list, nil
	}

	threshold := func(key, target, title string, value, enter, leave float64, severity protocol.Severity, hold, clear time.Duration) AlertCondition {
		limit := enter
		if firing[key] {
			limit = leave
		}
		return AlertCondition{
			Key: key, Rule: key, Target: target, Severity: severity,
			Title:  title,
			Detail: fmt.Sprintf("현재 %.1f%%", value),
			Active: value >= limit, HoldFor: hold, ClearFor: clear,
		}
	}
	if value, ok := numberOf(server.Metrics, "host.disk.percent"); ok {
		list = append(list,
			threshold("disk_warning", "host", "디스크 사용량 85% 초과", value, 85, 82, protocol.SeverityWarning, 5*time.Minute, 0),
			threshold("disk_critical", "host", "디스크 사용량 95% 초과", value, 95, 92, protocol.SeverityCritical, 0, 0),
		)
	}
	if value, ok := numberOf(server.Metrics, "host.cpu.percent"); ok {
		list = append(list, threshold("cpu_high", "host", "CPU 사용률 90% 이상 지속", value, 90, 80, protocol.SeverityWarning, 10*time.Minute, 5*time.Minute))
	}
	if value, ok := numberOf(server.Metrics, "host.mem.percent"); ok {
		list = append(list, threshold("memory_high", "host", "메모리 사용률 90% 이상 지속", value, 90, 80, protocol.SeverityWarning, 10*time.Minute, 5*time.Minute))
	}

	view, err := e.store.Topology(ctx, server.ID, nil, now)
	if errors.Is(err, ErrNotFound) {
		return list, nil
	}
	if err != nil {
		return nil, err
	}
	dies, err := e.store.recentEventCounts(ctx, server.ID, "container.die", now.Add(-10*time.Minute), now)
	if err != nil {
		return nil, err
	}
	ooms, err := e.store.recentEventCounts(ctx, server.ID, "container.oom", now.Add(-5*time.Minute), now)
	if err != nil {
		return nil, err
	}
	for _, service := range view.HostServices {
		// 설정에 적은 서비스만 판단한다. 자동으로 찾은 포트는 닫혀도 의도한 종료일 수 있다.
		if service.Evidence != protocol.HostEvidenceConfig {
			continue
		}
		list = append(list, AlertCondition{
			Key: "host_service:" + service.Name, Rule: "host_service_down", Target: "hostsvc:" + service.Name, Severity: protocol.SeverityCritical,
			Title:  "호스트 서비스 " + service.Name + " 포트 닫힘",
			Detail: "설정에 적은 포트가 열려 있지 않습니다.",
			Active: !service.Listening, HoldFor: time.Minute,
		})
	}
	for _, project := range view.Projects {
		for _, service := range project.Services {
			target := "svc:" + service.Key
			list = append(list, AlertCondition{
				Key: "service_down:" + service.Key, Rule: "service_down", Target: target, Severity: protocol.SeverityCritical,
				Title:  service.Key + " 응답 없음",
				Detail: strings.Join(service.Reasons, " "),
				Active: service.Response == ResponseDown,
			}, AlertCondition{
				Key: "external_path:" + service.Key, Rule: "external_path", Target: target, Severity: protocol.SeverityWarning,
				Title:  service.Key + " 외부 경로 문제 의심",
				Detail: "내부 응답은 정상이지만 외부 응답이 실패합니다. 프록시, DNS, 포트 포워딩, 회선을 확인하세요.",
				Active: service.Response == ResponseExternalPath, HoldFor: time.Minute,
			}, AlertCondition{
				Key: "restart_loop:" + service.Key, Rule: "restart_loop", Target: target, Severity: protocol.SeverityCritical,
				Title:  service.Key + " 재시작 반복",
				Detail: fmt.Sprintf("최근 10분 종료 %d회", dies[service.Key]),
				Active: dies[service.Key] >= 3,
			}, AlertCondition{
				Key: "oom:" + service.Key, Rule: "oom", Target: target, Severity: protocol.SeverityCritical,
				Title:  service.Key + " 메모리 부족(OOM) 기록",
				Detail: "OOM이 기록되었습니다. 같은 시간대 기록을 확인하세요.",
				Active: ooms[service.Key] > 0, ClearFor: 30 * time.Minute,
			})
		}
	}
	return list, nil
}

func (e *AlertEngine) link(alert Alert, at time.Time) string {
	if e.publicURL == "" {
		return ""
	}
	query := url.Values{
		"server": {alert.ServerID},
		"target": {alert.Target},
		"from":   {at.Add(-15 * time.Minute).Format(time.RFC3339)},
		"to":     {at.Add(15 * time.Minute).Format(time.RFC3339)},
		"at":     {at.Format(time.RFC3339)},
		"focus":  {"1"},
	}
	return strings.TrimRight(e.publicURL, "/") + "/?" + query.Encode()
}

// Evaluate는 전체 서버를 한 번 평가한다.
func (e *AlertEngine) Evaluate(ctx context.Context, now time.Time) error {
	if now.Sub(e.startedAt) < startupGrace {
		return nil
	}
	selfOK := e.SelfCheck == nil || e.SelfCheck(ctx)
	servers, err := e.store.Servers(ctx, now)
	if err != nil {
		return err
	}
	for _, server := range servers {
		firing, err := e.store.firingKeys(ctx, server.ID)
		if err != nil {
			return err
		}
		conditions, err := e.conditions(ctx, server, now, firing)
		if err != nil {
			return err
		}
		for _, condition := range conditions {
			// 중앙 서버 자신이 인터넷에 닿지 못하면 서버 장애로 단정하지 않는다.
			// 같은 장비의 에이전트는 인터넷을 거치지 않으므로 이 판단과 무관하다.
			if !selfOK && !server.SameHost && (condition.Rule == "server_unreachable" || condition.Rule == "agent_lost") {
				continue
			}
			alert, err := e.store.loadAlert(ctx, server.ID, condition)
			if err != nil {
				return err
			}
			next, kind := stepAlert(alert, condition, now)
			if kind != "" {
				next.LastNotifiedAt = &now
				next.NotifyCount++
			}
			// 대부분의 평가는 바뀐 것이 없다. 10초마다 모든 조건을 다시 쓰지 않는다.
			if !alertChanged(alert, next) {
				continue
			}
			id, err := e.store.saveAlert(ctx, next)
			if err != nil {
				return err
			}
			next.ID = id
			if kind != "" {
				e.announce(ctx, next, kind, now)
			}
		}
	}
	return nil
}

// alertService는 알림 대상을 흐름표의 서비스 키와 표시 이름으로 바꾼다.
func alertService(target string) (service, name string) {
	if key, ok := strings.CutPrefix(target, "svc:"); ok {
		return key, key
	}
	if key, ok := strings.CutPrefix(target, "hostsvc:"); ok {
		return protocol.ServiceKey(protocol.HostProject, key), key
	}
	return "", target
}

// announce는 흐름표에 남기고 채널로 보낸다. 전송 결과도 기록한다.
func (e *AlertEngine) announce(ctx context.Context, alert Alert, kind string, now time.Time) {
	severity := alert.Severity
	result := map[string]string{notifyFired: "발생", notifyResolved: "복구", notifyReminder: "지속", notifyFlapping: "반복 발생"}[kind]
	if kind == notifyResolved {
		severity = protocol.SeverityInfo
	}
	if kind != notifyReminder {
		service, target := alertService(alert.Target)
		if err := e.store.recordEvent(ctx, alert.ServerID, protocol.Event{
			OccurredAt: now, Category: protocol.CategoryAlert, Type: "alert." + kind, Severity: severity,
			Source: "central", Target: target, ServiceKey: service,
			Summary: alert.Title, Result: result, Evidence: "alert-rule",
		}, now); err != nil {
			log.Printf("알림 기록 실패: %v", err)
		}
		if e.hub != nil {
			e.hub.Publish(alert.ServerID)
		}
	}
	if e.notifier == nil {
		return
	}
	at := now
	if alert.FiredAt != nil {
		at = *alert.FiredAt
	}
	err := e.notifier.Send(ctx, Notification{Kind: kind, Alert: alert, Link: e.link(alert, at)})
	if err != nil {
		log.Printf("알림 전송 실패 alert=%d: %v", alert.ID, err)
	}
	if recordErr := e.store.recordDelivery(ctx, alert.ID, kind, now, err); recordErr != nil {
		log.Printf("전송 결과 기록 실패: %v", recordErr)
	}
}

// Run은 ctx가 끝날 때까지 주기적으로 평가한다.
func (e *AlertEngine) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := e.Evaluate(ctx, now.UTC()); err != nil && ctx.Err() == nil {
				log.Printf("알림 평가 실패: %v", err)
			}
		}
	}
}

// recentEventCounts는 제때 도착한 기록만 센다. 재전송으로 늦게 온 기록은 지나간 일이다.
func (s *Store) recentEventCounts(ctx context.Context, serverID, kind string, from, to time.Time) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT service_key, COUNT(*) FROM events
		WHERE server_id = ? AND type = ? AND occurred_at >= ? AND occurred_at <= ? AND received_at - occurred_at <= ?
		GROUP BY service_key`, serverID, kind, from.UnixMilli(), to.UnixMilli(), lateRecordAfter.Milliseconds())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return nil, err
		}
		counts[key] = count
	}
	return counts, rows.Err()
}

func toMillis(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UnixMilli()
}

func fromNullable(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	at := fromMillis(value.Int64)
	return &at
}

const alertColumns = `id, server_id, dedup_key, rule, target, severity, state, title, detail, pending_since, clear_since,
	fired_at, resolved_at, last_notified_at, notify_count, transitions`

func scanAlert(row interface{ Scan(...any) error }) (Alert, error) {
	var alert Alert
	var pending, clear, fired, resolved, notified sql.NullInt64
	var transitions string
	err := row.Scan(&alert.ID, &alert.ServerID, &alert.Key, &alert.Rule, &alert.Target, &alert.Severity, &alert.State,
		&alert.Title, &alert.Detail, &pending, &clear, &fired, &resolved, &notified, &alert.NotifyCount, &transitions)
	if err != nil {
		return alert, err
	}
	alert.PendingSince, alert.ClearSince = fromNullable(pending), fromNullable(clear)
	alert.FiredAt, alert.ResolvedAt, alert.LastNotifiedAt = fromNullable(fired), fromNullable(resolved), fromNullable(notified)
	if err := json.Unmarshal([]byte(transitions), &alert.Transitions); err != nil {
		return alert, err
	}
	return alert, nil
}

func (s *Store) loadAlert(ctx context.Context, serverID string, condition AlertCondition) (Alert, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+alertColumns+` FROM alerts WHERE server_id = ? AND dedup_key = ?`, serverID, condition.Key)
	alert, err := scanAlert(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{
			ServerID: serverID, Key: condition.Key, Rule: condition.Rule, Target: condition.Target,
			Severity: condition.Severity, State: alertIdle, Title: condition.Title,
		}, nil
	}
	return alert, err
}

func (s *Store) saveAlert(ctx context.Context, alert Alert) (int64, error) {
	transitions, err := json.Marshal(alert.Transitions)
	if err != nil {
		return 0, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	var id int64
	err = s.db.QueryRowContext(ctx, `
		INSERT INTO alerts (server_id, dedup_key, rule, target, severity, state, title, detail, pending_since, clear_since,
			fired_at, resolved_at, last_notified_at, notify_count, transitions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (server_id, dedup_key) DO UPDATE SET
			target = excluded.target, severity = excluded.severity, state = excluded.state, title = excluded.title,
			detail = excluded.detail, pending_since = excluded.pending_since, clear_since = excluded.clear_since,
			fired_at = excluded.fired_at, resolved_at = excluded.resolved_at, last_notified_at = excluded.last_notified_at,
			notify_count = excluded.notify_count, transitions = excluded.transitions
		RETURNING id`,
		alert.ServerID, alert.Key, alert.Rule, alert.Target, string(alert.Severity), alert.State, alert.Title, alert.Detail,
		toMillis(alert.PendingSince), toMillis(alert.ClearSince), toMillis(alert.FiredAt), toMillis(alert.ResolvedAt),
		toMillis(alert.LastNotifiedAt), alert.NotifyCount, string(transitions)).Scan(&id)
	return id, err
}

func (s *Store) firingKeys(ctx context.Context, serverID string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT dedup_key FROM alerts WHERE server_id = ? AND state IN ('firing', 'flapping')`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	keys := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		keys[key] = true
	}
	return keys, rows.Err()
}

// Alerts는 진행 중인 알림과 최근 해결된 알림을 돌려준다.
func (s *Store) Alerts(ctx context.Context, now time.Time) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+alertColumns+` FROM alerts
		WHERE state IN ('firing', 'flapping') OR (state = 'resolved' AND resolved_at >= ?)
		ORDER BY state = 'resolved', fired_at DESC`, now.Add(-24*time.Hour).UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	alerts := []Alert{}
	for rows.Next() {
		alert, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		alerts = append(alerts, alert)
	}
	return alerts, rows.Err()
}

func (s *Store) recordEvent(ctx context.Context, serverID string, event protocol.Event, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertEvent(ctx, tx, serverID, event, at); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) recordDelivery(ctx context.Context, alertID int64, kind string, at time.Time, deliveryErr error) error {
	message := ""
	if deliveryErr != nil {
		message = deliveryErr.Error()
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `INSERT INTO notification_deliveries (alert_id, kind, at, ok, error) VALUES (?, ?, ?, ?, ?)`,
		alertID, kind, at.UnixMilli(), deliveryErr == nil, message)
	return err
}

// RecordProbes는 중앙 서버가 직접 수행한 외부 응답 확인을 저장한다.
func (s *Store) RecordProbes(ctx context.Context, serverID string, probes []protocol.Probe, at time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range probes {
		if err := applyProbe(ctx, tx, serverID, p, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}
