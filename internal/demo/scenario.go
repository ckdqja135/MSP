// Package demo는 실제 서버 없이 화면을 확인하기 위한 가짜 데이터를 만든다.
//
// 데모 데이터는 데모 모드의 중앙 서버와 전용 DB에만 저장된다.
package demo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	ServerID = "demo-home"

	cycle         = 10 * time.Minute
	historyCycles = 6
	// 시작 직후 장애 상태를 바로 볼 수 있도록 응답 실패 구간에서 시작한다.
	startPhase  = 250 * time.Second
	sampleEvery = 15 * time.Second
	probeEvery  = 30 * time.Second
	tick        = 5 * time.Second
)

// 한 주기 안에서 blog/api에 일어나는 사건의 시점이다.
const (
	apiMemWarn   = 150 * time.Second
	apiHangStart = 180 * time.Second
	apiUnhealthy = 210 * time.Second
	apiOOM       = 330 * time.Second
	apiRestart   = 335 * time.Second
	apiHealthy   = 390 * time.Second
	appRecreate  = 480 * time.Second
)

type service struct {
	project, name, image string
	replica              int
	oneOff, healthcheck  bool
	networks             []string
	ports                []protocol.PortBinding
	deps                 []string
	cpu, memMB, limitMB  float64
	netKB                float64
	ipBase               int
}

var services = []service{
	{project: "blog", name: "api", image: "registry.test/blog-api:2.4", replica: 1, healthcheck: true,
		networks: []string{"blog_default", "proxy"}, deps: []string{"db"},
		ports: []protocol.PortBinding{{HostIP: "127.0.0.1", HostPort: 18080, ContainerPort: 8080, Proto: "tcp"}},
		cpu:   9, memMB: 180, limitMB: 512, netKB: 90, ipBase: 3},
	{project: "blog", name: "db", image: "postgres:16", replica: 1, healthcheck: true,
		networks: []string{"blog_default"},
		ports:    []protocol.PortBinding{{ContainerPort: 5432, Proto: "tcp"}},
		cpu:      4, memMB: 240, limitMB: 1024, netKB: 30, ipBase: 2},
	{project: "blog", name: "migrate", image: "registry.test/blog-api:2.4", replica: 1, oneOff: true,
		networks: []string{"blog_default"}, ipBase: 9},
	{project: "blog", name: "web", image: "registry.test/blog-web:1.8", replica: 1,
		networks: []string{"blog_default", "proxy"}, deps: []string{"api"},
		ports: []protocol.PortBinding{{ContainerPort: 3000, Proto: "tcp"}},
		cpu:   3, memMB: 96, limitMB: 256, netKB: 140, ipBase: 4},
	{project: "infra", name: "proxy", image: "caddy:2", replica: 1,
		networks: []string{"proxy"},
		ports: []protocol.PortBinding{
			{HostIP: "0.0.0.0", HostPort: 80, ContainerPort: 80, Proto: "tcp"},
			{HostIP: "0.0.0.0", HostPort: 443, ContainerPort: 443, Proto: "tcp"},
		},
		cpu: 2, memMB: 40, limitMB: 128, netKB: 260, ipBase: 2},
	{project: "shop", name: "app", image: "registry.test/shop-app:5.1", replica: 1, healthcheck: true,
		networks: []string{"proxy", "shop_default"}, deps: []string{"cache", "db"},
		ports: []protocol.PortBinding{{ContainerPort: 4000, Proto: "tcp"}},
		cpu:   12, memMB: 310, limitMB: 768, netKB: 120, ipBase: 5},
	{project: "shop", name: "cache", image: "redis:7", replica: 1,
		networks: []string{"shop_default"},
		ports:    []protocol.PortBinding{{ContainerPort: 6379, Proto: "tcp"}},
		cpu:      1, memMB: 32, limitMB: 128, ipBase: 3},
	{project: "shop", name: "db", image: "postgres:16", replica: 1, healthcheck: true,
		networks: []string{"shop_default"},
		ports:    []protocol.PortBinding{{ContainerPort: 5432, Proto: "tcp"}},
		cpu:      5, memMB: 280, limitMB: 1024, netKB: 45, ipBase: 2},
	{project: "", name: "minio-backup", image: "minio/minio:latest", ipBase: 8,
		networks: []string{"bridge"}},
}

