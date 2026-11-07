// Package agent는 대상 서버에서 실행되어 상태를 수집하고 중앙 서버로 보낸다.
//
// 이 에이전트는 읽기만 한다. 컨테이너를 변경하는 코드가 없고, 받을 수 있는 작업은 로그 조회뿐이다.
package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ckdqja135/MSP/internal/agent/accesslog"
	"github.com/ckdqja135/MSP/internal/agent/dockerapi"
	"github.com/ckdqja135/MSP/internal/agent/hostports"
	"github.com/ckdqja135/MSP/internal/probe"
	"github.com/ckdqja135/MSP/internal/protocol"
)

const Version = "0.1.0"

// 수집 주기다.
const (
	heartbeatEvery   = 10 * time.Second
	hostEvery        = 10 * time.Second
	statsEvery       = 15 * time.Second
	diskEvery        = 60 * time.Second
	temperatureEvery = 30 * time.Second
	topologyEvery    = 60 * time.Second
	probeEvery       = 30 * time.Second
	accessLogEvery   = 2 * time.Second
	// 한 번 전송에 담는 요청의 상한이다. 요청이 몰려도 수집이 서버에 부담을 주지 않게 한다.
	maxRequestsPerFlush = 300
	// 스냅샷이 전송 대기 초과로 사라져도 복구되도록 구성이 같아도 주기적으로 다시 보낸다.
	topologyResendEvery = 5 * time.Minute

	statsConcurrency = 4
	maxBackoff       = 60 * time.Second
	sendTimeout      = 30 * time.Second
	eventFlushDelay  = time.Second
	// 이 시간 이상 Docker 이벤트 연결이 끊기면 일부 기록이 빠졌을 수 있다고 남긴다.
	eventGapAfter = 30 * time.Second
	// 이 시간 이상 동작하지 않았다면 공백 기록을 남긴다.
	downtimeGapAfter = 30 * time.Second
)

// Docker는 에이전트가 쓰는 Docker 읽기 기능이다. 변경 기능은 포함하지 않는다.
type Docker interface {
	Ping(ctx context.Context) error
	Topology(ctx context.Context) (*protocol.Topology, error)
	Stats(ctx context.Context, id string) (dockerapi.Stats, error)
	Events(ctx context.Context, since time.Time, handle func(dockerapi.Event)) error
	Logs(ctx context.Context, id string, options dockerapi.LogOptions, emit func(dockerapi.LogEntry) bool) error
}

// ProbeConfig는 내부 응답 확인 대상이다. 주소는 서비스의 현재 컨테이너 IP로만 정해진다.
type ProbeConfig struct {
	Service   string `json:"service"`
	Port      int    `json:"port"`
	Path      string `json:"path"`
	TimeoutMS int64  `json:"timeout_ms"`
}

// DeclaredHostService는 사용자가 설정 파일에 적은 호스트 서비스다. 이름을 확정하고 포트가 닫히면 장애로 본다.
type DeclaredHostService struct {
	Name  string `json:"name"`
	Ports []int  `json:"ports"`
}

// AccessLogConfig는 실제 요청을 읽을 프록시 접근 로그다. Service는 그 프록시의 이름이다.
type AccessLogConfig struct {
	Service string `json:"service"`
	Path    string `json:"path"`
}

type Config struct {
	CentralURL   string
	ServerID     string
	Token        string
	SpoolDir     string
	SpoolBytes   int64
	Probes       []ProbeConfig
	HostServices []DeclaredHostService
	AccessLogs   []AccessLogConfig
}

// ValidateCentralURL은 토큰이 평문으로 나가는 설정을 거부한다. 같은 장비 안의 시험만 예외로 둔다.
func ValidateCentralURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return fmt.Errorf("중앙 서버 주소가 잘못되었습니다: %q", raw)
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		host := parsed.Hostname()
		ip := net.ParseIP(host)
		if host == "localhost" || (ip != nil && ip.IsLoopback()) {
			return nil
		}
		return errors.New("토큰이 평문으로 전송되므로 http는 루프백 주소에서만 허용합니다")
	}
	return fmt.Errorf("지원하지 않는 주소 형식입니다: %s", parsed.Scheme)
}

type rateSample struct {
	at time.Time
	a  uint64
	b  uint64
}

