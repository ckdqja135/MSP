package central

import (
	"sort"
	"strings"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

// 하트비트 끊김 판정 기준이다. 짧은 끊김은 장애가 아니라 지연으로 표시한다.
const (
	heartbeatDelayedAfter = 30 * time.Second
	heartbeatLostAfter    = 90 * time.Second
)

const (
	StatusOK           = "ok"
	StatusWarning      = "warning"
	StatusCritical     = "critical"
	StatusStopped      = "stopped"
	StatusDisconnected = "disconnected"
)

const (
	ResponseOK          = "ok"
	ResponseFailing     = "failing"
	ResponseDown        = "down"
	ResponseUnconfirmed = "unconfirmed"
	ResponseHistorical  = "historical"
)

// 관계 유형이다. 같은 네트워크 소속으로 서비스 간 관계를 만드는 유형은 두지 않는다.
const (
	RelationMemberOf = "member_of"
	RelationPublish  = "publishes"
	RelationDeclared = "declared_depends_on"
)

type Relation struct {
	Type     string `json:"type"`
	From     string `json:"from"`
	To       string `json:"to"`
	Evidence string `json:"evidence"`
	Detail   string `json:"detail,omitempty"`
}

type LatestValue struct {
	Value float64   `json:"value"`
	At    time.Time `json:"at"`
}

type ContainerView struct {
	protocol.Container
	Metrics map[string]LatestValue `json:"metrics"`
}

type ProbeView struct {
	Vantage     protocol.Vantage `json:"vantage"`
	Target      string           `json:"target"`
	State       string           `json:"state"`
	Failures    int              `json:"failures"`
	LastChecked time.Time        `json:"last_checked"`
	LatencyMS   *int64           `json:"latency_ms"`
	LastFailure string           `json:"last_failure,omitempty"`
}

type ServiceView struct {
	Key        string          `json:"key"`
	Name       string          `json:"name"`
	Project    string          `json:"project"`
	Status     string          `json:"status"`
	Runtime    string          `json:"runtime"`
	Response   string          `json:"response"`
	Reasons    []string        `json:"reasons"`
	Containers []ContainerView `json:"containers"`
	Probes     []ProbeView     `json:"probes"`
}

type ProjectView struct {
	Key      string        `json:"key"`
	Status   string        `json:"status"`
	Services []ServiceView `json:"services"`
}

type NetworkView struct {
	protocol.Network
	Members []string `json:"members"`
	Shared  bool     `json:"shared"`
}

type TopologyView struct {
	ServerID   string        `json:"server_id"`
	VersionID  int64         `json:"version_id"`
	ValidFrom  time.Time     `json:"valid_from"`
	ValidTo    *time.Time    `json:"valid_to"`
	ObservedAt time.Time     `json:"observed_at"`
	Historical bool          `json:"historical"`
	Projects   []ProjectView `json:"projects"`
	Networks   []NetworkView `json:"networks"`
	Relations  []Relation    `json:"relations"`
}

// Reachability는 서버 도달 상태다. 응답 중단만으로 정전이나 OS 장애를 단정하지 않는다.
type Reachability struct {
	State  string `json:"state"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

func reachabilityOf(lastSeen, now time.Time, dockerOK bool) Reachability {
	if lastSeen.IsZero() {
		return Reachability{State: "unknown", Label: "수신 기록 없음", Detail: "에이전트 데이터를 받은 적이 없습니다."}
	}
	age := now.Sub(lastSeen)
	switch {
	case age >= heartbeatLostAfter:
		return Reachability{State: "lost", Label: "서버 응답 없음, 원인 미상",
			Detail: "에이전트 수신이 끊겼습니다. 전원, OS, 네트워크, 에이전트 중 무엇이 원인인지는 이 정보만으로 알 수 없습니다."}
	case age >= heartbeatDelayedAfter:
		return Reachability{State: "delayed", Label: "데이터 지연", Detail: "수신이 늦어지고 있습니다. 아직 장애로 판단하지 않습니다."}
	case !dockerOK:
		return Reachability{State: "docker_down", Label: "Docker 응답 없음", Detail: "에이전트는 동작하지만 Docker 데몬이 응답하지 않습니다."}
	}
	return Reachability{State: "ok", Label: "정상", Detail: ""}
}

// rollupService는 실행 상태와 응답 상태를 구분해 서비스 상태를 파생한다.
func rollupService(containers []protocol.Container, probes []ProbeView, fresh, historical bool) (status, runtime, response string, reasons []string) {
	reasons = []string{}
	running, restarting, failedExit, unhealthy, starting, oneOffOnly := 0, 0, 0, 0, 0, true
	for _, c := range containers {
		if !c.OneOff {
			oneOffOnly = false
		}
		switch c.State {
		case "running":
			running++
			if c.Health == "unhealthy" {
				unhealthy++
			}
			if c.Health == "starting" {
				starting++
			}
		case "restarting":
			restarting++
		case "exited", "dead":
			if c.ExitCode != 0 {
				failedExit++
			}
		}
	}

	switch {
	case running == len(containers) && running > 0:
		runtime = "running"
	case running > 0:
		runtime = "partial"
	case restarting > 0:
		runtime = "restarting"
	default:
		runtime = "stopped"
	}

	response = ResponseUnconfirmed
	if historical {
		response = ResponseHistorical
	}
	for _, p := range probes {
		switch p.State {
		case probeDown:
			response = ResponseDown
		case probeFailing:
			if response != ResponseDown {
				response = ResponseFailing
			}
		case probeOK:
			if response == ResponseUnconfirmed {
				response = ResponseOK
			}
		}
	}

	status = StatusOK
	if !fresh {
		return StatusDisconnected, runtime, response, append(reasons, "데이터 수신이 끊겨 마지막으로 받은 상태입니다.")
	}
	raise := func(level string, reason string) {
		if level == StatusCritical || status == StatusOK {
			status = level
		}
		reasons = append(reasons, reason)
	}
	if runtime == "stopped" && restarting == 0 {
		if failedExit > 0 && !oneOffOnly {
			raise(StatusCritical, "컨테이너가 비정상 종료했습니다.")
		} else {
			status = StatusStopped
			reasons = append(reasons, "실행 중인 컨테이너가 없습니다.")
		}
	}
	if restarting > 0 {
		raise(StatusCritical, "컨테이너가 재시작 중입니다.")
	}
	if runtime == "partial" {
		raise(StatusWarning, "일부 컨테이너만 실행 중입니다.")
	}
	if unhealthy > 0 {
		raise(StatusCritical, "Docker 헬스체크가 unhealthy입니다.")
	}
	if starting > 0 {
		raise(StatusWarning, "Docker 헬스체크가 시작 중입니다.")
	}
	if response == ResponseDown {
		if running > 0 {
			raise(StatusCritical, "실행 중 / 무응답: 컨테이너는 실행 중이지만 응답하지 않습니다.")
		} else {
			raise(StatusCritical, "응답하지 않습니다.")
		}
	}
	if response == ResponseFailing {
		raise(StatusWarning, "응답 확인이 일시적으로 실패했습니다. 아직 장애로 판단하지 않습니다.")
	}
	return status, runtime, response, reasons
}

func statusRank(status string) int {
	switch status {
	case StatusCritical:
		return 4
	case StatusDisconnected:
		return 3
	case StatusWarning:
		return 2
	case StatusOK:
		return 1
	}
	return 0
}

// buildTopologyView는 스냅샷, 응답 상태, 최신 지표를 화면용 구조로 합친다.
func buildTopologyView(t *protocol.Topology, probes map[string][]ProbeView, latest map[string]map[string]LatestValue, fresh, historical bool) TopologyView {
	byService := map[string][]protocol.Container{}
	for _, c := range t.Containers {
		byService[c.ServiceKey()] = append(byService[c.ServiceKey()], c)
	}

	byProject := map[string][]ServiceView{}
	for key, containers := range byService {
		sort.Slice(containers, func(i, j int) bool {
			if containers[i].Replica != containers[j].Replica {
				return containers[i].Replica < containers[j].Replica
			}
			return containers[i].ID < containers[j].ID
		})
		serviceProbes := probes[key]
		if serviceProbes == nil {
			serviceProbes = []ProbeView{}
		}
		status, runtime, response, reasons := rollupService(containers, serviceProbes, fresh, historical)
		service := ServiceView{
			Key: key, Name: containers[0].Service, Project: strings.SplitN(key, "/", 2)[0],
			Status: status, Runtime: runtime, Response: response, Reasons: reasons, Probes: serviceProbes,
		}
		for _, c := range containers {
			metrics := latest[protocol.ContainerScope(key, c.Replica)]
			if metrics == nil {
				metrics = map[string]LatestValue{}
			}
			service.Containers = append(service.Containers, ContainerView{Container: c, Metrics: metrics})
		}
		byProject[service.Project] = append(byProject[service.Project], service)
	}

	view := TopologyView{ObservedAt: t.ObservedAt, Historical: historical, Projects: []ProjectView{}}
	for key, services := range byProject {
		sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
		project := ProjectView{Key: key, Status: StatusStopped, Services: services}
		for _, service := range services {
			if statusRank(service.Status) > statusRank(project.Status) {
				project.Status = service.Status
			}
		}
		view.Projects = append(view.Projects, project)
	}
	sort.Slice(view.Projects, func(i, j int) bool {
		a, b := view.Projects[i].Key, view.Projects[j].Key
		// Compose 라벨이 없는 컨테이너 묶음은 목록 맨 뒤에 둔다.
		if (a == protocol.StandaloneProject) != (b == protocol.StandaloneProject) {
			return b == protocol.StandaloneProject
		}
		return a < b
	})

	view.Relations = buildRelations(t)
	view.Networks = buildNetworks(t)
	return view
}

// buildRelations는 근거가 있는 관계만 만든다. 네트워크 소속은 네트워크로만 연결한다.
func buildRelations(t *protocol.Topology) []Relation {
	relations := []Relation{}
	seen := map[Relation]bool{}
	add := func(r Relation) {
		if !seen[r] {
			seen[r] = true
			relations = append(relations, r)
		}
	}
	for _, c := range t.Containers {
		key := c.ServiceKey()
		for _, attachment := range c.Networks {
			add(Relation{Type: RelationMemberOf, From: key, To: "network:" + attachment.Network, Evidence: "docker-inspect"})
		}
		for _, p := range c.Ports {
			if p.HostPort == 0 {
				continue
			}
			add(Relation{Type: RelationPublish, From: "host", To: key, Evidence: "docker-inspect", Detail: portText(p)})
		}
		for _, dep := range c.DeclaredDependsOn {
			add(Relation{Type: RelationDeclared, From: key, To: protocol.ServiceKey(c.Project, dep), Evidence: "compose-label"})
		}
	}
	sort.Slice(relations, func(i, j int) bool {
		a, b := relations[i], relations[j]
		return a.Type+a.From+a.To+a.Detail < b.Type+b.From+b.To+b.Detail
	})
	return relations
}

func portText(p protocol.PortBinding) string {
	host := p.HostIP
	if host == "" {
		host = "0.0.0.0"
	}
	return host + ":" + itoa(p.HostPort) + "->" + itoa(p.ContainerPort) + "/" + p.Proto
}

// buildNetworks는 컨테이너가 하나라도 연결된 네트워크만 표시한다. Docker 기본 네트워크가 맵을 채우지 않게 한다.
func buildNetworks(t *protocol.Topology) []NetworkView {
	members := map[string]map[string]bool{}
	projects := map[string]map[string]bool{}
	for _, c := range t.Containers {
		for _, attachment := range c.Networks {
			if members[attachment.Network] == nil {
				members[attachment.Network] = map[string]bool{}
				projects[attachment.Network] = map[string]bool{}
			}
			members[attachment.Network][c.ServiceKey()] = true
			projects[attachment.Network][c.Project] = true
		}
	}
	views := []NetworkView{}
	for _, network := range t.Networks {
		if len(members[network.Name]) == 0 {
			continue
		}
		view := NetworkView{Network: network, Shared: len(projects[network.Name]) > 1, Members: []string{}}
		for member := range members[network.Name] {
			view.Members = append(view.Members, member)
		}
		sort.Strings(view.Members)
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Name < views[j].Name })
	return views
}