var networks = []protocol.Network{
	{ID: "net-blog", Name: "blog_default", Driver: "bridge", Subnet: "172.20.0.0/16"},
	{ID: "net-proxy", Name: "proxy", Driver: "bridge", Subnet: "172.21.0.0/16"},
	{ID: "net-shop", Name: "shop_default", Driver: "bridge", Subnet: "172.22.0.0/16"},
	{ID: "net-bridge", Name: "bridge", Driver: "bridge", Subnet: "172.17.0.0/16"},
	{ID: "net-host", Name: "host", Driver: "host"},
	{ID: "net-none", Name: "none", Driver: "null"},
}

var subnetPrefix = map[string]string{
	"blog_default": "172.20.0.", "proxy": "172.21.0.", "shop_default": "172.22.0.", "bridge": "172.17.0.",
}

// Scenario는 시각을 넣으면 항상 같은 결과를 내는 결정적 시나리오다.
type Scenario struct {
	origin         time.Time
	gapFrom, gapTo time.Time
	gapSent        bool
	seq            uint64
}

func New(now time.Time) *Scenario {
	origin := now.Add(-historyCycles*cycle - startPhase).Truncate(time.Second)
	return &Scenario{
		origin:  origin,
		gapFrom: origin.Add(22 * time.Minute),
		gapTo:   origin.Add(25 * time.Minute),
	}
}

func (s *Scenario) position(t time.Time) (int, time.Duration) {
	elapsed := t.Sub(s.origin)
	if elapsed < 0 {
		return 0, 0
	}
	return int(elapsed / cycle), elapsed % cycle
}

func containerID(name string, generation int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", name, generation)))
	return hex.EncodeToString(sum[:])
}

func containerName(svc service) string {
	if svc.project == "" {
		return svc.name
	}
	return fmt.Sprintf("%s-%s-%d", svc.project, svc.name, svc.replica)
}

// appGeneration은 shop/app이 재생성될 때마다 늘어나 컨테이너 ID와 IP가 바뀌게 한다.
func appGeneration(n int, phase time.Duration) int {
	if phase >= appRecreate {
		return n + 1
	}
	return n
}

func (s *Scenario) topologyAt(t time.Time) *protocol.Topology {
	n, phase := s.position(t)
	boot := s.origin.Add(-2 * time.Hour)
	topology := &protocol.Topology{ObservedAt: t, Networks: networks}
	for _, svc := range services {
		c := protocol.Container{
			Name: containerName(svc), Project: svc.project, Service: svc.name, Replica: svc.replica, OneOff: svc.oneOff,
			Image: svc.image, ImageID: "sha256:" + containerID(svc.image, 0)[:32], State: "running", Health: "none",
			RestartPolicy: "unless-stopped", CreatedAt: boot, StartedAt: boot,
			Ports: svc.ports, DeclaredDependsOn: svc.deps,
		}
		if svc.healthcheck {
			c.Health = "healthy"
		}
		generation := 0
		ipOffset := 0
		switch {
		case svc.oneOff:
			c.State, c.RestartPolicy = "exited", "no"
		case svc.project == "":
			c.State, c.RestartPolicy = "exited", "no"
		case svc.project == "blog" && svc.name == "api":
			applyAPIState(&c, s, n, phase)
		case svc.project == "shop" && svc.name == "app":
			generation = appGeneration(n, phase)
			ipOffset = generation % 20
			if generation > 0 {
				recreated := s.origin.Add(time.Duration(generation-1)*cycle + appRecreate)
				c.CreatedAt, c.StartedAt = recreated, recreated
			}
		}
		c.ID = containerID(c.Name, generation)
		for _, network := range svc.networks {
			c.Networks = append(c.Networks, protocol.NetworkAttachment{
				Network: network, IP: fmt.Sprintf("%s%d", subnetPrefix[network], svc.ipBase+ipOffset),
			})
		}
		topology.Containers = append(topology.Containers, c)
	}
	topology.Normalize()
	return topology
}

