// Package protocol은 에이전트와 중앙 서버 사이의 전송 계약을 정의한다.
package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"
	"time"
)

const Version = 1

type Mode string

const (
	ModeLive Mode = "live"
	ModeDemo Mode = "demo"
)

// Batch는 에이전트가 보내는 전송 단위다. (Epoch, Seq) 쌍으로 중복 수신을 제거한다.
type Batch struct {
	ProtocolVersion int        `json:"protocol_version"`
	ServerID        string     `json:"server_id"`
	Mode            Mode       `json:"mode"`
	Epoch           string     `json:"epoch"`
	Seq             uint64     `json:"seq"`
	SentAt          time.Time  `json:"sent_at"`
	Heartbeat       *Heartbeat `json:"heartbeat,omitempty"`
	Topology        *Topology  `json:"topology,omitempty"`
	Events          []Event    `json:"events,omitempty"`
	Samples         []Sample   `json:"samples,omitempty"`
	Gaps            []Gap      `json:"gaps,omitempty"`
	Probes          []Probe    `json:"probes,omitempty"`
	Requests        []Request  `json:"requests,omitempty"`
}

type Ack struct {
	Seq       uint64 `json:"seq"`
	Duplicate bool   `json:"duplicate,omitempty"`
}

type CollectorState string

const (
	CollectorOK          CollectorState = "ok"
	CollectorUnsupported CollectorState = "unsupported"
	CollectorError       CollectorState = "error"
)

// CollectorStatus는 값이 없는 이유를 구분하기 위한 수집기 상태다.
type CollectorStatus struct {
	Name        string         `json:"name"`
	State       CollectorState `json:"state"`
	LastSuccess *time.Time     `json:"last_success,omitempty"`
	Error       string         `json:"error,omitempty"`
}

type Heartbeat struct {
	AgentVersion  string            `json:"agent_version"`
	BootTime      time.Time         `json:"boot_time"`
	UptimeSeconds int64             `json:"uptime_seconds"`
	DockerOK      bool              `json:"docker_ok"`
	Collectors    []CollectorStatus `json:"collectors,omitempty"`
}

// Topology는 한 시점의 Docker 구성 스냅샷이다. 구성이 바뀔 때만 전송한다.
type Topology struct {
	Hash       string      `json:"hash"`
	ObservedAt time.Time   `json:"observed_at"`
	Containers []Container `json:"containers"`
	Networks   []Network   `json:"networks"`
	// HostServices는 Docker 밖에서 포트를 연 호스트 프로세스다.
	HostServices []HostService `json:"host_services,omitempty"`
}

// HostService의 근거다. 소켓 주인을 읽지 못하면 이름은 추정이거나 비어 있다.
const (
	HostEvidenceOwner   = "socket-owner"
	HostEvidenceConfig  = "config"
	HostEvidenceName    = "name-match"
	HostEvidenceUnknown = "unknown"
)

type HostPort struct {
	IP   string `json:"ip"`
	Port int    `json:"port"`
}

type HostService struct {
	Name      string     `json:"name"`
	Ports     []HostPort `json:"ports"`
	Evidence  string     `json:"evidence"`
	Listening bool       `json:"listening"`
}

// Container에는 환경 변수와 실행 명령 필드를 두지 않는다. 비밀값이 섞일 수 있기 때문이다.
type Container struct {
	ID                string              `json:"id"`
	Name              string              `json:"name"`
	Project           string              `json:"project"`
	Service           string              `json:"service"`
	Replica           int                 `json:"replica"`
	OneOff            bool                `json:"one_off,omitempty"`
	Image             string              `json:"image"`
	ImageID           string              `json:"image_id"`
	State             string              `json:"state"`
	ExitCode          int                 `json:"exit_code"`
	OOMKilled         bool                `json:"oom_killed,omitempty"`
	Health            string              `json:"health"`
	RestartCount      int                 `json:"restart_count"`
	RestartPolicy     string              `json:"restart_policy,omitempty"`
	CreatedAt         time.Time           `json:"created_at"`
	StartedAt         time.Time           `json:"started_at"`
	Networks          []NetworkAttachment `json:"networks,omitempty"`
	Ports             []PortBinding       `json:"ports,omitempty"`
	DeclaredDependsOn []string            `json:"declared_depends_on,omitempty"`
}

type NetworkAttachment struct {
	Network string `json:"network"`
	IP      string `json:"ip,omitempty"`
}

// PortBinding의 HostPort가 0이면 호스트에 공개되지 않은 포트다.
type PortBinding struct {
	HostIP        string `json:"host_ip,omitempty"`
	HostPort      int    `json:"host_port,omitempty"`
	ContainerPort int    `json:"container_port"`
	Proto         string `json:"proto"`
}

type Network struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Driver   string `json:"driver"`
	Subnet   string `json:"subnet,omitempty"`
	Internal bool   `json:"internal,omitempty"`
}

type EventCategory string