type statsSample struct {
	at    time.Time
	stats dockerapi.Stats
}

type Agent struct {
	cfg    Config
	docker Docker
	host   HostReader
	spool  *Spool
	client *http.Client
	probes *http.Client
	now    func() time.Time

	epoch     string
	startedAt int64
	seq       uint64

	mu             sync.Mutex
	pending        protocol.Batch
	topology       *protocol.Topology
	services       map[string]string
	lastHash       string
	lastTopologyAt time.Time
	dockerOK       bool
	collectors     map[string]protocol.CollectorStatus
	limiter        *eventLimiter

	prevNet     *rateSample
	prevDiskIO  *rateSample
	prevStats   map[string]statsSample
	tempUnknown bool
	hostScanned bool

	topologyDirty chan struct{}
	flushSoon     chan struct{}
	accessTailers []accessTailer
	// 상한을 넘겨 버린 요청 수다. 다음 전송 때 공백으로 남긴다.
	requestsDropped int
	requestsDropAt  time.Time
}

func New(cfg Config, docker Docker, host HostReader) (*Agent, error) {
	if err := ValidateCentralURL(cfg.CentralURL); err != nil {
		return nil, err
	}
	if cfg.ServerID == "" || cfg.Token == "" {
		return nil, errors.New("서버 ID와 토큰이 필요합니다")
	}
	spool, err := OpenSpool(cfg.SpoolDir, cfg.SpoolBytes)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	now := func() time.Time { return time.Now().UTC() }
	return &Agent{
		cfg:           cfg,
		docker:        docker,
		host:          host,
		spool:         spool,
		client:        &http.Client{Timeout: sendTimeout},
		probes:        probe.NewClient(),
		now:           now,
		epoch:         hex.EncodeToString(raw),
		startedAt:     now().UnixNano(),
		collectors:    map[string]protocol.CollectorStatus{},
		services:      map[string]string{},
		limiter:       newEventLimiter(),
		prevStats:     map[string]statsSample{},
		tempUnknown:   true,
		topologyDirty: make(chan struct{}, 1),
		flushSoon:     make(chan struct{}, 1),
		accessTailers: newAccessTailers(cfg.AccessLogs),
	}, nil
}

func newAccessTailers(configs []AccessLogConfig) []accessTailer {
	tailers := make([]accessTailer, 0, len(configs))
	for _, config := range configs {
		tailers = append(tailers, accessTailer{service: config.Service, tailer: accesslog.NewTailer(config.Path)})
	}
	return tailers
}

type accessTailer struct {
	service string
	tailer  *accesslog.Tailer
}

// collectRequests는 접근 로그에 새로 적힌 요청을 읽는다. 상한을 넘으면 버리고 버린 수를 기록한다.
func (a *Agent) collectRequests(context.Context) {
	now := a.now()
	var requests []protocol.Request
	var failure error
	for _, source := range a.accessTailers {
		lines, err := source.tailer.Next()
		if err != nil {
			failure = err
			continue
		}
		for _, line := range lines {
			if request, ok := accesslog.Parse(line, source.service); ok {
				requests = append(requests, request)
			}
		}
	}

	a.mu.Lock()
	a.setCollector("access_log", now, failure)
	room := maxRequestsPerFlush - len(a.pending.Requests)
	if room < 0 {
		room = 0
	}
	if len(requests) > room {
		if a.requestsDropped == 0 {
			a.requestsDropAt = now
		}
		a.requestsDropped += len(requests) - room
		requests = requests[:room]
	}
	a.pending.Requests = append(a.pending.Requests, requests...)
	if a.requestsDropped > 0 && len(a.pending.Requests) < maxRequestsPerFlush {
		a.pending.Gaps = append(a.pending.Gaps, protocol.Gap{
			Source: "access_log", From: a.requestsDropAt, To: now,
			Reason: "요청이 많아 일부를 기록하지 않았습니다", Dropped: a.requestsDropped,
		})
		a.requestsDropped = 0
	}
	a.mu.Unlock()
	if len(requests) > 0 {
		a.requestFlush()
	}
}

// requestFlush는 요청이 들어왔을 때 전송을 앞당긴다. 화면에 요청이 늦게 나타나지 않게 하기 위해서다.
func (a *Agent) requestFlush() {
	select {
	case a.flushSoon <- struct{}{}:
	default:
	}
}

