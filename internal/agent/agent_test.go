package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ckdqja135/MSP/internal/agent/dockerapi"
	"github.com/ckdqja135/MSP/internal/agent/hostports"
	"github.com/ckdqja135/MSP/internal/central"
	"github.com/ckdqja135/MSP/internal/protocol"
)

var testStart = time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)

type fakeDocker struct {
	mu         sync.Mutex
	containers []protocol.Container
	stats      map[string]dockerapi.Stats
	topoErr    error
	pingErr    error
	eventsFn   func(ctx context.Context, since time.Time, handle func(dockerapi.Event)) error
	logsFn     func(ctx context.Context, id string, options dockerapi.LogOptions, emit func(dockerapi.LogEntry) bool) error
}

func (f *fakeDocker) Logs(ctx context.Context, id string, options dockerapi.LogOptions, emit func(dockerapi.LogEntry) bool) error {
	if f.logsFn != nil {
		return f.logsFn(ctx, id, options, emit)
	}
	return nil
}

func (f *fakeDocker) Ping(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pingErr
}

func (f *fakeDocker) Topology(context.Context) (*protocol.Topology, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.topoErr != nil {
		return nil, f.topoErr
	}
	topology := &protocol.Topology{ObservedAt: testStart, Containers: append([]protocol.Container{}, f.containers...)}
	topology.Normalize()
	return topology, nil
}

func (f *fakeDocker) Stats(_ context.Context, id string) (dockerapi.Stats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stats[id], nil
}

func (f *fakeDocker) Events(ctx context.Context, since time.Time, handle func(dockerapi.Event)) error {
	if f.eventsFn != nil {
		return f.eventsFn(ctx, since, handle)
	}
	<-ctx.Done()
	return ctx.Err()
}

type fakeHost struct {
	cpuErr        error
	ioErr         error
	temperature   float64
	tempSupported bool
	rx, tx        uint64
	listeners     []hostports.Listener
	running       map[string]bool
	listenErr     error
}

func (h *fakeHost) CPUPercent(context.Context) (float64, error)         { return 12, h.cpuErr }
func (h *fakeHost) Memory(context.Context) (float64, uint64, error)     { return 40, 1000, nil }
func (h *fakeHost) DiskUsage(context.Context) (float64, float64, error) { return 80, 10, nil }
func (h *fakeHost) DiskIO(context.Context) (uint64, uint64, error)      { return 100, 100, h.ioErr }
func (h *fakeHost) Network(context.Context) (uint64, uint64, error)     { return h.rx, h.tx, nil }
func (h *fakeHost) Temperature(context.Context) (float64, bool, error) {
	return h.temperature, h.tempSupported, nil
}
func (h *fakeHost) Boot(context.Context) (time.Time, int64, error) { return testStart, 3600, nil }
func (h *fakeHost) Listeners(context.Context) ([]hostports.Listener, map[string]bool, error) {
	return h.listeners, h.running, h.listenErr
}

// gate는 실제 중앙 서버 앞에서 장애를 흉내 낸다.
type gate struct {
	next      http.Handler
	down      atomic.Bool
	loseReply atomic.Bool
	requests  atomic.Int64
}

func (g *gate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.requests.Add(1)
	if g.down.Load() {
		http.Error(w, "down", http.StatusServiceUnavailable)
		return
	}
	if g.loseReply.Load() {
		// 중앙 서버는 저장했지만 응답이 에이전트에 도달하지 않은 경우다.
		g.next.ServeHTTP(httptest.NewRecorder(), r)
		http.Error(w, "reply lost", http.StatusBadGateway)
		return
	}
	g.next.ServeHTTP(w, r)
}

