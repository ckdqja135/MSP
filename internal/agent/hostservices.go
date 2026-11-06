package agent

import (
	"net"
	"sort"
	"strings"

	"github.com/ckdqja135/MSP/internal/agent/hostports"
	"github.com/ckdqja135/MSP/internal/protocol"
)

// 잘 알려진 프로세스와 포트의 짝이다. 소켓 주인을 읽지 못할 때 이름을 추정하는 데만 쓴다.
var wellKnownPorts = map[int][]string{
	22:   {"sshd"},
	80:   {"nginx", "apache2", "httpd", "caddy", "haproxy", "traefik"},
	443:  {"nginx", "apache2", "httpd", "caddy", "haproxy", "traefik"},
	3306: {"mysqld", "mariadbd"},
	5432: {"postgres"},
	6379: {"redis-server"},
}

// isLoopbackIP는 127.0.0.0/8 전체를 루프백으로 본다. 127.0.0.53 같은 주소도 외부에서 닿지 않는다.
func isLoopbackIP(ip string) bool {
	parsed := net.ParseIP(ip)
	return parsed != nil && parsed.IsLoopback()
}

// guessName은 후보가 하나일 때만 이름을 추정한다. 둘 이상이면 어느 쪽인지 알 수 없다.
func guessName(port int, running map[string]bool) string {
	found := ""
	for _, name := range wellKnownPorts[port] {
		if !running[name] {
			continue
		}
		if found != "" {
			return ""
		}
		found = name
	}
	return found
}

// deriveHostServices는 열린 포트를 서비스로 묶는다. 컨테이너가 공개한 포트와 루프백 전용 포트는 제외한다.
func deriveHostServices(listeners []hostports.Listener, running map[string]bool, containers []protocol.Container, declared []DeclaredHostService) []protocol.HostService {
	published := map[int]bool{}
	for _, c := range containers {
		for _, port := range c.Ports {
			if port.HostPort != 0 {
				published[port.HostPort] = true
			}
		}
	}
	declaredByPort := map[int]string{}
	for _, service := range declared {
		for _, port := range service.Ports {
			declaredByPort[port] = service.Name
		}
	}

	type key struct{ name, evidence string }
	groups := map[key][]protocol.HostPort{}
	for _, listener := range listeners {
		declaredName, isDeclared := declaredByPort[listener.Port]
		if published[listener.Port] || (isLoopbackIP(listener.IP) && !isDeclared) {
			continue
		}
		var group key
		switch {
		case isDeclared:
			group = key{declaredName, protocol.HostEvidenceConfig}
		case listener.Process != "":
			group = key{listener.Process, protocol.HostEvidenceOwner}
		default:
			group = key{"", protocol.HostEvidenceUnknown}
			if name := guessName(listener.Port, running); name != "" {
				group = key{name, protocol.HostEvidenceName}
			}
		}
		groups[group] = append(groups[group], protocol.HostPort{IP: listener.IP, Port: listener.Port})
	}

	services := make([]protocol.HostService, 0, len(groups)+len(declared))
	listeningDeclared := map[string]bool{}
	for group, ports := range groups {
		if group.evidence == protocol.HostEvidenceConfig {
			listeningDeclared[group.name] = true
		}
		services = append(services, protocol.HostService{Name: group.name, Ports: dedupePorts(ports), Evidence: group.evidence, Listening: true})
	}
	// 설정에 적었는데 열려 있지 않으면 닫힌 서비스로 남긴다. 사라진 것처럼 보이면 장애를 놓친다.
	for _, service := range declared {
		if listeningDeclared[service.Name] {
			continue
		}
		ports := make([]protocol.HostPort, 0, len(service.Ports))
		for _, port := range service.Ports {
			ports = append(ports, protocol.HostPort{Port: port})
		}
		services = append(services, protocol.HostService{Name: service.Name, Ports: ports, Evidence: protocol.HostEvidenceConfig})
	}
	sort.Slice(services, func(i, j int) bool { return strings.Compare(services[i].Name, services[j].Name) < 0 })
	return services
}

func dedupePorts(ports []protocol.HostPort) []protocol.HostPort {
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Port != ports[j].Port {
			return ports[i].Port < ports[j].Port
		}
		return ports[i].IP < ports[j].IP
	})
	return ports
}