func applyAPIState(c *protocol.Container, s *Scenario, n int, phase time.Duration) {
	c.RestartCount = n
	if phase >= apiRestart {
		c.RestartCount = n + 1
		c.StartedAt = s.origin.Add(time.Duration(n)*cycle + apiRestart)
	} else if n > 0 {
		c.StartedAt = s.origin.Add(time.Duration(n-1)*cycle + apiRestart)
	}
	switch {
	case phase >= apiHealthy:
	case phase >= apiRestart:
		c.Health = "starting"
	case phase >= apiOOM:
		c.State, c.ExitCode, c.OOMKilled = "exited", 137, true
		c.Health = "unhealthy"
	case phase >= apiUnhealthy:
		c.Health = "unhealthy"
	}
}

func wave(t time.Time, seconds, amplitude float64) float64 {
	return amplitude * math.Sin(float64(t.Unix())/seconds)
}

func (s *Scenario) samplesAt(t time.Time) []protocol.Sample {
	n, phase := s.position(t)
	pressure := 0.0
	if phase >= apiMemWarn-30*time.Second && phase < apiOOM {
		pressure = float64(phase-apiMemWarn+30*time.Second) / float64(apiOOM-apiMemWarn+30*time.Second)
	}
	host := []struct {
		name  string
		value float64
	}{
		{"host.cpu.percent", 22 + wave(t, 60, 7) + 18*pressure},
		{"host.mem.percent", 61 + wave(t, 300, 2) + 6*pressure},
		{"host.mem.used_bytes", (61 + wave(t, 300, 2) + 6*pressure) / 100 * 32e9},
		{"host.disk.percent", 82 + float64(n)*0.05},
		{"host.disk.read_bps", 4e6 + wave(t, 45, 2e6)},
		{"host.disk.write_bps", 9e6 + wave(t, 37, 4e6)},
		{"host.net.rx_bps", 3e6 + wave(t, 29, 1e6)},
		{"host.net.tx_bps", 5e6 + wave(t, 41, 2e6)},
	}
	samples := make([]protocol.Sample, 0, 64)
	for _, m := range host {
		samples = append(samples, protocol.Sample{At: t, Scope: protocol.HostScope, Name: m.name, Value: m.value})
	}

	topology := s.topologyAt(t)
	for _, c := range topology.Containers {
		// 중지된 컨테이너는 0을 만들지 않는다. 값이 없는 구간은 차트에서 끊겨 보여야 한다.
		if c.State != "running" {
			continue
		}
		svc := lookup(c.Project, c.Service)
		cpu := svc.cpu + wave(t, 53, svc.cpu*0.3)
		mem := svc.memMB + wave(t, 200, svc.memMB*0.03)
		if c.ServiceKey() == "blog/api" && pressure > 0 {
			cpu += 40 * pressure
			mem = svc.memMB + (svc.limitMB*0.99-svc.memMB)*pressure
		}
		scope := protocol.ContainerScope(c.ServiceKey(), c.Replica)
		values := []struct {
			name  string
			value float64
		}{
			{"container.cpu.percent", math.Max(cpu, 0)},
			{"container.mem.bytes", mem * 1e6},
			{"container.mem.limit_bytes", svc.limitMB * 1e6},
			{"container.net.rx_bps", math.Max(svc.netKB*1e3+wave(t, 31, svc.netKB*300), 0)},
			{"container.net.tx_bps", math.Max(svc.netKB*1.4e3+wave(t, 23, svc.netKB*400), 0)},
		}
		for _, v := range values {
			samples = append(samples, protocol.Sample{At: t, Scope: scope, Name: v.name, Value: v.value})
		}
	}
	return samples
}

func lookup(project, name string) service {
	for _, svc := range services {
		if svc.project == project && svc.name == name {
			return svc
		}
	}
	return service{}
}

