package central

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

var baseTime = time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)

func openStore(t *testing.T, mode protocol.Mode) *Store {
	t.Helper()
	store, err := OpenStore(filepath.Join(t.TempDir(), "test.db"), mode)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func liveBatch(seq uint64, at time.Time) protocol.Batch {
	return protocol.Batch{
		ProtocolVersion: protocol.Version, ServerID: "home-1", Mode: protocol.ModeLive,
		Epoch: "e1", Seq: seq, SentAt: at,
	}
}

func topologyWith(at time.Time, containers ...protocol.Container) *protocol.Topology {
	topology := &protocol.Topology{ObservedAt: at, Containers: containers}
	topology.Normalize()
	return topology
}

func TestDuplicateBatchIsNotStoredTwice(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ctx := context.Background()
	batch := liveBatch(1, baseTime)
	batch.Events = []protocol.Event{{OccurredAt: baseTime, Category: protocol.CategoryOps, Type: "container.die",
		Severity: protocol.SeverityCritical, Summary: "종료", Evidence: "docker-event"}}

	if _, err := store.Ingest(ctx, batch, baseTime); err != nil {
		t.Fatal(err)
	}
	ack, err := store.Ingest(ctx, batch, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	if !ack.Duplicate {
		t.Error("재전송을 중복으로 판단하지 못했다")
	}
	events, err := store.Events(ctx, "home-1", EventFilter{From: baseTime.Add(-time.Hour), To: baseTime.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Errorf("events = %d, want 1", len(events))
	}
}

func TestUnmeasuredValuesStayNull(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ctx := context.Background()
	zero := int64(0)
	batch := liveBatch(1, baseTime)
	batch.Events = []protocol.Event{
		{OccurredAt: baseTime, Category: protocol.CategoryOps, Type: "a", Severity: protocol.SeverityInfo, Summary: "미측정", Evidence: "x"},
		{OccurredAt: baseTime.Add(time.Second), Category: protocol.CategoryOps, Type: "b", Severity: protocol.SeverityInfo, Summary: "0 측정", Evidence: "x", DurationMS: &zero, Bytes: &zero},
	}
	if _, err := store.Ingest(ctx, batch, baseTime); err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(ctx, "home-1", EventFilter{From: baseTime.Add(-time.Hour), To: baseTime.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	measured, unmeasured := events[0], events[1]
	if unmeasured.DurationMS != nil || unmeasured.Bytes != nil {
		t.Errorf("측정하지 않은 값이 채워졌다: %+v", unmeasured)
	}
	if measured.DurationMS == nil || *measured.DurationMS != 0 {
		t.Errorf("측정된 0이 사라졌다: %+v", measured)
	}
}

func TestTopologyVersionsOnlyOnChangeAndHistoryIsReadable(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ctx := context.Background()
	oldAPI := protocol.Container{ID: "old", Name: "blog-api-1", Project: "blog", Service: "api", Replica: 1, State: "running", Health: "none"}
	newAPI := oldAPI
	newAPI.ID = "new"

	for i, step := range []struct {
		at        time.Time
		container protocol.Container
	}{
		{baseTime, oldAPI},
		{baseTime.Add(time.Minute), oldAPI},
		{baseTime.Add(2 * time.Minute), newAPI},
	} {
		batch := liveBatch(uint64(i+1), step.at)
		batch.Topology = topologyWith(step.at, step.container)
		if _, err := store.Ingest(ctx, batch, step.at); err != nil {
			t.Fatal(err)
		}
	}

	var versions int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM topology_versions`).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 2 {
		t.Errorf("versions = %d, want 2 (변경 시에만 저장)", versions)
	}

	now := baseTime.Add(3 * time.Minute)
	past := baseTime.Add(90 * time.Second)
	historical, err := store.Topology(ctx, "home-1", &past, now)
	if err != nil {
		t.Fatal(err)
	}
	if id := historical.Projects[0].Services[0].Containers[0].ID; id != "old" || !historical.Historical {
		t.Errorf("과거 구조 id = %s historical = %v", id, historical.Historical)
	}
	if historical.Projects[0].Services[0].Response != ResponseHistorical {
		t.Errorf("과거 응답 상태를 현재 값처럼 표시했다: %s", historical.Projects[0].Services[0].Response)
	}

	current, err := store.Topology(ctx, "home-1", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	service := current.Projects[0].Services[0]
	if service.Key != "blog/api" || service.Containers[0].ID != "new" {
		t.Errorf("재생성 후 서비스 키가 유지되지 않았다: %+v", service)
	}
}

func TestSameNetworkDoesNotCreateServiceRelation(t *testing.T) {
	a := protocol.Container{ID: "a", Project: "blog", Service: "web", Networks: []protocol.NetworkAttachment{{Network: "shared"}}}
	b := protocol.Container{ID: "b", Project: "shop", Service: "app", Networks: []protocol.NetworkAttachment{{Network: "shared"}}}
	relations := buildRelations(topologyWith(baseTime, a, b))
	for _, r := range relations {
		if r.Type != RelationMemberOf {
			t.Errorf("소속 외 관계가 생겼다: %+v", r)
		}
		if !strings.HasPrefix(r.To, "network:") {
			t.Errorf("서비스 간 연결이 생겼다: %+v", r)
		}
	}
	if len(relations) != 2 {
		t.Errorf("relations = %d, want 2", len(relations))
	}
}

func TestHostServicesReachTheViewAndAreVersioned(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ctx := context.Background()
	first := topologyWith(baseTime)
	first.HostServices = []protocol.HostService{{Name: "nginx", Evidence: protocol.HostEvidenceName, Listening: true, Ports: []protocol.HostPort{{IP: "0.0.0.0", Port: 80}}}}
	first.Normalize()
	batch := liveBatch(1, baseTime)
	batch.Topology = first
	if _, err := store.Ingest(ctx, batch, baseTime); err != nil {
		t.Fatal(err)
	}
	later := baseTime.Add(time.Minute)
	second := topologyWith(later)
	second.Normalize()
	batch = liveBatch(2, later)
	batch.Topology = second
	if _, err := store.Ingest(ctx, batch, later); err != nil {
		t.Fatal(err)
	}

	now := baseTime.Add(2 * time.Minute)
	current, err := store.Topology(ctx, "home-1", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.HostServices) != 0 {
		t.Errorf("current = %+v", current.HostServices)
	}
	past := baseTime.Add(30 * time.Second)
	historical, err := store.Topology(ctx, "home-1", &past, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(historical.HostServices) != 1 || historical.HostServices[0].Name != "nginx" {
		t.Errorf("과거 호스트 서비스 = %+v", historical.HostServices)
	}
}

func TestDeclaredAndPublishedRelationsCarryEvidence(t *testing.T) {
	c := protocol.Container{ID: "a", Project: "blog", Service: "api", DeclaredDependsOn: []string{"db"},
		Ports: []protocol.PortBinding{
			{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8080, Proto: "tcp"},
			{ContainerPort: 9090, Proto: "tcp"},
		}}
	relations := buildRelations(topologyWith(baseTime, c))
	if len(relations) != 2 {
		t.Fatalf("relations = %+v", relations)
	}
	for _, r := range relations {
		if r.Evidence == "" {
			t.Errorf("근거 없는 관계: %+v", r)
		}
		if strings.Contains(r.Detail, "9090") {
			t.Errorf("공개되지 않은 포트가 공개 관계로 표시됐다: %+v", r)
		}
	}
}

func TestRollupDistinguishesRuntimeFromResponse(t *testing.T) {
	running := protocol.Container{State: "running", Health: "none"}
	cases := []struct {
		name       string
		containers []protocol.Container
		probes     []ProbeView
		fresh      bool
		status     string
		response   string
	}{
		{"실행 중, 프로브 없음은 응답 미확인", []protocol.Container{running}, nil, true, StatusOK, ResponseUnconfirmed},
		{"실행 중, 응답 정상", []protocol.Container{running}, []ProbeView{{State: probeOK}}, true, StatusOK, ResponseOK},
		{"실행 중, 일시 실패는 주의", []protocol.Container{running}, []ProbeView{{State: probeFailing}}, true, StatusWarning, ResponseFailing},
		{"실행 중, 연속 실패는 장애", []protocol.Container{running}, []ProbeView{{State: probeDown}}, true, StatusCritical, ResponseDown},
		{"하나라도 무응답이면 무응답", []protocol.Container{running}, []ProbeView{{State: probeOK}, {State: probeDown}}, true, StatusCritical, ResponseDown},
		{"수신이 끊기면 연결 끊김", []protocol.Container{running}, []ProbeView{{State: probeOK}}, false, StatusDisconnected, ResponseOK},
		{"비정상 종료는 장애", []protocol.Container{{State: "exited", ExitCode: 137, Health: "none"}}, nil, true, StatusCritical, ResponseUnconfirmed},
		{"정상 종료는 장애가 아님", []protocol.Container{{State: "exited", ExitCode: 0, Health: "none"}}, nil, true, StatusStopped, ResponseUnconfirmed},
		{"일회성 작업 실패는 서비스 장애가 아님", []protocol.Container{{State: "exited", ExitCode: 1, OneOff: true, Health: "none"}}, nil, true, StatusStopped, ResponseUnconfirmed},
		{"안에서만 응답하면 외부 경로 의심", []protocol.Container{running}, []ProbeView{{Vantage: protocol.VantageInternal, State: probeOK}, {Vantage: protocol.VantageExternal, State: probeDown}}, true, StatusWarning, ResponseExternalPath},
		{"내부도 실패하면 서비스 장애", []protocol.Container{running}, []ProbeView{{Vantage: protocol.VantageInternal, State: probeDown}, {Vantage: protocol.VantageExternal, State: probeDown}}, true, StatusCritical, ResponseDown},
		{"외부 일시 실패는 아직 경로 문제로 보지 않음", []protocol.Container{running}, []ProbeView{{Vantage: protocol.VantageInternal, State: probeOK}, {Vantage: protocol.VantageExternal, State: probeFailing}}, true, StatusWarning, ResponseFailing},
		{"unhealthy는 장애", []protocol.Container{{State: "running", Health: "unhealthy"}}, nil, true, StatusCritical, ResponseUnconfirmed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, response, _ := rollupService(tc.containers, tc.probes, tc.fresh, false)
			if status != tc.status || response != tc.response {
				t.Errorf("status = %s, response = %s, want %s, %s", status, response, tc.status, tc.response)
			}
		})
	}
}

func TestProbeRequiresConsecutiveFailures(t *testing.T) {
	fail := protocol.Probe{Target: "GET /health", ServiceKey: "blog/api", Vantage: protocol.VantageInternal, Failure: "timeout", TimeoutMS: 5000}
	ok := protocol.Probe{Target: "GET /health", ServiceKey: "blog/api", Vantage: protocol.VantageInternal, OK: true}

	state := probeState{}
	var event *protocol.Event
	state, event = nextProbeState(state, fail)
	if state.state != probeFailing || event == nil || event.Severity != protocol.SeverityWarning {
		t.Fatalf("첫 실패: %+v %+v", state, event)
	}
	if event.DurationMS != nil || event.DurationLimitMS == nil || *event.DurationLimitMS != 5000 {
		t.Errorf("제한 시간을 측정값으로 기록했다: %+v", event)
	}
	state, event = nextProbeState(state, fail)
	if state.state != probeFailing || event != nil {
		t.Fatalf("두 번째 실패는 기록하지 않는다: %+v %+v", state, event)
	}
	state, event = nextProbeState(state, fail)
	if state.state != probeDown || event == nil || event.Severity != protocol.SeverityCritical {
		t.Fatalf("세 번째 실패: %+v %+v", state, event)
	}
	state, event = nextProbeState(state, fail)
	if event != nil {
		t.Error("장애 지속 중 행을 반복해서 만들었다")
	}

	for i := 0; i < probeRecoverThreshold-1; i++ {
		state, event = nextProbeState(state, ok)
		if state.state != probeDown || event != nil {
			t.Fatalf("복구 판정이 너무 빠르다: %+v", state)
		}
	}
	state, event = nextProbeState(state, ok)
	if state.state != probeOK || event == nil || event.Type != "probe.recovered" {
		t.Fatalf("복구: %+v %+v", state, event)
	}

	state, _ = nextProbeState(probeState{state: probeOK}, fail)
	state, event = nextProbeState(state, ok)
	if state.state != probeOK || event == nil || event.Type != "probe.ok" {
		t.Errorf("일시 실패 후 응답: %+v %+v", state, event)
	}
}

func TestReachabilityNeverClaimsACause(t *testing.T) {
	now := baseTime
	cases := []struct {
		age   time.Duration
		state string
	}{
		{10 * time.Second, "ok"},
		{45 * time.Second, "delayed"},
		{5 * time.Minute, "lost"},
	}
	for _, tc := range cases {
		r := reachabilityOf(now.Add(-tc.age), now, true, false, false)
		if r.State != tc.state {
			t.Errorf("age %v: %s, want %s", tc.age, r.State, tc.state)
		}
		if strings.Contains(r.Label, "정전") {
			t.Errorf("원인을 단정했다: %s", r.Label)
		}
	}
	if r := reachabilityOf(now, now, false, false, false); r.State != "docker_down" {
		t.Errorf("Docker 중단 구분 실패: %+v", r)
	}
	if r := reachabilityOf(now.Add(-5*time.Minute), now, true, true, false); r.State != "agent_lost" || strings.Contains(r.Label, "서버 응답 없음") {
		t.Errorf("외부 응답이 있는데 서버 장애로 표시했다: %+v", r)
	}
}

func TestStaleSamplesAreNotReportedAsLatest(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ctx := context.Background()
	batch := liveBatch(1, baseTime)
	batch.Samples = []protocol.Sample{{At: baseTime, Scope: protocol.HostScope, Name: "host.cpu.percent", Value: 12}}
	if _, err := store.Ingest(ctx, batch, baseTime); err != nil {
		t.Fatal(err)
	}
	recent, err := store.Servers(ctx, baseTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recent[0].Metrics["host.cpu.percent"]; !ok {
		t.Error("최근 값이 없다")
	}
	stale, err := store.Servers(ctx, baseTime.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := stale[0].Metrics["host.cpu.percent"]; ok {
		t.Error("오래된 값을 최신값으로 돌려줬다")
	}
}

func TestSamplesLeaveGapsWhereNoData(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ctx := context.Background()
	batch := liveBatch(1, baseTime)
	for _, offset := range []time.Duration{0, time.Minute, 30 * time.Minute} {
		batch.Samples = append(batch.Samples, protocol.Sample{At: baseTime.Add(offset), Scope: "host", Name: "m", Value: 1})
	}
	if _, err := store.Ingest(ctx, batch, baseTime); err != nil {
		t.Fatal(err)
	}
	series, _, err := store.Samples(ctx, "home-1", "host", []string{"m"}, baseTime, baseTime.Add(time.Hour), 60)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(series[0].Points); got != 3 {
		t.Errorf("points = %d, want 3 (빈 구간은 점을 만들지 않음)", got)
	}
}

func TestDatabaseModeCannotBeMixed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	demo, err := OpenStore(path, protocol.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	demo.Close()
	if _, err := OpenStore(path, protocol.ModeLive); err == nil {
		t.Error("데모 DB를 실제 모드로 열었다")
	}
}

func TestResetOnlyWorksInDemoMode(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	if err := store.ResetDemo(context.Background()); err == nil {
		t.Error("실제 DB를 초기화했다")
	}
}

func TestStoreRejectsBatchFromOtherMode(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	batch := liveBatch(1, baseTime)
	batch.Mode = protocol.ModeDemo
	if _, err := store.Ingest(context.Background(), batch, baseTime); err == nil {
		t.Error("데모 배치를 실제 DB에 저장했다")
	}
}

func TestValidateListen(t *testing.T) {
	cases := []struct {
		listen, password string
		ok               bool
	}{
		{"127.0.0.1:8787", "", true},
		{"localhost:8787", "", true},
		{"[::1]:8787", "", true},
		{"0.0.0.0:8787", "", false},
		{":8787", "", false},
		{"192.168.0.10:8787", "", false},
		{"0.0.0.0:8787", "secret", true},
	}
	for _, tc := range cases {
		if err := ValidateListen(tc.listen, tc.password); (err == nil) != tc.ok {
			t.Errorf("ValidateListen(%q, %q) = %v", tc.listen, tc.password, err)
		}
	}
}

func newTestServer(t *testing.T, store *Store, password string) *httptest.Server {
	t.Helper()
	srv := NewServer(store, NewHub(), Config{DashboardPassword: password})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postBatch(t *testing.T, url, server, token string, batch protocol.Batch) int {
	t.Helper()
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, url+"/agent/v1/batches", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-MSP-Server", server)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func TestAgentEndpointRequiresValidToken(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	if err := store.Enroll(context.Background(), "home-1", "good-token"); err != nil {
		t.Fatal(err)
	}
	ts := newTestServer(t, store, "")
	batch := liveBatch(1, baseTime)

	if code := postBatch(t, ts.URL, "home-1", "wrong", batch); code != http.StatusUnauthorized {
		t.Errorf("잘못된 토큰: %d", code)
	}
	if code := postBatch(t, ts.URL, "unknown", "good-token", batch); code != http.StatusUnauthorized {
		t.Errorf("없는 서버: %d", code)
	}
	other := batch
	other.ServerID = "other"
	if code := postBatch(t, ts.URL, "home-1", "good-token", other); code != http.StatusForbidden {
		t.Errorf("다른 서버 사칭: %d", code)
	}
	if code := postBatch(t, ts.URL, "home-1", "good-token", batch); code != http.StatusOK {
		t.Errorf("정상 토큰: %d", code)
	}

	var stored string
	if err := store.db.QueryRow(`SELECT token_hash FROM servers WHERE id = 'home-1'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "good-token" {
		t.Error("토큰 원문을 저장했다")
	}
}

func TestDemoServerRejectsRealAgents(t *testing.T) {
	store := openStore(t, protocol.ModeDemo)
	ts := newTestServer(t, store, "")
	if code := postBatch(t, ts.URL, "home-1", "any", liveBatch(1, baseTime)); code != http.StatusForbidden {
		t.Errorf("데모 서버가 에이전트 수신을 거부하지 않았다: %d", code)
	}
}

func TestLoopbackDashboardRejectsForeignHost(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ts := newTestServer(t, store, "")

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/meta", nil)
	req.Host = "evil.example.com"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("외부 Host: %d", resp.StatusCode)
	}

	resp, err = http.Get(ts.URL + "/api/v1/meta")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var meta map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta["read_only"] != true {
		t.Errorf("meta = %+v", meta)
	}
}

func TestDashboardPasswordIsEnforced(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ts := newTestServer(t, store, "pw")

	resp, err := http.Get(ts.URL + "/api/v1/meta")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("비밀번호 없음: %d", resp.StatusCode)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/meta", nil)
	req.SetBasicAuth("admin", "pw")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("비밀번호 있음: %d", resp.StatusCode)
	}
}

func TestDashboardHasNoMutatingRoutes(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ts := newTestServer(t, store, "")
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, path := range []string{"/api/v1/servers", "/api/v1/servers/home-1/topology", "/api/v1/servers/home-1/events"} {
			req, _ := http.NewRequest(method, ts.URL+path, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed && resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s: %d", method, path, resp.StatusCode)
			}
		}
	}
}

func TestSameHostLossIsNotServerLoss(t *testing.T) {
	now := baseTime
	r := reachabilityOf(now.Add(-5*time.Minute), now, true, false, true)
	if r.State != "agent_lost" || strings.Contains(r.Label, "서버 응답 없음") {
		t.Errorf("중앙 서버가 동작 중인데 서버 장애처럼 표시했다: %+v", r)
	}
	if r := reachabilityOf(now.Add(-40*time.Second), now, true, false, true); r.State != "delayed" {
		t.Errorf("짧은 지연은 그대로 지연이어야 한다: %+v", r)
	}
}

func TestExternalProbeFromSameHostIsRejected(t *testing.T) {
	probes := []ExternalProbe{{Server: "home-1", Service: "blog/web", URL: "https://blog.example.test/"}}
	if err := ValidateExternalProbes(probes, []string{"home-1"}); err == nil {
		t.Error("같은 장비에서 보내는 요청을 외부 확인으로 받아들였다")
	}
	if err := ValidateExternalProbes(probes, []string{"home-2"}); err != nil {
		t.Errorf("다른 장비의 외부 확인을 막았다: %v", err)
	}
}

func queryPlan(t *testing.T, store *Store, query string, args ...any) string {
	t.Helper()
	rows, err := store.db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail + "\n"
	}
	return plan
}

func TestPurgeRemovesOnlyExpiredSamples(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ctx := context.Background()
	if err := store.Enroll(ctx, "home-1", "token"); err != nil {
		t.Fatal(err)
	}
	old := baseTime.Add(-sampleRetention - time.Minute)
	recent := baseTime.Add(-sampleRetention + time.Minute)
	batch := liveBatch(1, baseTime)
	batch.Samples = []protocol.Sample{
		{At: old, Scope: protocol.HostScope, Name: "host.cpu.percent", Value: 1},
		{At: recent, Scope: protocol.HostScope, Name: "host.cpu.percent", Value: 2},
	}
	if _, err := store.Ingest(ctx, batch, baseTime); err != nil {
		t.Fatal(err)
	}
	if err := store.Purge(ctx, baseTime); err != nil {
		t.Fatal(err)
	}
	var remaining []float64
	rows, err := store.db.QueryContext(ctx, `SELECT value FROM samples`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var value float64
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		remaining = append(remaining, value)
	}
	if len(remaining) != 1 || remaining[0] != 2 {
		t.Errorf("남은 값 = %v, want [2]", remaining)
	}
}

// 10초와 1시간마다 도는 쿼리가 표 전체를 훑지 않는지 본다. 데이터가 쌓일수록 부하가 커지는 것을 막는다.
func TestPeriodicQueriesUseTimeIndex(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	for name, plan := range map[string]string{
		"정리":  queryPlan(t, store, `DELETE FROM samples WHERE server_id IN (SELECT id FROM servers) AND at < ?`, 1),
		"최신값": queryPlan(t, store, `SELECT scope, name, value, MAX(at) FROM samples INDEXED BY samples_time WHERE server_id = ? AND at > ? AND at <= ? GROUP BY scope, name`, "a", 1, 2),
	} {
		if !strings.Contains(plan, "samples_time") || !strings.Contains(plan, "at") || strings.Contains(plan, "SCAN samples") {
			t.Errorf("%s 쿼리가 시간 인덱스를 쓰지 않는다:\n%s", name, plan)
		}
	}
}
