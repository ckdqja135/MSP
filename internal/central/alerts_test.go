package central

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

func stepAll(t *testing.T, alert Alert, condition AlertCondition, now time.Time, active bool) (Alert, string) {
	t.Helper()
	condition.Active = active
	return stepAlert(alert, condition, now)
}

func TestAlertWaitsForHoldThenFiresOnce(t *testing.T) {
	condition := AlertCondition{Key: "k", HoldFor: 5 * time.Minute}
	alert := Alert{State: alertIdle}

	alert, kind := stepAll(t, alert, condition, baseTime, true)
	if alert.State != alertPending || kind != "" {
		t.Fatalf("일시적인 값으로 바로 알렸다: %s %s", alert.State, kind)
	}
	// 짧게 튄 값은 알리지 않는다.
	alert, kind = stepAll(t, alert, condition, baseTime.Add(time.Minute), false)
	if alert.State != alertIdle || kind != "" {
		t.Fatalf("idle로 돌아가지 않았다: %+v", alert)
	}
	alert, _ = stepAll(t, alert, condition, baseTime.Add(2*time.Minute), true)
	alert, kind = stepAll(t, alert, condition, baseTime.Add(7*time.Minute), true)
	if alert.State != alertFiring || kind != notifyFired {
		t.Fatalf("지속 후 발생해야 한다: %s %q", alert.State, kind)
	}
	alert.LastNotifiedAt = ptr(baseTime.Add(7 * time.Minute))
	alert.NotifyCount = 1
	_, kind = stepAll(t, alert, condition, baseTime.Add(8*time.Minute), true)
	if kind != "" {
		t.Errorf("중복 알림: %s", kind)
	}
}

func ptr(at time.Time) *time.Time { return &at }

func TestReminderBacksOff(t *testing.T) {
	condition := AlertCondition{Key: "k"}
	alert := Alert{State: alertFiring, LastNotifiedAt: ptr(baseTime), NotifyCount: 1}
	if _, kind := stepAll(t, alert, condition, baseTime.Add(59*time.Minute), true); kind != "" {
		t.Errorf("너무 이른 재알림: %s", kind)
	}
	if _, kind := stepAll(t, alert, condition, baseTime.Add(time.Hour), true); kind != notifyReminder {
		t.Errorf("1시간 뒤 재알림이 없다: %q", kind)
	}
	alert.NotifyCount = 2
	if _, kind := stepAll(t, alert, condition, baseTime.Add(time.Hour), true); kind != "" {
		t.Errorf("두 번째 재알림은 4시간 간격이어야 한다: %q", kind)
	}
	if _, kind := stepAll(t, alert, condition, baseTime.Add(4*time.Hour), true); kind != notifyReminder {
		t.Errorf("4시간 뒤 재알림이 없다: %q", kind)
	}
}

func TestRecoveryNeedsToStayClear(t *testing.T) {
	condition := AlertCondition{Key: "k", ClearFor: time.Minute}
	alert := Alert{State: alertFiring, LastNotifiedAt: ptr(baseTime), NotifyCount: 1}
	alert, kind := stepAll(t, alert, condition, baseTime, false)
	if alert.State != alertFiring || kind != "" {
		t.Fatalf("너무 빨리 복구됐다: %s %s", alert.State, kind)
	}
	// 복구 대기 중 다시 나빠지면 복구 알림이 없다.
	alert, kind = stepAll(t, alert, condition, baseTime.Add(30*time.Second), true)
	if alert.ClearSince != nil || kind != "" {
		t.Fatalf("clear = %+v", alert)
	}
	alert, _ = stepAll(t, alert, condition, baseTime.Add(40*time.Second), false)
	alert, kind = stepAll(t, alert, condition, baseTime.Add(100*time.Second), false)
	if alert.State != alertResolved || kind != notifyResolved {
		t.Fatalf("복구 알림이 없다: %s %q", alert.State, kind)
	}
}

