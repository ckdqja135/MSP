package demo

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ckdqja135/MSP/internal/central"
	"github.com/ckdqja135/MSP/internal/protocol"
)

var now = time.Date(2026, 11, 6, 3, 0, 0, 0, time.UTC)

func find(t *testing.T, topology *protocol.Topology, key string) protocol.Container {
	t.Helper()
	for _, c := range topology.Containers {
		if c.ServiceKey() == key {
			return c
		}
	}
	t.Fatalf("%s 없음", key)
	return protocol.Container{}
}

func TestRecreateChangesIDAndIPButKeepsServiceKey(t *testing.T) {
	s := New(now)
	before := find(t, s.topologyAt(s.origin.Add(appRecreate-time.Second)), "shop/app")
	after := find(t, s.topologyAt(s.origin.Add(appRecreate+time.Second)), "shop/app")
	if before.ID == after.ID {
		t.Error("재생성인데 컨테이너 ID가 같다")
	}
	if before.Networks[0].IP == after.Networks[0].IP {
		t.Error("재생성인데 IP가 같다")
	}
	if before.ServiceKey() != after.ServiceKey() {
		t.Error("서비스 키가 바뀌었다")
	}
}

func TestStoppedContainerProducesNoSamples(t *testing.T) {
	s := New(now)
	at := s.origin.Add(apiOOM + time.Second)
	for _, sample := range s.samplesAt(at) {
		if sample.Scope == protocol.ContainerScope("blog/api", 1) {
			t.Fatalf("종료된 컨테이너의 값을 만들었다: %+v", sample)
		}
	}
}

func TestTemperatureIsUnsupportedNotZero(t *testing.T) {
	s := New(now)
	for _, sample := range s.samplesAt(now) {
		if sample.Name == "host.temperature" {
			t.Fatal("미지원 지표의 값을 만들었다")
		}
	}
	found := false
	for _, status := range s.collectorStatus(now) {
		if status.Name == "temperature" {
			found = status.State == protocol.CollectorUnsupported
		}
	}
	if !found {
		t.Error("온도 수집기가 미지원으로 표시되지 않았다")
	}
}

func TestCollectFailureLeavesNoValue(t *testing.T) {
	s := New(now)
	at := s.origin.Add(diskIOFailFrom + time.Second)
	for _, sample := range s.samplesAt(at) {
		if sample.Name == "host.disk.read_bps" {
			t.Fatal("수집 실패 구간에 값을 만들었다")
		}
	}
	for _, status := range s.collectorStatus(at) {
		if status.Name == "disk_io" && status.State != protocol.CollectorError {
			t.Errorf("disk_io = %s", status.State)
		}
	}
}

func TestIdleNetworkIsARealZero(t *testing.T) {
	s := New(now)
	found := false
	for _, sample := range s.samplesAt(now) {
		if sample.Scope == protocol.ContainerScope("shop/cache", 1) && sample.Name == "container.net.rx_bps" {
			found = true
			if sample.Value != 0 {
				t.Errorf("value = %f", sample.Value)
			}
		}
	}
	if !found {
		t.Error("측정된 0이 빠졌다")
	}
}

func TestSeedProducesExpectedServerState(t *testing.T) {
	store, err := central.OpenStore(filepath.Join(t.TempDir(), "demo.db"), protocol.ModeDemo)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()

	s := New(now)
	_, err = s.Seed(ctx, now, func(ctx context.Context, batch protocol.Batch, receivedAt time.Time) error {
		_, err := store.Ingest(ctx, batch, receivedAt)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	view, err := store.Topology(ctx, ServerID, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]central.ServiceView{}
	for _, project := range view.Projects {
		for _, service := range project.Services {
			statuses[service.Key] = service
		}
	}
	api := statuses["blog/api"]
	if api.Status != central.StatusCritical || api.Runtime != "running" || api.Response != central.ResponseDown {
		t.Errorf("시작 시점 blog/api는 실행 중·무응답이어야 한다: %+v", api)
	}
	if web := statuses["blog/web"]; web.Status != central.StatusOK || web.Response != central.ResponseOK {
		t.Errorf("blog/web = %+v", web)
	}
	if cache := statuses["shop/cache"]; cache.Response != central.ResponseUnconfirmed {
		t.Errorf("프로브 없는 서비스 = %+v", cache)
	}
	if migrate := statuses["blog/migrate"]; migrate.Status != central.StatusStopped {
		t.Errorf("정상 종료한 일회성 작업 = %+v", migrate)
	}

	gaps, err := store.Gaps(ctx, ServerID, now.Add(-2*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 1 {
		t.Errorf("gaps = %+v", gaps)
	}

	servers, err := store.Servers(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if servers[0].Reachability.State != "ok" || servers[0].Mode != protocol.ModeDemo {
		t.Errorf("server = %+v", servers[0])
	}
}

func TestDemoLogsAreMaskedAndMatchTheScenario(t *testing.T) {
	s := New(now)
	lines := s.logLines("blog/api", "", s.origin, s.origin.Add(cycle))
	if len(lines) == 0 {
		t.Fatal("로그가 없다")
	}
	sawFatal := false
	for _, line := range lines {
		if strings.Contains(line.Text, "demo-password") || strings.Contains(line.Text, "demo-token") {
			t.Errorf("민감정보가 남았다: %s", line.Text)
		}
		sawFatal = sawFatal || strings.Contains(line.Text, "FATAL")
	}
	if !sawFatal {
		t.Error("장애 구간 로그가 없다")
	}
	if got := s.logLines("blog/migrate", "", s.origin, s.origin.Add(time.Minute)); len(got) != 0 {
		t.Errorf("실행 중이 아닌 컨테이너의 로그를 만들었다: %+v", got[0])
	}
	if got := s.logLines("blog/api", "fatal", s.origin, s.origin.Add(cycle)); len(got) == 0 || len(got) == len(lines) {
		t.Errorf("검색 결과 = %d", len(got))
	}
}