type harness struct {
	url    string
	agent  *Agent
	docker *fakeDocker
	host   *fakeHost
	store  *central.Store
	gate   *gate
	clock  *testClock
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newHarness(t *testing.T, spoolBytes int64, probes ...ProbeConfig) *harness {
	t.Helper()
	store, err := central.OpenStore(filepath.Join(t.TempDir(), "central.db"), protocol.ModeLive)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	if err := store.Enroll(context.Background(), "home-1", "token"); err != nil {
		t.Fatal(err)
	}
	server := central.NewServer(store, central.NewHub(), central.Config{})
	g := &gate{next: server.Handler()}
	ts := httptest.NewServer(g)
	t.Cleanup(ts.Close)

	docker := &fakeDocker{stats: map[string]dockerapi.Stats{}}
	host := &fakeHost{listenErr: hostports.ErrUnsupported}
	agent, err := New(Config{
		CentralURL: ts.URL, ServerID: "home-1", Token: "token",
		SpoolDir: filepath.Join(t.TempDir(), "spool"), SpoolBytes: spoolBytes, Probes: probes,
	}, docker, host)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{t: testStart}
	agent.now = clock.now
	return &harness{url: ts.URL, agent: agent, docker: docker, host: host, store: store, gate: g, clock: clock}
}

// drain은 대기열이 빌 때까지 보낸다.
func (h *harness) drain(t *testing.T) {
	t.Helper()
	for {
		sent, err := h.agent.sendOnce(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !sent {
			return
		}
	}
}

func (h *harness) flush(t *testing.T) {
	t.Helper()
	if err := h.agent.flush(context.Background(), true); err != nil {
		t.Fatal(err)
	}
}

func (h *harness) events(t *testing.T) []central.EventRow {
	t.Helper()
	events, err := h.store.Events(context.Background(), "home-1", central.EventFilter{
		From: testStart.Add(-time.Hour), To: testStart.Add(24 * time.Hour), Limit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	return events
}

func api(id, ip string) protocol.Container {
	return protocol.Container{
		ID: id, Name: "blog-api-1", Project: "blog", Service: "api", Replica: 1, State: "running", Health: "none",
		Networks: []protocol.NetworkAttachment{{Network: "blog_default", IP: ip}},
	}
}

func TestRecreatedContainerKeepsServiceHistory(t *testing.T) {
	h := newHarness(t, 1<<30)
	ctx := context.Background()
	h.docker.containers = []protocol.Container{api("aaa", "172.20.0.3")}
	h.docker.stats["aaa"] = dockerapi.Stats{MemUsage: 100, MemLimit: 1000}
	h.agent.collectTopology(ctx)
	h.agent.collectStats(ctx)
	h.flush(t)

	h.clock.advance(time.Minute)
	h.docker.mu.Lock()
	h.docker.containers = []protocol.Container{api("bbb", "172.20.0.9")}
	h.docker.stats["bbb"] = dockerapi.Stats{MemUsage: 200, MemLimit: 1000}
	h.docker.mu.Unlock()
	h.agent.collectTopology(ctx)
	h.agent.collectStats(ctx)
	h.flush(t)
	h.drain(t)

	now := h.clock.now()
	view, err := h.store.Topology(ctx, "home-1", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	service := view.Projects[0].Services[0]
	if service.Key != "blog/api" || service.Containers[0].ID != "bbb" {
		t.Fatalf("서비스 = %+v", service)
	}
	series, _, err := h.store.Samples(ctx, "home-1", "blog/api#1", []string{"container.mem.bytes"}, testStart.Add(-time.Minute), now.Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(series[0].Points); got != 2 {
		t.Errorf("재생성 전후 지표가 이어지지 않았다: %d점", got)
	}
}

func TestDisconnectBuffersAndResendsWithoutDuplicates(t *testing.T) {
	h := newHarness(t, 1<<30)
	ctx := context.Background()

	h.gate.down.Store(true)
	for i := 0; i < 5; i++ {
		h.agent.handleDockerEvent(dockerapi.Event{
			Type: "container", Action: "start", ActorID: fmt.Sprintf("id%d", i),
			Name: fmt.Sprintf("web-%d", i), Service: fmt.Sprintf("s%d", i), Project: "p", At: testStart.Add(time.Duration(i) * time.Second),
		})
		h.flush(t)
	}
	if _, err := h.agent.sendOnce(ctx); err == nil {
		t.Fatal("중앙 서버가 죽었는데 전송이 성공했다")
	}
	if got := h.agent.spool.Count(); got != 5 {
		t.Fatalf("대기 = %d", got)
	}

	// 복구 직후 응답이 유실되어 같은 배치를 다시 보내는 상황을 포함한다.
	h.gate.down.Store(false)
	h.gate.loseReply.Store(true)
	if _, err := h.agent.sendOnce(ctx); err == nil {
		t.Fatal("응답 유실을 성공으로 처리했다")
	}
	if got := h.agent.spool.Count(); got != 5 {
		t.Fatalf("응답을 받지 못한 배치를 지웠다: %d", got)
	}
	h.gate.loseReply.Store(false)
	h.drain(t)

	if got := len(h.events(t)); got != 5 {
		t.Errorf("이벤트 = %d, want 5 (중복 없음)", got)
	}
}

func TestSpoolOverflowDropsMetricsFirstAndRecordsGap(t *testing.T) {
	h := newHarness(t, 6000)
	h.agent.mu.Lock()
	h.agent.pending.Events = []protocol.Event{{OccurredAt: testStart, Category: protocol.CategoryOps, Type: "container.die",
		Severity: protocol.SeverityCritical, Summary: "오래된 이벤트", Evidence: "docker-event"}}
	h.agent.mu.Unlock()
	h.gate.down.Store(true)

	for i := 0; i < 8; i++ {
		h.agent.mu.Lock()
		for j := 0; j < 20; j++ {
			h.agent.addSample(testStart.Add(time.Duration(i)*time.Minute), protocol.HostScope, fmt.Sprintf("m%d", j), 1)
		}
		h.agent.mu.Unlock()
		h.flush(t)
	}
	h.gate.down.Store(false)
	h.flush(t)
	h.drain(t)

	events := h.events(t)
	if len(events) != 1 || events[0].Summary != "오래된 이벤트" {
		t.Errorf("지표보다 먼저 이벤트를 버렸다: %+v", events)
	}
	gaps, err := h.store.Gaps(context.Background(), "home-1", testStart.Add(-time.Hour), testStart.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) == 0 {
		t.Fatal("버린 내용이 공백으로 기록되지 않았다")
	}
	if gaps[0].Dropped == 0 || gaps[0].Source != "spool" {
		t.Errorf("gap = %+v", gaps[0])
	}
}

func TestSpoolDeletesOldestEventsLastAndReports(t *testing.T) {
	spool, err := OpenSpool(t.TempDir(), 300)
	if err != nil {
		t.Fatal(err)
	}
	var gaps []protocol.Gap
	for i := 1; i <= 6; i++ {
		got, err := spool.Put(1, protocol.Batch{Epoch: "e", Seq: uint64(i), Events: []protocol.Event{{OccurredAt: testStart, Summary: "event"}}})
		if err != nil {
			t.Fatal(err)
		}
		gaps = append(gaps, got...)
	}
	if len(gaps) == 0 {
		t.Fatal("이벤트를 버렸는데 기록이 없다")
	}
	_, batch, ok := spool.Oldest()
	if !ok || batch.Seq == 1 {
		t.Errorf("오래된 배치부터 지워야 한다: %+v", batch.Seq)
	}
}

func TestEventBurstIsAggregatedAndCountsArePreserved(t *testing.T) {
	limiter := newEventLimiter()
	event := protocol.Event{ServiceKey: "blog/api", Type: "container.die", Summary: "종료"}
	admitted := 0
	const total = 1000
	for i := 0; i < total; i++ {
		if limiter.admit(event, testStart.Add(time.Duration(i)*time.Millisecond)) {
			admitted++
		}
	}
	rows := limiter.drain(testStart.Add(time.Minute))
	if admitted != limiterBurst {
		t.Errorf("admitted = %d", admitted)
	}
	if len(rows) != 1 || rows[0].Aggregated != total-limiterBurst {
		t.Fatalf("집계 = %+v", rows)
	}
	if admitted+rows[0].Aggregated != total {
		t.Error("건수가 사라졌다")
	}
}

func TestLimiterKeepsCountsAcrossWindows(t *testing.T) {
	limiter := newEventLimiter()
	event := protocol.Event{ServiceKey: "s", Type: "t"}
	admitted := 0
	for i := 0; i < 50; i++ {
		if limiter.admit(event, testStart.Add(time.Duration(i)*time.Second)) {
			admitted++
		}
	}
	aggregated := 0
	for _, row := range limiter.drain(testStart.Add(time.Hour)) {
		aggregated += row.Aggregated
	}
	if admitted+aggregated != 50 {
		t.Errorf("admitted %d + aggregated %d != 50", admitted, aggregated)
	}
}

func TestConvertEvent(t *testing.T) {
	services := map[string]string{"ccc": "blog/api"}
	cases := []struct {
		name     string
		event    dockerapi.Event
		keep     bool
		kind     string
		severity protocol.Severity
		service  string
	}{
		{"exec 이벤트 제외", dockerapi.Event{Type: "container", Action: "exec_create: /bin/sh"}, false, "", "", ""},
		{"exec die 제외", dockerapi.Event{Type: "container", Action: "exec_die"}, false, "", "", ""},
		{"정상 종료", dockerapi.Event{Type: "container", Action: "die", ExitCode: "0", Project: "blog", Service: "api"}, true, "container.die", protocol.SeverityInfo, "blog/api"},
		{"비정상 종료", dockerapi.Event{Type: "container", Action: "die", ExitCode: "137", Project: "blog", Service: "api"}, true, "container.die", protocol.SeverityCritical, "blog/api"},
		{"OOM", dockerapi.Event{Type: "container", Action: "oom", Project: "blog", Service: "api"}, true, "container.oom", protocol.SeverityCritical, "blog/api"},
		{"헬스 unhealthy", dockerapi.Event{Type: "container", Action: "health_status: unhealthy", Project: "blog", Service: "api"}, true, "container.health", protocol.SeverityWarning, "blog/api"},
		{"헬스 healthy", dockerapi.Event{Type: "container", Action: "health_status: healthy", Project: "blog", Service: "api"}, true, "container.health", protocol.SeverityInfo, "blog/api"},
		{"Compose 없는 컨테이너", dockerapi.Event{Type: "container", Action: "start", Name: "minio"}, true, "container.start", protocol.SeverityInfo, "_standalone/minio"},
		{"네트워크 연결", dockerapi.Event{Type: "network", Action: "connect", Name: "proxy", ContainerID: "ccc"}, true, "network.connect", protocol.SeverityInfo, "blog/api"},
		{"이미지 이벤트 제외", dockerapi.Event{Type: "image", Action: "pull"}, false, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, keep := convertEvent(tc.event, services)
			if keep != tc.keep {
				t.Fatalf("keep = %v", keep)
			}
			if !keep {
				return
			}
			if got.Type != tc.kind || got.Severity != tc.severity || got.ServiceKey != tc.service {
				t.Errorf("got = %+v", got)
			}
			if got.DurationMS != nil || got.Bytes != nil {
				t.Error("측정하지 않은 값을 채웠다")
			}
			if got.Summary == "" {
				t.Error("내용이 비어 있다")
			}
		})
	}
}

func hostMetric(t *testing.T, h *harness, name string) (float64, bool) {
	t.Helper()
	servers, err := h.store.Servers(context.Background(), h.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	value, ok := servers[0].Metrics[name]
	return value.Value, ok
}

func collectorOf(t *testing.T, h *harness, name string) protocol.CollectorStatus {
	t.Helper()
	servers, err := h.store.Servers(context.Background(), h.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	for _, collector := range servers[0].Collectors {
		if collector.Name == name {
			return collector
		}
	}
	t.Fatalf("수집기 %s 없음", name)
	return protocol.CollectorStatus{}
}

func TestMissingValuesAreDistinguished(t *testing.T) {
	h := newHarness(t, 1<<30)
	ctx := context.Background()
	h.host.cpuErr = errors.New("읽기 실패")

	h.agent.collectHost(ctx)
	h.agent.collectTemperature(ctx)
	h.clock.advance(10 * time.Second)
	// 네트워크 카운터가 그대로면 측정된 0이다.
	h.agent.collectHost(ctx)
	h.flush(t)
	h.drain(t)

	if _, ok := hostMetric(t, h, "host.cpu.percent"); ok {
		t.Error("수집 실패인데 값이 있다")
	}
	if status := collectorOf(t, h, "cpu"); status.State != protocol.CollectorError || status.Error == "" {
		t.Errorf("cpu = %+v", status)
	}
	if _, ok := hostMetric(t, h, "host.temperature"); ok {
		t.Error("미지원인데 값이 있다")
	}
	if status := collectorOf(t, h, "temperature"); status.State != protocol.CollectorUnsupported {
		t.Errorf("temperature = %+v", status)
	}
	if value, ok := hostMetric(t, h, "host.net.rx_bps"); !ok || value != 0 {
		t.Errorf("측정된 0이 사라졌다: %v %v", value, ok)
	}
	if status := collectorOf(t, h, "memory"); status.State != protocol.CollectorOK {
		t.Errorf("다른 수집기까지 실패했다: %+v", status)
	}
}

func TestSupportedTemperatureIsReported(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.host.temperature, h.host.tempSupported = 51, true
	h.agent.collectTemperature(context.Background())
	h.flush(t)
	h.drain(t)
	if value, ok := hostMetric(t, h, "host.temperature"); !ok || value != 51 {
		t.Errorf("온도 = %v %v", value, ok)
	}
}

func TestDockerDownIsReportedByLiveAgent(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.docker.pingErr = errors.New("connection refused")
	h.docker.topoErr = errors.New("connection refused")
	h.agent.collectTopology(context.Background())
	h.flush(t)
	h.drain(t)
	servers, err := h.store.Servers(context.Background(), h.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if servers[0].Reachability.State != "docker_down" {
		t.Errorf("reachability = %+v", servers[0].Reachability)
	}
}

func TestCPUPercentUsesDeltas(t *testing.T) {
	h := newHarness(t, 1<<30)
	ctx := context.Background()
	h.docker.containers = []protocol.Container{api("aaa", "")}
	h.docker.stats["aaa"] = dockerapi.Stats{CPUTotal: 100, SystemCPU: 1000, OnlineCPUs: 4, NetRx: 0}
	h.agent.collectTopology(ctx)
	h.agent.collectStats(ctx)

	h.clock.advance(15 * time.Second)
	h.docker.mu.Lock()
	h.docker.stats["aaa"] = dockerapi.Stats{CPUTotal: 200, SystemCPU: 2000, OnlineCPUs: 4, NetRx: 1500}
	h.docker.mu.Unlock()
	h.agent.collectStats(ctx)
	h.flush(t)
	h.drain(t)

	series, _, err := h.store.Samples(ctx, "home-1", "blog/api#1", []string{"container.cpu.percent", "container.net.rx_bps"}, testStart, testStart.Add(time.Minute), 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(series[0].Points) != 1 || series[0].Points[0].Value != 40 {
		t.Errorf("CPU = %+v (첫 수집에는 값이 없고 이후 10%%×4코어)", series[0].Points)
	}
	if len(series[1].Points) != 1 || series[1].Points[0].Value != 100 {
		t.Errorf("수신 = %+v", series[1].Points)
	}
}

func TestStoppedContainerIsNotQueriedForStats(t *testing.T) {
	h := newHarness(t, 1<<30)
	ctx := context.Background()
	stopped := api("aaa", "")
	stopped.State = "exited"
	h.docker.containers = []protocol.Container{stopped}
	h.agent.collectTopology(ctx)
	h.agent.collectStats(ctx)
	h.flush(t)
	h.drain(t)
	series, _, err := h.store.Samples(ctx, "home-1", "blog/api#1", []string{"container.mem.bytes"}, testStart.Add(-time.Minute), testStart.Add(time.Minute), 60)
	if err != nil {
		t.Fatal(err)
	}
	if len(series[0].Points) != 0 {
		t.Errorf("중지된 컨테이너의 값을 만들었다: %+v", series[0].Points)
	}
}

func probeFor(t *testing.T, targetPort int, service string, containers ...protocol.Container) protocol.Probe {
	t.Helper()
	h := newHarness(t, 1<<30)
	h.agent.cfg.Probes = []ProbeConfig{{Service: service, Port: targetPort, Path: "/health", TimeoutMS: 200}}
	h.docker.containers = containers
	h.agent.collectTopology(context.Background())
	h.agent.runProbes(context.Background())
	return h.agent.pending.Probes[0]
}

func portOf(t *testing.T, ts *httptest.Server) int {
	t.Helper()
	parsed, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func TestProbeDistinguishesFailureKinds(t *testing.T) {
	running := api("aaa", "127.0.0.1")

	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("경로 = %s", r.URL.Path)
		}
	}))
	defer healthy.Close()
	if p := probeFor(t, portOf(t, healthy), "blog/api", running); !p.OK || p.LatencyMS == nil {
		t.Errorf("정상 = %+v", p)
	}

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()
	if p := probeFor(t, portOf(t, broken), "blog/api", running); p.OK || p.Failure != "status" || p.LatencyMS == nil {
		t.Errorf("500 = %+v", p)
	}

	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer hung.Close()
	defer close(release)
	if p := probeFor(t, portOf(t, hung), "blog/api", running); p.OK || p.Failure != "timeout" || p.LatencyMS != nil {
		t.Errorf("무응답은 소요 시간을 측정값으로 남기면 안 된다: %+v", p)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedPort := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	if p := probeFor(t, closedPort, "blog/api", running); p.OK || p.Failure != "refused" || p.LatencyMS != nil {
		t.Errorf("연결 거부 = %+v", p)
	}

	stopped := running
	stopped.State = "exited"
	if p := probeFor(t, closedPort, "blog/api", stopped); p.OK || p.Failure != "no_container" {
		t.Errorf("중지된 컨테이너 = %+v", p)
	}
}

func TestRunningButUnresponsiveReachesCentralAsDown(t *testing.T) {
	release := make(chan struct{})
	hung := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer hung.Close()
	defer close(release)

	h := newHarness(t, 1<<30, ProbeConfig{Service: "blog/api", Port: portOf(t, hung), Path: "/health", TimeoutMS: 100})
	ctx := context.Background()
	h.docker.containers = []protocol.Container{api("aaa", "127.0.0.1")}
	h.agent.collectTopology(ctx)
	for i := 0; i < 3; i++ {
		h.clock.advance(30 * time.Second)
		h.agent.runProbes(ctx)
		h.flush(t)
	}
	h.drain(t)

	view, err := h.store.Topology(ctx, "home-1", nil, h.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	service := view.Projects[0].Services[0]
	if service.Runtime != "running" || service.Response != central.ResponseDown || service.Status != central.StatusCritical {
		t.Errorf("실행 중·무응답 판정 실패: %+v", service)
	}
	servers, _ := h.store.Servers(ctx, h.clock.now())
	if servers[0].Reachability.State != "ok" {
		t.Errorf("서비스 장애를 서버 장애로 표시했다: %+v", servers[0].Reachability)
	}
}

func TestDowntimeIsRecordedAsGap(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.agent.markAlive()
	h.clock.advance(10 * time.Minute)
	h.agent.recordDowntime()
	h.flush(t)
	h.drain(t)
	gaps, err := h.store.Gaps(context.Background(), "home-1", testStart.Add(-time.Hour), testStart.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 || gaps[0].Source != "agent" {
		t.Fatalf("gaps = %+v", gaps)
	}
	if !strings.Contains(gaps[0].Reason, "원인은 알 수 없습니다") {
		t.Errorf("원인을 단정했다: %s", gaps[0].Reason)
	}
}

func TestShortRestartIsNotAGap(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.agent.markAlive()
	h.clock.advance(5 * time.Second)
	h.agent.recordDowntime()
	if len(h.agent.pending.Gaps) != 0 {
		t.Errorf("gaps = %+v", h.agent.pending.Gaps)
	}
}

func TestDockerEventStreamLossRecordsGap(t *testing.T) {
	h := newHarness(t, 1<<30)
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.docker.eventsFn = func(_ context.Context, _ time.Time, _ func(dockerapi.Event)) error {
		if calls.Add(1) == 1 {
			// 재연결을 기다리는 동안 시간이 충분히 지난 상황을 만든다.
			time.AfterFunc(100*time.Millisecond, func() { h.clock.advance(time.Minute) })
			return errors.New("stream closed")
		}
		cancel()
		return nil
	}
	h.agent.watchEvents(ctx)
	if calls.Load() < 2 {
		t.Fatal("재연결하지 않았다")
	}
	h.agent.mu.Lock()
	defer h.agent.mu.Unlock()
	if len(h.agent.pending.Gaps) != 1 || h.agent.pending.Gaps[0].Source != "docker-events" {
		t.Errorf("gaps = %+v", h.agent.pending.Gaps)
	}
}

func TestShortEventStreamLossIsNotAGap(t *testing.T) {
	h := newHarness(t, 1<<30)
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.docker.eventsFn = func(_ context.Context, _ time.Time, _ func(dockerapi.Event)) error {
		if calls.Add(1) == 1 {
			return errors.New("stream closed")
		}
		cancel()
		return nil
	}
	h.agent.watchEvents(ctx)
	if len(h.agent.pending.Gaps) != 0 {
		t.Errorf("gaps = %+v", h.agent.pending.Gaps)
	}
}

func TestReplayedEventsAreNotRecordedTwice(t *testing.T) {
	h := newHarness(t, 1<<30)
	first := testStart.Add(time.Second)
	second := testStart.Add(2 * time.Second)
	event := func(at time.Time) dockerapi.Event {
		return dockerapi.Event{Type: "container", Action: "start", Name: "web", Service: "web", Project: "blog", At: at}
	}
	var calls atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.docker.eventsFn = func(_ context.Context, _ time.Time, handle func(dockerapi.Event)) error {
		switch calls.Add(1) {
		case 1:
			handle(event(first))
			return errors.New("stream closed")
		default:
			// 재연결하면 같은 초의 이벤트부터 다시 온다.
			handle(event(first))
			handle(event(second))
			cancel()
			return nil
		}
	}
	h.agent.watchEvents(ctx)
	if got := len(h.agent.pending.Events); got != 2 {
		t.Errorf("events = %d, want 2", got)
	}
}

func TestSpoolWriteFailureIsRecordedAsGap(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.agent.mu.Lock()
	h.agent.addSample(testStart, protocol.HostScope, "m", 1)
	h.agent.mu.Unlock()
	h.agent.spool.dir = filepath.Join(t.TempDir(), "missing", "dir")
	if err := h.agent.flush(context.Background(), false); err == nil {
		t.Fatal("저장 실패가 오류로 돌아오지 않았다")
	}
	h.agent.mu.Lock()
	defer h.agent.mu.Unlock()
	if len(h.agent.pending.Gaps) != 1 || h.agent.pending.Gaps[0].Dropped != 1 {
		t.Errorf("gaps = %+v", h.agent.pending.Gaps)
	}
}

func TestUsesHTTPSOutsideLoopback(t *testing.T) {
	cases := []struct {
		url string
		ok  bool
	}{
		{"https://central.example.test", true},
		{"http://127.0.0.1:8787", true},
		{"http://localhost:8787", true},
		{"http://central.example.test", false},
		{"http://192.168.0.10:8787", false},
		{"ftp://central.example.test", false},
		{"not a url", false},
	}
	for _, tc := range cases {
		if err := ValidateCentralURL(tc.url); (err == nil) != tc.ok {
			t.Errorf("ValidateCentralURL(%q) = %v", tc.url, err)
		}
	}
}

func TestTopologyIsResentPeriodicallyEvenWhenUnchanged(t *testing.T) {
	h := newHarness(t, 1<<30)
	ctx := context.Background()
	h.docker.containers = []protocol.Container{api("aaa", "")}
	h.agent.collectTopology(ctx)
	if h.agent.pending.Topology == nil {
		t.Fatal("첫 스냅샷이 없다")
	}
	h.flush(t)
	h.agent.collectTopology(ctx)
	if h.agent.pending.Topology != nil {
		t.Error("바뀌지 않았는데 바로 다시 보냈다")
	}
	h.clock.advance(topologyResendEvery)
	h.agent.collectTopology(ctx)
	if h.agent.pending.Topology == nil {
		t.Error("주기적 재전송이 없다")
	}
}

// readLogs는 화면처럼 로그 스트림을 읽어 끝날 때까지 모은다.
func readLogs(t *testing.T, ctx context.Context, url string) []protocol.LogChunk {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var chunks []protocol.LogChunk
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 1<<20), 1<<24)
	for scanner.Scan() {
		line, found := strings.CutPrefix(scanner.Text(), "data: ")
		if !found {
			continue
		}
		var chunk protocol.LogChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, chunk)
		if chunk.Done {
			break
		}
	}
	return chunks
}

func (h *harness) startPolling(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.agent.pollTasks(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		chunks := readLogs(t, context.Background(), h.url+"/api/v1/servers/home-1/logs?service=probe/check")
		if len(chunks) == 1 && !strings.Contains(chunks[0].Error, "연결되어 있지 않아") {
			return cancel
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("에이전트가 작업을 받지 않았다")
	return cancel
}

func TestLogsAreMaskedBeforeLeavingTheAgent(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.docker.containers = []protocol.Container{api("aaa", "")}
	h.agent.collectTopology(context.Background())
	var gotID atomic.Value
	h.docker.logsFn = func(_ context.Context, id string, _ dockerapi.LogOptions, emit func(dockerapi.LogEntry) bool) error {
		gotID.Store(id)
		emit(dockerapi.LogEntry{At: testStart, Stream: "stdout", Text: "login password=abc123 ok"})
		emit(dockerapi.LogEntry{At: testStart, Stream: "stderr", Text: "Authorization: Bearer abc123"})
		return nil
	}
	h.startPolling(t)
	chunks := readLogs(t, context.Background(), h.url+"/api/v1/servers/home-1/logs?service=blog/api")

	body, _ := json.Marshal(chunks)
	if strings.Contains(string(body), "abc123") {
		t.Errorf("민감정보가 전달됐다: %s", body)
	}
	if !strings.Contains(string(body), "가려짐") {
		t.Errorf("가림 표시가 없다: %s", body)
	}
	if gotID.Load() != "aaa" {
		t.Errorf("다른 컨테이너를 읽었다: %v", gotID.Load())
	}
}

func TestSearchCannotFindMaskedValues(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.docker.containers = []protocol.Container{api("aaa", "")}
	h.agent.collectTopology(context.Background())
	h.docker.logsFn = func(_ context.Context, _ string, _ dockerapi.LogOptions, emit func(dockerapi.LogEntry) bool) error {
		emit(dockerapi.LogEntry{Text: "password=abc123"})
		emit(dockerapi.LogEntry{Text: "ERROR something failed"})
		return nil
	}
	h.startPolling(t)
	count := func(query string) int {
		total := 0
		for _, chunk := range readLogs(t, context.Background(), h.url+"/api/v1/servers/home-1/logs?service=blog/api&q="+query) {
			total += len(chunk.Lines)
		}
		return total
	}
	if got := count("abc123"); got != 0 {
		t.Errorf("가려진 값을 검색으로 찾았다: %d", got)
	}
	if got := count("error"); got != 1 {
		t.Errorf("검색 결과 = %d", got)
	}
}

func TestLogStreamStopsWhenViewerLeaves(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.docker.containers = []protocol.Container{api("aaa", "")}
	h.agent.collectTopology(context.Background())
	stopped := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	h.docker.logsFn = func(ctx context.Context, _ string, options dockerapi.LogOptions, emit func(dockerapi.LogEntry) bool) error {
		if !options.Follow {
			return nil
		}
		defer close(stopped)
		for {
			once.Do(func() { close(started) })
			if !emit(dockerapi.LogEntry{Text: "line"}) {
				return nil
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	h.startPolling(t)

	viewer, leave := context.WithCancel(context.Background())
	defer leave()
	go func() {
		request, _ := http.NewRequestWithContext(viewer, http.MethodGet, h.url+"/api/v1/servers/home-1/logs?service=blog/api&follow=true", nil)
		if response, err := http.DefaultClient.Do(request); err == nil {
			io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("로그 스트림이 시작되지 않았다")
	}
	leave()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("화면을 떠났는데 에이전트가 계속 읽는다")
	}
}

func TestLogRequestOutsideTopologyIsRefused(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.docker.containers = []protocol.Container{api("aaa", "")}
	h.agent.collectTopology(context.Background())
	called := atomic.Bool{}
	h.docker.logsFn = func(context.Context, string, dockerapi.LogOptions, func(dockerapi.LogEntry) bool) error {
		called.Store(true)
		return nil
	}
	h.startPolling(t)
	called.Store(false)
	for _, query := range []string{"service=other/service", "service=blog/api&container=bbb"} {
		chunks := readLogs(t, context.Background(), h.url+"/api/v1/servers/home-1/logs?"+query)
		if len(chunks) != 1 || !strings.Contains(chunks[0].Error, "구성에 없는") {
			t.Errorf("%s: %+v", query, chunks)
		}
	}
	if called.Load() {
		t.Error("구성에 없는 컨테이너의 로그를 읽었다")
	}
}

func TestLogsWithoutAgentFailImmediately(t *testing.T) {
	h := newHarness(t, 1<<30)
	started := time.Now()
	chunks := readLogs(t, context.Background(), h.url+"/api/v1/servers/home-1/logs?service=blog/api")
	if len(chunks) != 1 || !chunks[0].Done || !strings.Contains(chunks[0].Error, "연결되어 있지 않아") {
		t.Errorf("chunks = %+v", chunks)
	}
	if time.Since(started) > time.Second {
		t.Errorf("바로 실패하지 않았다: %v", time.Since(started))
	}
}

func TestExcessLogLinesAreCountedNotHidden(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.docker.containers = []protocol.Container{api("aaa", "")}
	h.agent.collectTopology(context.Background())
	const total = 5000
	h.docker.logsFn = func(_ context.Context, _ string, _ dockerapi.LogOptions, emit func(dockerapi.LogEntry) bool) error {
		for i := 0; i < total; i++ {
			if !emit(dockerapi.LogEntry{Text: "line"}) {
				return nil
			}
		}
		return nil
	}
	h.startPolling(t)
	lines, dropped := 0, 0
	for _, chunk := range readLogs(t, context.Background(), h.url+"/api/v1/servers/home-1/logs?follow=true&service=blog/api") {
		lines += len(chunk.Lines)
		dropped += chunk.Dropped
	}
	if lines+dropped != total {
		t.Errorf("lines %d + dropped %d != %d", lines, dropped, total)
	}
	if dropped == 0 {
		t.Error("한도를 넘었는데 누락이 표시되지 않았다")
	}
}

func TestPastLogsKeepTheNewestLines(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.docker.containers = []protocol.Container{api("aaa", "")}
	h.agent.collectTopology(context.Background())
	const total = 5000
	h.docker.logsFn = func(_ context.Context, _ string, _ dockerapi.LogOptions, emit func(dockerapi.LogEntry) bool) error {
		for i := 0; i < total; i++ {
			if !emit(dockerapi.LogEntry{Text: fmt.Sprintf("line %d", i)}) {
				return nil
			}
		}
		return nil
	}
	h.startPolling(t)
	var last string
	lines, dropped := 0, 0
	for _, chunk := range readLogs(t, context.Background(), h.url+"/api/v1/servers/home-1/logs?tail=5000&service=blog/api") {
		lines += len(chunk.Lines)
		dropped += chunk.Dropped
		if n := len(chunk.Lines); n > 0 {
			last = chunk.Lines[n-1].Text
		}
	}
	if lines != total || dropped != 0 {
		t.Errorf("lines = %d, dropped = %d", lines, dropped)
	}
	if last != fmt.Sprintf("line %d", total-1) {
		t.Errorf("가장 최근 줄이 사라졌다: %q", last)
	}
}

func TestUnknownTaskIsNeverExecuted(t *testing.T) {
	h := newHarness(t, 1<<30)
	served := make(chan struct{}, 1)
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case served <- struct{}{}:
			json.NewEncoder(w).Encode(protocol.Task{ID: "x", Kind: "exec", Logs: &protocol.LogRequest{ServiceKey: "blog/api"}})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer central.Close()
	h.agent.cfg.CentralURL = central.URL
	called := atomic.Bool{}
	h.docker.logsFn = func(context.Context, string, dockerapi.LogOptions, func(dockerapi.LogEntry) bool) error {
		called.Store(true)
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	h.agent.pollTasks(ctx)
	if called.Load() {
		t.Error("알 수 없는 작업을 실행했다")
	}
	if len(served) != 1 {
		t.Error("작업을 받지 않았다")
	}
}

func TestBatchNeverContainsContainerSecrets(t *testing.T) {
	// 에이전트가 보내는 구조체에 환경 변수 필드 자체가 없음을 확인한다.
	body, err := json.Marshal(protocol.Container{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"env", "cmd", "labels", "args"} {
		if strings.Contains(strings.ToLower(string(body)), `"`+field+`"`) {
			t.Errorf("전송 구조체에 %s 필드가 있다", field)
		}
	}
}

func hostServiceNamed(t *testing.T, services []protocol.HostService, name string) protocol.HostService {
	t.Helper()
	for _, service := range services {
		if service.Name == name {
			return service
		}
	}
	t.Fatalf("%q 없음: %+v", name, services)
	return protocol.HostService{}
}

func TestHostServicesExcludeDockerAndLoopback(t *testing.T) {
	containers := []protocol.Container{{Ports: []protocol.PortBinding{{HostIP: "0.0.0.0", HostPort: 8080, ContainerPort: 80, Proto: "tcp"}}}}
	services := deriveHostServices([]hostports.Listener{
		{IP: "0.0.0.0", Port: 8080},
		{IP: "127.0.0.1", Port: 631, Process: "cupsd"},
		{IP: "0.0.0.0", Port: 9000, Process: "myapp"},
	}, nil, containers, nil)
	if len(services) != 1 || services[0].Name != "myapp" || services[0].Evidence != protocol.HostEvidenceOwner {
		t.Errorf("services = %+v", services)
	}
}

func TestHostServiceNameIsOnlyGuessedWithEvidence(t *testing.T) {
	services := deriveHostServices([]hostports.Listener{
		{IP: "0.0.0.0", Port: 80},
		{IP: "0.0.0.0", Port: 443},
		{IP: "0.0.0.0", Port: 9999},
	}, map[string]bool{"nginx": true}, nil, nil)
	nginx := hostServiceNamed(t, services, "nginx")
	if nginx.Evidence != protocol.HostEvidenceName || len(nginx.Ports) != 2 {
		t.Errorf("nginx = %+v", nginx)
	}
	unknown := hostServiceNamed(t, services, "")
	if unknown.Evidence != protocol.HostEvidenceUnknown || unknown.Ports[0].Port != 9999 {
		t.Errorf("모르는 포트에 이름을 붙였다: %+v", unknown)
	}
}

func TestAllLoopbackAddressesAreExcluded(t *testing.T) {
	services := deriveHostServices([]hostports.Listener{
		{IP: "127.0.0.53", Port: 53},
		{IP: "127.0.0.54", Port: 53},
		{IP: "::1", Port: 631},
	}, nil, nil, nil)
	if len(services) != 0 {
		t.Errorf("루프백을 공개 포트로 봤다: %+v", services)
	}
}

func TestAmbiguousPortIsNotGuessed(t *testing.T) {
	services := deriveHostServices([]hostports.Listener{{IP: "0.0.0.0", Port: 80}}, map[string]bool{"nginx": true, "apache2": true}, nil, nil)
	if len(services) != 1 || services[0].Name != "" || services[0].Evidence != protocol.HostEvidenceUnknown {
		t.Errorf("후보가 둘인데 하나로 단정했다: %+v", services)
	}
}

func TestWellKnownPortWithoutProcessIsNotNamed(t *testing.T) {
	services := deriveHostServices([]hostports.Listener{{IP: "0.0.0.0", Port: 80}}, map[string]bool{"sshd": true}, nil, nil)
	if services[0].Name != "" {
		t.Errorf("프로세스가 없는데 이름을 붙였다: %+v", services[0])
	}
}

func TestSocketOwnerWinsOverGuess(t *testing.T) {
	services := deriveHostServices([]hostports.Listener{{IP: "0.0.0.0", Port: 80, Process: "myproxy"}}, map[string]bool{"nginx": true}, nil, nil)
	if services[0].Name != "myproxy" || services[0].Evidence != protocol.HostEvidenceOwner {
		t.Errorf("services = %+v", services)
	}
}

func TestDeclaredServiceStaysVisibleWhenClosed(t *testing.T) {
	declared := []DeclaredHostService{{Name: "nginx", Ports: []int{80, 443}}}
	open := deriveHostServices([]hostports.Listener{{IP: "0.0.0.0", Port: 80}}, nil, nil, declared)
	if !open[0].Listening || open[0].Evidence != protocol.HostEvidenceConfig {
		t.Errorf("열림 = %+v", open[0])
	}
	closed := deriveHostServices(nil, nil, nil, declared)
	if len(closed) != 1 || closed[0].Listening || len(closed[0].Ports) != 2 {
		t.Errorf("닫힘 = %+v", closed)
	}
}

func TestDeclaredLoopbackServiceIsIncluded(t *testing.T) {
	declared := []DeclaredHostService{{Name: "local-db", Ports: []int{5432}}}
	services := deriveHostServices([]hostports.Listener{{IP: "127.0.0.1", Port: 5432}}, nil, nil, declared)
	if len(services) != 1 || services[0].Name != "local-db" || !services[0].Listening {
		t.Errorf("services = %+v", services)
	}
}

func TestHostServicesReachCentralAndChangesAreRecorded(t *testing.T) {
	h := newHarness(t, 1<<30)
	ctx := context.Background()
	h.host.listenErr = nil
	h.host.listeners = []hostports.Listener{{IP: "0.0.0.0", Port: 9000, Process: "myapp"}}
	h.docker.containers = []protocol.Container{api("aaa", "")}
	h.agent.collectTopology(ctx)
	if len(h.agent.pending.Events) != 0 {
		t.Errorf("첫 수집을 변화로 기록했다: %+v", h.agent.pending.Events)
	}
	h.flush(t)

	h.clock.advance(time.Minute)
	h.host.listeners = nil
	h.agent.collectTopology(ctx)
	if len(h.agent.pending.Events) != 1 || h.agent.pending.Events[0].Type != "host.port_closed" {
		t.Fatalf("닫힘 기록 = %+v", h.agent.pending.Events)
	}
	if h.agent.pending.Topology == nil {
		t.Error("호스트 포트가 바뀌었는데 구성이 전송되지 않았다")
	}
	h.flush(t)
	h.drain(t)
	events := h.events(t)
	if len(events) != 1 || events[0].ServiceKey != "_host/myapp" {
		t.Errorf("events = %+v", events)
	}
}

func TestHostScanFailureKeepsLastKnownServices(t *testing.T) {
	h := newHarness(t, 1<<30)
	ctx := context.Background()
	h.host.listenErr = nil
	h.host.listeners = []hostports.Listener{{IP: "0.0.0.0", Port: 9000, Process: "myapp"}}
	h.agent.collectTopology(ctx)

	h.host.listenErr = errors.New("읽기 실패")
	h.agent.collectTopology(ctx)
	if len(h.agent.topology.HostServices) != 1 {
		t.Errorf("수집 실패를 포트 닫힘으로 처리했다: %+v", h.agent.topology.HostServices)
	}
	if h.agent.collectors["host_services"].State != protocol.CollectorError {
		t.Errorf("collector = %+v", h.agent.collectors["host_services"])
	}
	for _, event := range h.agent.pending.Events {
		if event.Type == "host.port_closed" {
			t.Errorf("수집 실패인데 닫힘을 기록했다: %+v", event)
		}
	}
}

func TestUnsupportedPlatformIsNotAnError(t *testing.T) {
	h := newHarness(t, 1<<30)
	h.agent.collectTopology(context.Background())
	if h.agent.collectors["host_services"].State != protocol.CollectorUnsupported {
		t.Errorf("collector = %+v", h.agent.collectors["host_services"])
	}
}

func TestCollectRequestsReadsOnlyNewLinesAndDiscardsClientData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	line := `203.0.113.7 - - [06/Nov/2026:21:35:00 +0900] "GET /api/posts?token=abc HTTP/1.1" 200 12 "-" "curl"` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	instance := &Agent{
		now:           time.Now,
		collectors:    map[string]protocol.CollectorStatus{},
		flushSoon:     make(chan struct{}, 1),
		accessTailers: newAccessTailers([]AccessLogConfig{{Service: "nginx", Path: path}}),
	}
	instance.collectRequests(context.Background())
	if len(instance.pending.Requests) != 0 {
		t.Fatalf("지나간 로그를 읽었다: %+v", instance.pending.Requests)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := file.WriteString(line + "형식이 다른 줄\n"); err != nil {
		t.Fatal(err)
	}
	instance.collectRequests(context.Background())
	if len(instance.pending.Requests) != 1 || instance.pending.Requests[0].Path != "/api/posts" {
		t.Fatalf("요청 = %+v", instance.pending.Requests)
	}
	body, _ := json.Marshal(instance.pending.Requests)
	for _, secret := range []string{"203.0.113.7", "token", "abc", "curl"} {
		if strings.Contains(string(body), secret) {
			t.Errorf("%q가 전송 내용에 남았다: %s", secret, body)
		}
	}
	if instance.collectors["access_log"].State != protocol.CollectorOK {
		t.Errorf("수집 상태 = %+v", instance.collectors["access_log"])
	}
	select {
	case <-instance.flushSoon:
	default:
		t.Error("요청이 들어왔는데 전송을 앞당기지 않았다")
	}
}

func TestCollectRequestsCapsAndRecordsDropped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	instance := &Agent{
		now:           time.Now,
		collectors:    map[string]protocol.CollectorStatus{},
		flushSoon:     make(chan struct{}, 1),
		accessTailers: newAccessTailers([]AccessLogConfig{{Service: "nginx", Path: path}}),
	}
	instance.collectRequests(context.Background())
	line := `1.2.3.4 - - [06/Nov/2026:21:35:00 +0900] "GET / HTTP/1.1" 200 1 "-" "-"` + "\n"
	if err := os.WriteFile(path, []byte(strings.Repeat(line, maxRequestsPerFlush+50)), 0o644); err != nil {
		t.Fatal(err)
	}
	instance.collectRequests(context.Background())
	if len(instance.pending.Requests) != maxRequestsPerFlush || instance.requestsDropped != 50 {
		t.Fatalf("요청 %d개, 버림 %d개", len(instance.pending.Requests), instance.requestsDropped)
	}
	instance.pending.Requests = nil
	instance.collectRequests(context.Background())
	if len(instance.pending.Gaps) != 1 || instance.pending.Gaps[0].Dropped != 50 || instance.pending.Gaps[0].Source != "access_log" {
		t.Errorf("버린 요청을 공백으로 남기지 않았다: %+v", instance.pending.Gaps)
	}
}

func TestCollectRequestsReportsUnreadableLog(t *testing.T) {
	instance := &Agent{
		now:           time.Now,
		collectors:    map[string]protocol.CollectorStatus{},
		flushSoon:     make(chan struct{}, 1),
		accessTailers: newAccessTailers([]AccessLogConfig{{Service: "nginx", Path: filepath.Join(t.TempDir(), "none.log")}}),
	}
	instance.collectRequests(context.Background())
	if instance.collectors["access_log"].State != protocol.CollectorError {
		t.Errorf("읽지 못한 것을 수집 실패로 표시하지 않았다: %+v", instance.collectors["access_log"])
	}
}