func (a *Agent) setCollector(name string, at time.Time, err error) {
	status := protocol.CollectorStatus{Name: name, State: protocol.CollectorOK, LastSuccess: &at}
	if err != nil {
		previous := a.collectors[name]
		status = protocol.CollectorStatus{Name: name, State: protocol.CollectorError, LastSuccess: previous.LastSuccess, Error: err.Error()}
	}
	a.collectors[name] = status
}

func (a *Agent) addSample(at time.Time, scope, name string, value float64) {
	a.pending.Samples = append(a.pending.Samples, protocol.Sample{At: at, Scope: scope, Name: name, Value: value})
}

// rate는 누적 값의 초당 변화량을 구한다. 첫 값이거나 값이 줄었으면 만들지 않는다.
func rate(prev, current uint64, seconds float64) (float64, bool) {
	if current < prev || seconds <= 0 {
		return 0, false
	}
	return float64(current-prev) / seconds, true
}

func (a *Agent) collectHost(ctx context.Context) {
	now := a.now()
	cpuPercent, cpuErr := a.host.CPUPercent(ctx)
	memPercent, memUsed, memErr := a.host.Memory(ctx)
	rx, tx, netErr := a.host.Network(ctx)
	read, write, ioErr := a.host.DiskIO(ctx)

	a.mu.Lock()
	defer a.mu.Unlock()
	a.setCollector("cpu", now, cpuErr)
	if cpuErr == nil {
		a.addSample(now, protocol.HostScope, "host.cpu.percent", cpuPercent)
	}
	a.setCollector("memory", now, memErr)
	if memErr == nil {
		a.addSample(now, protocol.HostScope, "host.mem.percent", memPercent)
		a.addSample(now, protocol.HostScope, "host.mem.used_bytes", float64(memUsed))
	}
	a.setCollector("network", now, netErr)
	if netErr == nil {
		a.addRates(now, &a.prevNet, rx, tx, "host.net.rx_bps", "host.net.tx_bps")
	}
	a.setCollector("disk_io", now, ioErr)
	if ioErr == nil {
		a.addRates(now, &a.prevDiskIO, read, write, "host.disk.read_bps", "host.disk.write_bps")
	}
}

func (a *Agent) addRates(now time.Time, prev **rateSample, first, second uint64, firstName, secondName string) {
	if *prev != nil {
		seconds := now.Sub((*prev).at).Seconds()
		if value, ok := rate((*prev).a, first, seconds); ok {
			a.addSample(now, protocol.HostScope, firstName, value)
		}
		if value, ok := rate((*prev).b, second, seconds); ok {
			a.addSample(now, protocol.HostScope, secondName, value)
		}
	}
	*prev = &rateSample{at: now, a: first, b: second}
}