func TestFlappingIsSummarizedInsteadOfRepeating(t *testing.T) {
	condition := AlertCondition{Key: "k"}
	alert := Alert{State: alertIdle}
	var kinds []string
	now := baseTime
	for i := 0; i < 8; i++ {
		var kind string
		alert, kind = stepAll(t, alert, condition, now, i%2 == 0)
		if kind != "" {
			kinds = append(kinds, kind)
			alert.LastNotifiedAt = ptr(now)
			alert.NotifyCount++
		}
		now = now.Add(time.Minute)
	}
	joined := strings.Join(kinds, ",")
	// 4번 전환한 다음부터 반복으로 묶는다.
	if joined != "fired,resolved,fired,resolved,flapping" {
		t.Errorf("알림 순서 = %s", joined)
	}
	if alert.State != alertFlapping {
		t.Errorf("state = %s", alert.State)
	}

	// 충분히 안정되면 한 번만 해결로 알린다.
	alert, kind := stepAll(t, alert, condition, now.Add(10*time.Minute), false)
	if kind != "" {
		t.Errorf("안정되기 전에 알렸다: %s", kind)
	}
	alert, kind = stepAll(t, alert, condition, now.Add(50*time.Minute), false)
	if alert.State != alertResolved || kind != notifyResolved {
		t.Errorf("안정 후 복구가 없다: %s %q", alert.State, kind)
	}
}

type recordingNotifier struct {
	sent []Notification
	err  error
}

func (r *recordingNotifier) Send(_ context.Context, notification Notification) error {
	r.sent = append(r.sent, notification)
	return r.err
}

func enrollLive(t *testing.T) (*Store, *recordingNotifier, *AlertEngine) {
	t.Helper()
	store := openStore(t, protocol.ModeLive)
	notifier := &recordingNotifier{}
	return store, notifier, NewAlertEngine(store, notifier, NewHub(), "https://central.example.test", baseTime.Add(-time.Hour))
}

func heartbeat(t *testing.T, store *Store, seq uint64, at time.Time, mutate func(*protocol.Batch)) {
	t.Helper()
	batch := liveBatch(seq, at)
	batch.Heartbeat = &protocol.Heartbeat{DockerOK: true, BootTime: baseTime}
	if mutate != nil {
		mutate(&batch)
	}
	if _, err := store.Ingest(context.Background(), batch, at); err != nil {
		t.Fatal(err)
	}
}

func runningAPI(at time.Time) *protocol.Topology {
	return topologyWith(at, protocol.Container{ID: "a", Project: "blog", Service: "api", Replica: 1, State: "running", Health: "none"})
}

func downProbes(at time.Time, vantage protocol.Vantage) []protocol.Probe {
	var probes []protocol.Probe
	for i := 0; i < 3; i++ {
		probes = append(probes, protocol.Probe{At: at, ServiceKey: "blog/api", Vantage: vantage, Target: "GET /health", Failure: "timeout", TimeoutMS: 5000})
	}
	return probes
}