const (
	CategoryOps      EventCategory = "ops"
	CategoryProbe    EventCategory = "probe"
	CategoryResource EventCategory = "resource"
	CategoryAlert    EventCategory = "alert"
	CategoryGap      EventCategory = "gap"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// Event는 흐름표의 한 행이다. 측정하지 않은 소요 시간과 크기는 nil로 둔다.
type Event struct {
	OccurredAt  time.Time     `json:"occurred_at"`
	Category    EventCategory `json:"category"`
	Type        string        `json:"type"`
	Severity    Severity      `json:"severity"`
	Source      string        `json:"source,omitempty"`
	Target      string        `json:"target,omitempty"`
	ServiceKey  string        `json:"service_key,omitempty"`
	ContainerID string        `json:"container_id,omitempty"`
	Summary     string        `json:"summary"`
	DurationMS  *int64        `json:"duration_ms,omitempty"`
	// DurationLimitMS는 제한 시간까지 응답이 없었음을 뜻한다. 실제 소요 시간이 아니다.
	DurationLimitMS *int64 `json:"duration_limit_ms,omitempty"`
	Bytes           *int64 `json:"bytes,omitempty"`
	Result          string `json:"result,omitempty"`
	Evidence        string `json:"evidence"`
	Aggregated      int    `json:"aggregated,omitempty"`
}

// Sample의 Scope는 "host" 또는 서비스 키와 레플리카를 조합한 값이다.
type Sample struct {
	At    time.Time `json:"at"`
	Scope string    `json:"scope"`
	Name  string    `json:"name"`
	Value float64   `json:"value"`
}

// Gap은 수집하지 못했거나 버린 구간을 기록한다.
type Gap struct {
	Source  string    `json:"source"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Reason  string    `json:"reason"`
	Dropped int       `json:"dropped,omitempty"`
}

// TaskKind는 중앙 서버가 에이전트에 요청할 수 있는 작업이다. 조회만 존재하고 변경 작업은 없다.
type TaskKind string

const TaskLogs TaskKind = "logs"

type Task struct {
	ID   string      `json:"id"`
	Kind TaskKind    `json:"kind"`
	Logs *LogRequest `json:"logs,omitempty"`
}

// LogRequest는 서비스 안의 컨테이너 로그를 요청한다. 에이전트는 자신의 구성에 있는 컨테이너만 읽는다.
type LogRequest struct {
	ServiceKey  string     `json:"service_key"`
	ContainerID string     `json:"container_id,omitempty"`
	Since       *time.Time `json:"since,omitempty"`
	Until       *time.Time `json:"until,omitempty"`
	Tail        int        `json:"tail,omitempty"`
	Follow      bool       `json:"follow,omitempty"`
	Query       string     `json:"query,omitempty"`
}

type LogLine struct {
	At     time.Time `json:"at"`
	Stream string    `json:"stream"`
	Text   string    `json:"text"`
}

// LogChunk의 Dropped는 전송 한도를 넘어 버린 줄 수다. 누락을 화면에 드러내기 위한 값이다.
type LogChunk struct {
	Lines   []LogLine `json:"lines,omitempty"`
	Dropped int       `json:"dropped,omitempty"`
	Done    bool      `json:"done,omitempty"`
	Error   string    `json:"error,omitempty"`
}

type ChunkAck struct {
	Continue bool `json:"continue"`
}

type Vantage string

const (
	VantageInternal Vantage = "internal"
	VantageExternal Vantage = "external"
)

// Probe는 서비스 응답 확인 한 번의 결과다. 응답을 받지 못했으면 LatencyMS는 nil이다.
type Probe struct {
	At         time.Time `json:"at"`
	ServiceKey string    `json:"service_key"`
	Vantage    Vantage   `json:"vantage"`
	Target     string    `json:"target"`
	OK         bool      `json:"ok"`
	Failure    string    `json:"failure,omitempty"`
	LatencyMS  *int64    `json:"latency_ms,omitempty"`
	TimeoutMS  int64     `json:"timeout_ms,omitempty"`
}

// Request는 프록시 접근 로그에서 읽은 실제 API 요청이다. 클라이언트 주소와 쿼리 문자열은 담지 않는다.
// 로그에 없는 값은 비워 둔다. 소요 시간과 전달 대상은 로그 형식에 따라 없을 수 있다.
type Request struct {
	At         time.Time `json:"at"`
	Service    string    `json:"service"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Proto      string    `json:"proto,omitempty"`
	Status     int       `json:"status"`
	Bytes      *int64    `json:"bytes,omitempty"`
	DurationMS *int64    `json:"duration_ms,omitempty"`
	Upstream   string    `json:"upstream,omitempty"`
	UpstreamMS *int64    `json:"upstream_ms,omitempty"`
}

const HostScope = "host"

// HostProject는 Docker 밖의 호스트 서비스를 묶는 이름이다.
const HostProject = "_host"

// StandaloneProject는 Compose 라벨이 없는 컨테이너를 묶는 이름이다.
const StandaloneProject = "_standalone"

// ServiceKey는 컨테이너가 재생성되어도 유지되는 서비스 식별자를 만든다.
func ServiceKey(project, service string) string {
	if project == "" {
		project = StandaloneProject
	}
	return project + "/" + service
}

func (c Container) ServiceKey() string {
	return ServiceKey(c.Project, c.Service)
}

// ContainerScope는 재생성되어도 지표 계열이 이어지도록 컨테이너 ID 대신 레플리카를 쓴다.
func ContainerScope(serviceKey string, replica int) string {
	return serviceKey + "#" + strconv.Itoa(replica)
}

// Normalize는 순서를 고정하고 해시를 계산한다. 해시가 같으면 구성이 같다.
func (t *Topology) Normalize() {
	sort.Slice(t.Containers, func(i, j int) bool { return t.Containers[i].ID < t.Containers[j].ID })
	sort.Slice(t.Networks, func(i, j int) bool { return t.Networks[i].ID < t.Networks[j].ID })
	sort.Slice(t.HostServices, func(i, j int) bool { return t.HostServices[i].Name < t.HostServices[j].Name })
	body, _ := json.Marshal(struct {
		Containers   []Container
		Networks     []Network
		HostServices []HostService
	}{t.Containers, t.Networks, t.HostServices})
	sum := sha256.Sum256(body)
	t.Hash = hex.EncodeToString(sum[:])
}