func (a *Agent) collectDisk(ctx context.Context) {
	now := a.now()
	percent, inodes, err := a.host.DiskUsage(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.setCollector("disk", now, err)
	if err == nil {
		a.addSample(now, protocol.HostScope, "host.disk.percent", percent)
		a.addSample(now, protocol.HostScope, "host.disk.inode_percent", inodes)
	}
}

func (a *Agent) collectTemperature(ctx context.Context) {
	now := a.now()
	celsius, supported, err := a.host.Temperature(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	if !supported {
		a.collectors["temperature"] = protocol.CollectorStatus{
			Name: "temperature", State: protocol.CollectorUnsupported, Error: "이 환경에서 온도 센서를 찾지 못했습니다",
		}
		return
	}
	a.setCollector("temperature", now, err)
	if err == nil {
		a.addSample(now, protocol.HostScope, "host.temperature", celsius)
	}
}

// collectTopology는 구성을 읽어 바뀌었거나 재전송 시점이면 다음 배치에 넣는다.
func (a *Agent) collectTopology(ctx context.Context) {
	now := a.now()
	topology, err := a.docker.Topology(ctx)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.setCollector("docker", now, err)
	a.dockerOK = err == nil
	if err != nil {
		return
	}
	topology.HostServices = a.readHostServices(ctx, topology, now)
	topology.Normalize()
	services := make(map[string]string, len(topology.Containers))
	for _, c := range topology.Containers {
		services[c.ID] = c.ServiceKey()
	}
	a.recordHostChanges(topology.HostServices, now)
	a.topology = topology
	a.services = services
	if topology.Hash != a.lastHash || now.Sub(a.lastTopologyAt) >= topologyResendEvery {
		a.pending.Topology = topology
		a.lastHash = topology.Hash
		a.lastTopologyAt = now
	}
}

// readHostServices는 호출자가 잠금을 잡은 상태에서 실행된다. /proc 읽기는 짧아 수집을 막지 않는다.
func (a *Agent) readHostServices(ctx context.Context, topology *protocol.Topology, now time.Time) []protocol.HostService {
	listeners, running, err := a.host.Listeners(ctx)
	if errors.Is(err, hostports.ErrUnsupported) {
		a.collectors["host_services"] = protocol.CollectorStatus{
			Name: "host_services", State: protocol.CollectorUnsupported, Error: err.Error(),
		}
		return nil
	}
	a.setCollector("host_services", now, err)
	if err != nil {
		// 읽지 못한 것을 포트가 닫힌 것으로 보지 않는다. 직전 값을 유지한다.
		if a.topology != nil {
			return a.topology.HostServices
		}
		return nil
	}
	return deriveHostServices(listeners, running, topology.Containers, a.cfg.HostServices)
}

func hostServiceLabel(service protocol.HostService) string {
	if service.Name == "" {
		return "프로세스 미확인"
	}
	return service.Name
}

func portSet(service protocol.HostService) map[int]bool {
	ports := make(map[int]bool, len(service.Ports))
	for _, port := range service.Ports {
		ports[port.Port] = true
	}
	return ports
}

// recordHostChanges는 호스트 포트가 열리거나 닫힌 순간을 흐름표에 남긴다. 첫 수집은 변화가 아니다.
func (a *Agent) recordHostChanges(current []protocol.HostService, now time.Time) {
	opened, closed := map[int]string{}, map[int]string{}
	before := map[int]string{}
	if a.topology != nil {
		for _, service := range a.topology.HostServices {
			if service.Listening {
				for port := range portSet(service) {
					before[port] = hostServiceLabel(service)
				}
			}
		}
	}
	after := map[int]string{}
	for _, service := range current {
		if service.Listening {
			for port := range portSet(service) {
				after[port] = hostServiceLabel(service)
			}
		}
	}
	if !a.hostScanned {
		a.hostScanned = current != nil
		return
	}
	for port, name := range after {
		if _, ok := before[port]; !ok {
			opened[port] = name
		}
	}
	for port, name := range before {
		if _, ok := after[port]; !ok {
			closed[port] = name
		}
	}
	add := func(ports map[int]string, kind, verb string, severity protocol.Severity) {
		for port, name := range ports {
			a.pending.Events = append(a.pending.Events, protocol.Event{
				OccurredAt: now, Category: protocol.CategoryOps, Type: kind, Severity: severity,
				Source: "agent", Target: name, ServiceKey: protocol.ServiceKey(protocol.HostProject, name),
				Summary: fmt.Sprintf("호스트 포트 %d %s", port, verb), Result: verb, Evidence: "proc-net",
			})
		}
	}
	add(opened, "host.port_open", "열림", protocol.SeverityInfo)
	add(closed, "host.port_closed", "닫힘", protocol.SeverityWarning)
}

func (a *Agent) collectStats(ctx context.Context) {
	a.mu.Lock()
	topology := a.topology
	a.mu.Unlock()
	if topology == nil {
		return
	}

	type result struct {
		container protocol.Container
		stats     dockerapi.Stats
		at        time.Time
	}
	var (
		wg      sync.WaitGroup
		resultM sync.Mutex
		results []result
		lastErr error
	)
	limit := make(chan struct{}, statsConcurrency)
	for _, c := range topology.Containers {
		if c.State != "running" {
			continue
		}
		wg.Add(1)
		limit <- struct{}{}
		go func(c protocol.Container) {
			defer wg.Done()
			defer func() { <-limit }()
			stats, err := a.docker.Stats(ctx, c.ID)
			resultM.Lock()
			defer resultM.Unlock()
			if err != nil {
				lastErr = err
				return
			}
			results = append(results, result{c, stats, a.now()})
		}(c)
	}
	wg.Wait()

	a.mu.Lock()
	defer a.mu.Unlock()
	a.setCollector("container_stats", a.now(), lastErr)
	seen := make(map[string]bool, len(results))
	for _, r := range results {
		seen[r.container.ID] = true
		scope := protocol.ContainerScope(r.container.ServiceKey(), r.container.Replica)
		usage := r.stats.MemUsage
		if r.stats.MemInactive < usage {
			usage -= r.stats.MemInactive
		}
		a.addSample(r.at, scope, "container.mem.bytes", float64(usage))
		if r.stats.MemLimit > 0 {
			a.addSample(r.at, scope, "container.mem.limit_bytes", float64(r.stats.MemLimit))
		}
		if previous, ok := a.prevStats[r.container.ID]; ok {
			a.addContainerRates(r.at, scope, previous, r.stats)
		}
		a.prevStats[r.container.ID] = statsSample{at: r.at, stats: r.stats}
	}
	for id := range a.prevStats {
		if !seen[id] {
			delete(a.prevStats, id)
		}
	}
}

func (a *Agent) addContainerRates(at time.Time, scope string, previous statsSample, current dockerapi.Stats) {
	if current.SystemCPU > previous.stats.SystemCPU && current.CPUTotal >= previous.stats.CPUTotal {
		delta := float64(current.CPUTotal - previous.stats.CPUTotal)
		system := float64(current.SystemCPU - previous.stats.SystemCPU)
		cpus := float64(current.OnlineCPUs)
		if cpus == 0 {
			cpus = 1
		}
		a.addSample(at, scope, "container.cpu.percent", delta/system*cpus*100)
	}
	seconds := at.Sub(previous.at).Seconds()
	if value, ok := rate(previous.stats.NetRx, current.NetRx, seconds); ok {
		a.addSample(at, scope, "container.net.rx_bps", value)
	}
	if value, ok := rate(previous.stats.NetTx, current.NetTx, seconds); ok {
		a.addSample(at, scope, "container.net.tx_bps", value)
	}
}

func (a *Agent) runProbes(ctx context.Context) {
	if len(a.cfg.Probes) == 0 {
		return
	}
	a.mu.Lock()
	topology := a.topology
	a.mu.Unlock()
	if topology == nil {
		return
	}
	var wg sync.WaitGroup
	results := make([]protocol.Probe, len(a.cfg.Probes))
	for i, config := range a.cfg.Probes {
		wg.Add(1)
		go func(i int, config ProbeConfig) {
			defer wg.Done()
			results[i] = a.probe(ctx, config, topology)
		}(i, config)
	}
	wg.Wait()
	a.mu.Lock()
	a.pending.Probes = append(a.pending.Probes, results...)
	a.mu.Unlock()
}

// probe는 서비스의 현재 컨테이너 IP에만 요청한다. 임의 주소로 요청을 보내는 경로로 쓰이지 않게 하기 위해서다.
func (a *Agent) probe(ctx context.Context, config ProbeConfig, topology *protocol.Topology) protocol.Probe {
	timeout := time.Duration(config.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	path := config.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	result := protocol.Probe{
		At:         a.now(),
		ServiceKey: config.Service,
		Vantage:    protocol.VantageInternal,
		Target:     "GET " + path,
		TimeoutMS:  timeout.Milliseconds(),
	}

	address := ""
	for _, c := range topology.Containers {
		if c.ServiceKey() != config.Service || c.State != "running" {
			continue
		}
		for _, attachment := range c.Networks {
			if attachment.IP != "" {
				address = attachment.IP
				break
			}
		}
		if address != "" {
			break
		}
	}
	if address == "" {
		result.Failure = "no_container"
		return result
	}

	target := "http://" + net.JoinHostPort(address, strconv.Itoa(config.Port)) + path
	checked := probe.Run(ctx, a.probes, target, timeout)
	result.OK, result.Failure, result.LatencyMS = checked.OK, checked.Failure, checked.LatencyMS
	return result
}

// handleDockerEvent는 흐름표 행을 만들고 구성 재조회를 요청한다.
func (a *Agent) handleDockerEvent(e dockerapi.Event) {
	a.mu.Lock()
	row, keep := convertEvent(e, a.services)
	if keep {
		if a.limiter.admit(row, a.now()) {
			a.pending.Events = append(a.pending.Events, row)
		}
	}
	a.mu.Unlock()
	if !keep {
		return
	}
	select {
	case a.topologyDirty <- struct{}{}:
	default:
	}
	select {
	case a.flushSoon <- struct{}{}:
	default:
	}
}

// watchEvents는 Docker 이벤트 연결을 유지한다. 끊긴 동안의 기록은 일부 빠질 수 있어 공백으로 남긴다.
func (a *Agent) watchEvents(ctx context.Context) {
	backoff := time.Second
	since := a.now()
	for ctx.Err() == nil {
		last := since
		err := a.docker.Events(ctx, since, func(e dockerapi.Event) {
			// since는 초 단위라 재연결하면 이미 받은 이벤트가 다시 온다.
			if !e.At.After(last) {
				return
			}
			last = e.At
			backoff = time.Second
			a.handleDockerEvent(e)
		})
		if ctx.Err() != nil {
			return
		}
		lostAt := a.now()
		if err != nil {
			log.Printf("Docker 이벤트 연결 끊김: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
		if a.now().Sub(lostAt) >= eventGapAfter {
			a.mu.Lock()
			a.pending.Gaps = append(a.pending.Gaps, protocol.Gap{
				Source: "docker-events", From: lostAt, To: a.now(),
				Reason: "Docker 이벤트 연결이 끊겨 일부 기록이 빠졌을 수 있습니다",
			})
			a.mu.Unlock()
		}
		// 재연결하면 끊긴 동안의 기록을 다시 받되, 구성은 재대조로 맞춘다.
		since = last
		select {
		case a.topologyDirty <- struct{}{}:
		default:
		}
	}
}

func (a *Agent) heartbeat(ctx context.Context) *protocol.Heartbeat {
	boot, uptime, bootErr := a.host.Boot(ctx)
	pingErr := a.docker.Ping(ctx)
	now := a.now()
	a.mu.Lock()
	defer a.mu.Unlock()
	a.setCollector("boot", now, bootErr)
	a.dockerOK = pingErr == nil
	a.setCollector("docker", now, pingErr)
	collectors := make([]protocol.CollectorStatus, 0, len(a.collectors))
	for _, status := range a.collectors {
		collectors = append(collectors, status)
	}
	sort.Slice(collectors, func(i, j int) bool { return collectors[i].Name < collectors[j].Name })
	return &protocol.Heartbeat{
		AgentVersion: Version, BootTime: boot, UptimeSeconds: uptime, DockerOK: pingErr == nil, Collectors: collectors,
	}
}

// flush는 모아 둔 데이터를 전송 대기열에 넣는다.
func (a *Agent) flush(ctx context.Context, withHeartbeat bool) error {
	var heartbeat *protocol.Heartbeat
	if withHeartbeat {
		heartbeat = a.heartbeat(ctx)
	}
	now := a.now()

	a.mu.Lock()
	batch := a.pending
	a.pending = protocol.Batch{}
	batch.Events = append(batch.Events, a.limiter.drain(now)...)
	a.seq++
	batch.ProtocolVersion = protocol.Version
	batch.ServerID = a.cfg.ServerID
	batch.Mode = protocol.ModeLive
	batch.Epoch = a.epoch
	batch.Seq = a.seq
	batch.SentAt = now
	batch.Heartbeat = heartbeat
	a.mu.Unlock()

	gaps, err := a.spool.Put(a.startedAt, batch)
	if err != nil {
		// 디스크가 가득 찬 경우 등이다. 이 배치는 사라지므로 다음 배치에 공백으로 남긴다.
		gaps = append(gaps, protocol.Gap{
			Source: "spool", From: batch.SentAt, To: now,
			Reason:  "전송 대기열에 저장하지 못해 데이터를 버렸습니다",
			Dropped: len(batch.Events) + len(batch.Samples) + len(batch.Requests),
		})
	}
	if len(gaps) > 0 {
		a.mu.Lock()
		a.pending.Gaps = append(a.pending.Gaps, gaps...)
		a.mu.Unlock()
	}
	return err
}

// recordDowntime은 지난 실행의 마지막 시각을 읽어 동작하지 않은 구간을 공백으로 남긴다.
func (a *Agent) recordDowntime() {
	path := filepath.Join(a.cfg.SpoolDir, "last_alive")
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	last, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(body)))
	if err != nil {
		return
	}
	now := a.now()
	if now.Sub(last) >= downtimeGapAfter {
		a.mu.Lock()
		a.pending.Gaps = append(a.pending.Gaps, protocol.Gap{
			Source: "agent", From: last, To: now, Reason: "에이전트가 동작하지 않은 구간입니다. 원인은 알 수 없습니다",
		})
		a.mu.Unlock()
	}
}

func (a *Agent) markAlive() {
	path := filepath.Join(a.cfg.SpoolDir, "last_alive")
	if err := os.WriteFile(path, []byte(a.now().Format(time.RFC3339Nano)), 0o600); err != nil {
		log.Printf("마지막 동작 시각을 기록하지 못했습니다: %v", err)
	}
}

// sendOnce는 가장 오래된 배치 하나를 보낸다. 보낼 것이 없으면 false를 돌려준다.
func (a *Agent) sendOnce(ctx context.Context) (bool, error) {
	name, batch, ok := a.spool.Oldest()
	if !ok {
		return false, nil
	}
	// 전송 시각은 실제로 보내는 시각이다. 중앙 서버가 시계 차이를 계산하는 데 쓴다.
	batch.SentAt = a.now()
	if err := a.post(ctx, batch); err != nil {
		return true, err
	}
	return true, a.spool.Remove(name)
}

// sendLoop는 대기열을 순서대로 보낸다. 응답을 받기 전에는 지우지 않아 응답이 유실되면 다시 보낸다.
// 중앙 서버는 순번으로 중복을 제거한다.
func (a *Agent) sendLoop(ctx context.Context) {
	backoff := time.Second
	for ctx.Err() == nil {
		sent, err := a.sendOnce(ctx)
		wait := time.Duration(0)
		switch {
		case err != nil:
			log.Printf("전송 실패, 대기 %d건: %v", a.spool.Count(), err)
			jitter, _ := rand.Int(rand.Reader, big.NewInt(int64(backoff/2)+1))
			wait = backoff + time.Duration(jitter.Int64())
			backoff = min(backoff*2, maxBackoff)
		case !sent:
			wait = time.Second
		default:
			backoff = time.Second
		}
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
	}
}

// Run은 ctx가 끝날 때까지 수집과 전송을 계속한다.
func (a *Agent) Run(ctx context.Context) error {
	a.recordDowntime()
	a.collectTopology(ctx)
	a.collectTemperature(ctx)
	a.collectDisk(ctx)
	a.collectHost(ctx)
	if err := a.flush(ctx, true); err != nil {
		log.Printf("전송 대기열 저장 실패: %v", err)
	}

	go a.sendLoop(ctx)
	go a.watchEvents(ctx)
	go a.pollTasks(ctx)
	every := func(period time.Duration, task func(context.Context)) {
		go func() {
			ticker := time.NewTicker(period)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					task(ctx)
				}
			}
		}()
	}
	every(hostEvery, a.collectHost)
	every(statsEvery, a.collectStats)
	every(diskEvery, a.collectDisk)
	every(temperatureEvery, a.collectTemperature)
	every(probeEvery, a.runProbes)
	if len(a.accessTailers) > 0 {
		a.collectRequests(ctx)
		every(accessLogEvery, a.collectRequests)
	}

	heartbeat := time.NewTicker(heartbeatEvery)
	defer heartbeat.Stop()
	topology := time.NewTicker(topologyEvery)
	defer topology.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-heartbeat.C:
			a.markAlive()
			if err := a.flush(ctx, true); err != nil {
				log.Printf("전송 대기열 저장 실패: %v", err)
			}
		case <-topology.C:
			a.collectTopology(ctx)
		case <-a.topologyDirty:
			// 이벤트가 몰릴 때 조회가 반복되지 않도록 잠시 모아서 한 번만 읽는다.
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(eventFlushDelay):
			}
			a.collectTopology(ctx)
		case <-a.flushSoon:
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(eventFlushDelay):
			}
			if err := a.flush(ctx, false); err != nil {
				log.Printf("전송 대기열 저장 실패: %v", err)
			}
		}
	}
}