func (s *Scenario) probesAt(t time.Time) []protocol.Probe {
	_, phase := s.position(t)
	latency := func(base float64) *int64 {
		value := int64(base + wave(t, 19, base*0.3))
		return &value
	}
	probes := []protocol.Probe{
		{At: t, ServiceKey: "blog/web", Vantage: protocol.VantageInternal, Target: "GET /", OK: true, LatencyMS: latency(24)},
		{At: t, ServiceKey: "shop/app", Vantage: protocol.VantageInternal, Target: "GET /healthz", OK: true, LatencyMS: latency(18)},
		{At: t, ServiceKey: "blog/web", Vantage: protocol.VantageExternal, Target: "GET https://blog.example.test/", OK: true, LatencyMS: latency(86)},
	}
	for _, vantage := range []protocol.Vantage{protocol.VantageInternal, protocol.VantageExternal} {
		target := "GET /health"
		base := 12.0
		if vantage == protocol.VantageExternal {
			target = "GET https://blog.example.test/api/health"
			base = 64
		}
		p := protocol.Probe{At: t, ServiceKey: "blog/api", Vantage: vantage, Target: target}
		switch {
		case phase >= apiHangStart && phase < apiOOM:
			p.Failure, p.TimeoutMS = "timeout", 5000
		case phase >= apiOOM && phase < apiHealthy:
			p.Failure = "refused"
		default:
			p.OK, p.LatencyMS = true, latency(base)
		}
		probes = append(probes, p)
	}
	return probes
}

func (s *Scenario) eventsBetween(from, to time.Time) []protocol.Event {
	var events []protocol.Event
	first, _ := s.position(from)
	last, _ := s.position(to)
	for n := first; n <= last; n++ {
		start := s.origin.Add(time.Duration(n) * cycle)
		apiID := containerID("blog-api-1", 0)
		appOld := containerID("shop-app-1", appGeneration(n, 0))
		appNew := containerID("shop-app-1", appGeneration(n, appRecreate))
		docker := func(offset time.Duration, kind string, severity protocol.Severity, key, target, id, summary, result string) protocol.Event {
			return protocol.Event{
				OccurredAt: start.Add(offset), Category: protocol.CategoryOps, Type: kind, Severity: severity,
				Source: "docker", Target: target, ServiceKey: key, ContainerID: id,
				Summary: summary, Result: result, Evidence: "docker-event",
			}
		}
		resource := protocol.Event{
			OccurredAt: start.Add(apiMemWarn), Category: protocol.CategoryResource, Type: "resource.memory",
			Severity: protocol.SeverityWarning, Source: "agent", Target: "blog-api-1", ServiceKey: "blog/api", ContainerID: apiID,
			Summary: "메모리 사용량이 제한의 80%를 넘었습니다", Result: "주의", Evidence: "threshold",
		}
		candidates := []protocol.Event{
			resource,
			docker(apiUnhealthy, "container.health", protocol.SeverityWarning, "blog/api", "blog-api-1", apiID, "헬스체크 상태 unhealthy", "unhealthy"),
			docker(apiOOM, "container.oom", protocol.SeverityCritical, "blog/api", "blog-api-1", apiID, "메모리 부족(OOM) 기록", "OOM"),
			docker(apiOOM, "container.die", protocol.SeverityCritical, "blog/api", "blog-api-1", apiID, "컨테이너 종료, exit 137", "exit 137"),
			docker(apiRestart, "container.restart", protocol.SeverityWarning, "blog/api", "blog-api-1", apiID, "재시작 정책으로 다시 시작", "시작"),
			docker(apiHealthy, "container.health", protocol.SeverityInfo, "blog/api", "blog-api-1", apiID, "헬스체크 상태 healthy", "healthy"),
			docker(appRecreate, "container.stop", protocol.SeverityInfo, "shop/app", "shop-app-1", appOld, "컨테이너 중지", "중지"),
			docker(appRecreate, "container.destroy", protocol.SeverityInfo, "shop/app", "shop-app-1", appOld, "기존 컨테이너 삭제", "삭제"),
			docker(appRecreate, "container.create", protocol.SeverityInfo, "shop/app", "shop-app-1", appNew, "새 컨테이너 생성, 서비스 이력 유지", "생성"),
			docker(appRecreate, "container.start", protocol.SeverityInfo, "shop/app", "shop-app-1", appNew, "새 컨테이너 시작", "시작"),
		}
		for _, e := range candidates {
			if e.OccurredAt.After(from) && !e.OccurredAt.After(to) && !s.inGap(e.OccurredAt) {
				events = append(events, e)
			}
		}
	}
	return events
}