func TestRunningButUnresponsiveFiresServiceAlertOnly(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	ctx := context.Background()
	heartbeat(t, store, 1, baseTime, func(b *protocol.Batch) {
		b.Topology = runningAPI(baseTime)
		b.Probes = downProbes(baseTime, protocol.VantageInternal)
	})
	if err := engine.Evaluate(ctx, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 1 || notifier.sent[0].Alert.Rule != "service_down" {
		t.Fatalf("sent = %+v", notifier.sent)
	}
	link := notifier.sent[0].Link
	if !strings.Contains(link, "target=svc%3Ablog%2Fapi") || !strings.Contains(link, "from=") || !strings.Contains(link, "focus=1") {
		t.Errorf("알림 링크에 대상과 시간이 없다: %s", link)
	}
	if err := engine.Evaluate(ctx, baseTime.Add(10*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 1 {
		t.Errorf("중복 알림: %d", len(notifier.sent))
	}
	events, err := store.Events(ctx, "home-1", EventFilter{From: baseTime.Add(-time.Hour), To: baseTime.Add(time.Hour), Categories: []string{"alert"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ServiceKey != "blog/api" {
		t.Errorf("흐름표에 알림 기록이 없다: %+v", events)
	}
}

func TestServerLossSuppressesServiceAlerts(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	ctx := context.Background()
	heartbeat(t, store, 1, baseTime, func(b *protocol.Batch) {
		b.Topology = runningAPI(baseTime)
		b.Probes = downProbes(baseTime, protocol.VantageInternal)
	})
	// 서버 수신이 끊긴 시점에 처음 평가한다.
	later := baseTime.Add(5 * time.Minute)
	if err := engine.Evaluate(ctx, later); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 1 || notifier.sent[0].Alert.Rule != "server_unreachable" {
		t.Fatalf("sent = %+v", notifier.sent)
	}
	if strings.Contains(notifier.sent[0].Text(), "정전") {
		t.Errorf("원인을 단정했다: %s", notifier.sent[0].Text())
	}
}

func TestRecoveryNotificationSaysWhatRecovered(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	ctx := context.Background()
	heartbeat(t, store, 1, baseTime, nil)
	lost := baseTime.Add(5 * time.Minute)
	if err := engine.Evaluate(ctx, lost); err != nil {
		t.Fatal(err)
	}
	back := lost.Add(time.Minute)
	heartbeat(t, store, 2, back, nil)
	if err := engine.Evaluate(ctx, back); err != nil {
		t.Fatal(err)
	}
	heartbeat(t, store, 3, back.Add(61*time.Second), nil)
	if err := engine.Evaluate(ctx, back.Add(61*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 2 || notifier.sent[1].Kind != notifyResolved {
		t.Fatalf("sent = %+v", notifier.sent)
	}
	text := notifier.sent[1].Text()
	if !strings.Contains(text, "서버 응답 없음") {
		t.Errorf("무엇이 복구됐는지 알 수 없다: %s", text)
	}
	if strings.Contains(text, "수신이 끊겼습니다") {
		t.Errorf("복구 알림에 장애 설명이 남았다: %s", text)
	}
	alerts, err := store.Alerts(ctx, back.Add(62*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].Title != "서버 응답 없음, 원인 미상" || alerts[0].State != alertResolved {
		t.Errorf("alerts = %+v", alerts)
	}
}

func TestRecoveredThresholdDoesNotRepeatOldValue(t *testing.T) {
	notification := Notification{Kind: notifyResolved, Alert: Alert{ServerID: "home-1", Title: "디스크 사용량 85% 초과", Detail: "현재 90.0%"}}
	if strings.Contains(notification.Text(), "90.0%") {
		t.Errorf("지난 측정값을 현재 값처럼 보냈다: %s", notification.Text())
	}
	notification.Kind = notifyFired
	if !strings.Contains(notification.Text(), "90.0%") {
		t.Errorf("발생 알림에는 측정값이 있어야 한다: %s", notification.Text())
	}
}

func TestExternalSuccessDoesNotBecomeServerAlert(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	ctx := context.Background()
	heartbeat(t, store, 1, baseTime, nil)
	later := baseTime.Add(5 * time.Minute)
	if err := store.RecordProbes(ctx, "home-1", []protocol.Probe{{At: later, ServiceKey: "blog/api", Vantage: protocol.VantageExternal, Target: "GET https://x", OK: true}}, later); err != nil {
		t.Fatal(err)
	}
	if err := engine.Evaluate(ctx, later); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 1 || notifier.sent[0].Alert.Rule != "agent_lost" {
		t.Fatalf("sent = %+v", notifier.sent)
	}
}

func TestStartupGracePreventsFalseAlarm(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	notifier := &recordingNotifier{}
	heartbeat(t, store, 1, baseTime, nil)
	started := baseTime.Add(10 * time.Minute)
	engine := NewAlertEngine(store, notifier, NewHub(), "", started)
	if err := engine.Evaluate(context.Background(), started.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 0 {
		t.Errorf("중앙 서버 재시작 직후 알렸다: %+v", notifier.sent)
	}
	if err := engine.Evaluate(context.Background(), started.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 1 {
		t.Errorf("유예 후에는 판정해야 한다: %d", len(notifier.sent))
	}
}

func TestOwnConnectivityLossPostponesServerAlert(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	heartbeat(t, store, 1, baseTime, nil)
	engine.SelfCheck = func(context.Context) bool { return false }
	if err := engine.Evaluate(context.Background(), baseTime.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 0 {
		t.Errorf("중앙 서버 연결 문제를 서버 장애로 알렸다: %+v", notifier.sent)
	}
}

func TestLateRecordsDoNotFireAlerts(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	ctx := context.Background()
	now := baseTime.Add(4 * time.Minute)
	heartbeat(t, store, 1, now, func(b *protocol.Batch) {
		b.Topology = runningAPI(now)
		// 연결이 끊긴 동안 버퍼에 있다가 지금 도착한 3분 전 OOM이다.
		b.Events = []protocol.Event{{OccurredAt: now.Add(-3 * time.Minute), Category: protocol.CategoryOps, Type: "container.oom",
			Severity: protocol.SeverityCritical, ServiceKey: "blog/api", Summary: "OOM", Evidence: "docker-event"}}
	})
	if err := engine.Evaluate(ctx, now); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 0 {
		t.Errorf("지나간 일을 지금 일처럼 알렸다: %+v", notifier.sent)
	}
	// 기록은 흐름표에 남아 있어야 한다.
	events, err := store.Events(ctx, "home-1", EventFilter{From: baseTime, To: now})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("events = %d", len(events))
	}
}

func TestTimelyOOMAndRestartLoopFire(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	ctx := context.Background()
	heartbeat(t, store, 1, baseTime, func(b *protocol.Batch) {
		b.Topology = runningAPI(baseTime)
		b.Events = []protocol.Event{{OccurredAt: baseTime, Category: protocol.CategoryOps, Type: "container.oom", Severity: protocol.SeverityCritical, ServiceKey: "blog/api", Summary: "OOM", Evidence: "docker-event"}}
		for i := 0; i < 3; i++ {
			b.Events = append(b.Events, protocol.Event{OccurredAt: baseTime, Category: protocol.CategoryOps, Type: "container.die", Severity: protocol.SeverityCritical, ServiceKey: "blog/api", Summary: "종료", Evidence: "docker-event"})
		}
	})
	if err := engine.Evaluate(ctx, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	rules := map[string]bool{}
	for _, sent := range notifier.sent {
		rules[sent.Alert.Rule] = true
	}
	if !rules["oom"] || !rules["restart_loop"] {
		t.Errorf("rules = %v", rules)
	}
}

func TestDiskAlertUsesHysteresisAndNotifiesRecovery(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	ctx := context.Background()
	send := func(seq uint64, at time.Time, percent float64) {
		heartbeat(t, store, seq, at, func(b *protocol.Batch) {
			b.Samples = []protocol.Sample{{At: at, Scope: protocol.HostScope, Name: "host.disk.percent", Value: percent}}
		})
		if err := engine.Evaluate(ctx, at); err != nil {
			t.Fatal(err)
		}
	}
	send(1, baseTime, 90)
	if len(notifier.sent) != 0 {
		t.Fatal("5분 지속 전에 알렸다")
	}
	send(2, baseTime.Add(6*time.Minute), 90)
	if len(notifier.sent) != 1 || notifier.sent[0].Alert.Rule != "disk_warning" {
		t.Fatalf("sent = %+v", notifier.sent)
	}
	// 기준 바로 아래로 내려가도 복구로 보지 않는다.
	send(3, baseTime.Add(7*time.Minute), 84)
	if len(notifier.sent) != 1 {
		t.Fatalf("경계에서 복구 알림: %+v", notifier.sent)
	}
	send(4, baseTime.Add(8*time.Minute), 80)
	if len(notifier.sent) != 2 || notifier.sent[1].Kind != notifyResolved {
		t.Fatalf("복구 알림이 없다: %+v", notifier.sent)
	}
}

func TestNotificationFailureIsRecorded(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	notifier.err = errors.New("채널 응답 없음")
	heartbeat(t, store, 1, baseTime, nil)
	if err := engine.Evaluate(context.Background(), baseTime.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	var ok int
	var message string
	if err := store.db.QueryRow(`SELECT ok, error FROM notification_deliveries`).Scan(&ok, &message); err != nil {
		t.Fatal(err)
	}
	if ok != 0 || message == "" {
		t.Errorf("ok = %d, error = %q", ok, message)
	}
}

func TestWebhookRetriesAndNeverLeaksURL(t *testing.T) {
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer srv.Close()
	hook := WebhookNotifier{URL: srv.URL + "/SECRET-PATH", Attempts: 3, Backoff: time.Millisecond}
	if err := hook.Send(context.Background(), Notification{Kind: notifyFired}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d", calls.Load())
	}

	srv.Close()
	err := hook.Send(context.Background(), Notification{Kind: notifyFired})
	if err == nil {
		t.Fatal("닫힌 서버로 전송이 성공했다")
	}
	if strings.Contains(err.Error(), "SECRET-PATH") {
		t.Errorf("오류에 웹훅 주소가 들어 있다: %v", err)
	}
}

func TestDeclaredHostServiceClosedFiresAlert(t *testing.T) {
	store, notifier, engine := enrollLive(t)
	ctx := context.Background()
	withHost := func(at time.Time, services ...protocol.HostService) *protocol.Topology {
		topology := runningAPI(at)
		topology.HostServices = services
		topology.Normalize()
		return topology
	}
	guessed := protocol.HostService{Name: "sshd", Evidence: protocol.HostEvidenceName, Ports: []protocol.HostPort{{Port: 22}}}
	closed := protocol.HostService{Name: "nginx", Evidence: protocol.HostEvidenceConfig, Ports: []protocol.HostPort{{Port: 80}}}
	heartbeat(t, store, 1, baseTime, func(b *protocol.Batch) { b.Topology = withHost(baseTime, guessed, closed) })
	if err := engine.Evaluate(ctx, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 0 {
		t.Fatalf("1분 지속 전에 알렸다: %+v", notifier.sent)
	}
	later := baseTime.Add(61 * time.Second)
	heartbeat(t, store, 2, later, nil)
	if err := engine.Evaluate(ctx, later); err != nil {
		t.Fatal(err)
	}
	if len(notifier.sent) != 1 || notifier.sent[0].Alert.Target != "hostsvc:nginx" {
		t.Fatalf("sent = %+v", notifier.sent)
	}
	for _, sent := range notifier.sent {
		if strings.Contains(sent.Alert.Title, "sshd") {
			t.Errorf("자동으로 찾은 서비스로 알렸다: %+v", sent)
		}
	}
}

func TestAlertsAPIListsActiveFirst(t *testing.T) {
	store, _, engine := enrollLive(t)
	ctx := context.Background()
	heartbeat(t, store, 1, baseTime, func(b *protocol.Batch) {
		b.Topology = runningAPI(baseTime)
		b.Probes = downProbes(baseTime, protocol.VantageInternal)
	})
	if err := engine.Evaluate(ctx, baseTime.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	alerts, err := store.Alerts(ctx, baseTime.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts) != 1 || alerts[0].State != alertFiring || alerts[0].Target != "svc:blog/api" {
		t.Errorf("alerts = %+v", alerts)
	}
}