func (s *Scenario) inGap(t time.Time) bool {
	return !t.Before(s.gapFrom) && t.Before(s.gapTo)
}

// Batch는 (from, to] 구간의 데이터를 묶는다. 공백 구간에서는 에이전트가 끊긴 것처럼 아무것도 보내지 않는다.
func (s *Scenario) Batch(from, to time.Time) protocol.Batch {
	s.seq++
	batch := protocol.Batch{
		ProtocolVersion: protocol.Version, ServerID: ServerID, Mode: protocol.ModeDemo,
		Epoch: "demo", Seq: s.seq, SentAt: to,
	}
	if s.inGap(to) {
		return batch
	}
	if !s.gapSent && !to.Before(s.gapTo) {
		s.gapSent = true
		batch.Gaps = []protocol.Gap{{Source: "agent", From: s.gapFrom, To: s.gapTo, Reason: "에이전트 연결 끊김", Dropped: 12}}
	}

	batch.Heartbeat = &protocol.Heartbeat{
		AgentVersion: "demo", BootTime: s.origin.Add(-72 * time.Hour),
		UptimeSeconds: int64(to.Sub(s.origin.Add(-72*time.Hour)) / time.Second), DockerOK: true,
		Collectors: collectorStatus(to),
	}
	batch.Topology = s.topologyAt(to)
	batch.Events = s.eventsBetween(from, to)
	for t := nextGrid(from, sampleEvery); !t.After(to); t = t.Add(sampleEvery) {
		if !s.inGap(t) {
			batch.Samples = append(batch.Samples, s.samplesAt(t)...)
		}
	}
	for t := nextGrid(from, probeEvery); !t.After(to); t = t.Add(probeEvery) {
		if !s.inGap(t) {
			batch.Probes = append(batch.Probes, s.probesAt(t)...)
		}
	}
	return batch
}

func nextGrid(after time.Time, step time.Duration) time.Time {
	return after.Truncate(step).Add(step)
}

func collectorStatus(at time.Time) []protocol.CollectorStatus {
	return []protocol.CollectorStatus{
		{Name: "cpu", State: protocol.CollectorOK, LastSuccess: &at},
		{Name: "memory", State: protocol.CollectorOK, LastSuccess: &at},
		{Name: "disk", State: protocol.CollectorOK, LastSuccess: &at},
		{Name: "network", State: protocol.CollectorOK, LastSuccess: &at},
		{Name: "temperature", State: protocol.CollectorUnsupported, Error: "이 환경에서 온도 센서를 찾지 못했습니다"},
	}
}

// Seed는 과거 기록을 채운다. receivedAt에 데이터 시각을 넣어 지연 도착으로 보이지 않게 한다.
func (s *Scenario) Seed(ctx context.Context, now time.Time, ingest func(context.Context, protocol.Batch, time.Time) error) (time.Time, error) {
	cursor := s.origin
	for cursor.Before(now) {
		next := cursor.Add(sampleEvery)
		if next.After(now) {
			next = now
		}
		if err := ingest(ctx, s.Batch(cursor, next), next); err != nil {
			return cursor, err
		}
		cursor = next
	}
	return cursor, nil
}

// Run은 ctx가 끝날 때까지 5초마다 새 데이터를 만든다.
func (s *Scenario) Run(ctx context.Context, from time.Time, ingest func(context.Context, protocol.Batch) error) error {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	cursor := from
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-ticker.C:
			now = now.UTC()
			if err := ingest(ctx, s.Batch(cursor, now)); err != nil {
				return err
			}
			cursor = now
		}
	}
}
