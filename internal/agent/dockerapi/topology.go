package dockerapi

import (
	"context"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	labelProject   = "com.docker.compose.project"
	labelService   = "com.docker.compose.service"
	labelNumber    = "com.docker.compose.container-number"
	labelOneOff    = "com.docker.compose.oneoff"
	labelDependsOn = "com.docker.compose.depends_on"
)

// inspectResponse에는 필요한 필드만 정의한다. Config.Env와 Config.Cmd는 읽지 않는다.
type inspectResponse struct {
	ID           string    `json:"Id"`
	Name         string    `json:"Name"`
	Created      time.Time `json:"Created"`
	Image        string    `json:"Image"`
	RestartCount int       `json:"RestartCount"`
	State        struct {
		Status    string    `json:"Status"`
		ExitCode  int       `json:"ExitCode"`
		OOMKilled bool      `json:"OOMKilled"`
		StartedAt time.Time `json:"StartedAt"`
		Health    *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	HostConfig struct {
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`
	NetworkSettings struct {
		Networks map[string]struct {
			IPAddress string `json:"IPAddress"`
		} `json:"Networks"`
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
	} `json:"NetworkSettings"`
}

func (r inspectResponse) toContainer() protocol.Container {
	name := strings.TrimPrefix(r.Name, "/")
	labels := r.Config.Labels
	c := protocol.Container{
		ID:                r.ID,
		Name:              name,
		Project:           labels[labelProject],
		Service:           labels[labelService],
		OneOff:            strings.EqualFold(labels[labelOneOff], "true"),
		Image:             r.Config.Image,
		ImageID:           r.Image,
		State:             r.State.Status,
		ExitCode:          r.State.ExitCode,
		OOMKilled:         r.State.OOMKilled,
		Health:            "none",
		RestartCount:      r.RestartCount,
		RestartPolicy:     r.HostConfig.RestartPolicy.Name,
		CreatedAt:         r.Created,
		StartedAt:         r.State.StartedAt,
		DeclaredDependsOn: parseDependsOn(labels[labelDependsOn]),
	}
	if c.Service == "" {
		c.Service = name
	}
	if n, err := strconv.Atoi(labels[labelNumber]); err == nil {
		c.Replica = n
	}
	if r.State.Health != nil && r.State.Health.Status != "" {
		c.Health = r.State.Health.Status
	}
	for network, attachment := range r.NetworkSettings.Networks {
		c.Networks = append(c.Networks, protocol.NetworkAttachment{Network: network, IP: attachment.IPAddress})
	}
	sort.Slice(c.Networks, func(i, j int) bool { return c.Networks[i].Network < c.Networks[j].Network })
	c.Ports = parsePorts(r)
	return c
}

// parseDependsOn은 "db:service_started:false,cache:service_healthy:true"에서 서비스 이름만 꺼낸다.
func parseDependsOn(label string) []string {
	if label == "" {
		return nil
	}
	var deps []string
	for _, part := range strings.Split(label, ",") {
		if name, _, _ := strings.Cut(part, ":"); name != "" {
			deps = append(deps, name)
		}
	}
	sort.Strings(deps)
	return deps
}

func parsePorts(r inspectResponse) []protocol.PortBinding {
	var ports []protocol.PortBinding
	for spec, bindings := range r.NetworkSettings.Ports {
		portText, proto, _ := strings.Cut(spec, "/")
		containerPort, err := strconv.Atoi(portText)
		if err != nil {
			continue
		}
		if len(bindings) == 0 {
			ports = append(ports, protocol.PortBinding{ContainerPort: containerPort, Proto: proto})
			continue
		}
		for _, b := range bindings {
			hostPort, _ := strconv.Atoi(b.HostPort)
			ports = append(ports, protocol.PortBinding{
				HostIP: b.HostIP, HostPort: hostPort, ContainerPort: containerPort, Proto: proto,
			})
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		a, b := ports[i], ports[j]
		if a.ContainerPort != b.ContainerPort {
			return a.ContainerPort < b.ContainerPort
		}
		if a.HostPort != b.HostPort {
			return a.HostPort < b.HostPort
		}
		return a.HostIP < b.HostIP
	})
	return ports
}

type networkResponse struct {
	ID       string `json:"Id"`
	Name     string `json:"Name"`
	Driver   string `json:"Driver"`
	Internal bool   `json:"Internal"`
	IPAM     struct {
		Config []struct {
			Subnet string `json:"Subnet"`
		} `json:"Config"`
	} `json:"IPAM"`
}

// Topology는 컨테이너와 네트워크 구성을 읽어 스냅샷을 만든다.
func (c *Client) Topology(ctx context.Context) (*protocol.Topology, error) {
	var list []struct {
		ID string `json:"Id"`
	}
	query := url.Values{"all": {"true"}}
	if err := c.getJSON(ctx, "/containers/json", query, &list); err != nil {
		return nil, err
	}

	t := &protocol.Topology{ObservedAt: time.Now().UTC()}
	for _, item := range list {
		var inspected inspectResponse
		if err := c.getJSON(ctx, "/containers/"+item.ID+"/json", nil, &inspected); err != nil {
			// 목록 조회와 inspect 사이에 삭제된 컨테이너는 건너뛴다.
			if isNotFound(err) {
				continue
			}
			return nil, err
		}
		t.Containers = append(t.Containers, inspected.toContainer())
	}

	var networks []networkResponse
	if err := c.getJSON(ctx, "/networks", nil, &networks); err != nil {
		return nil, err
	}
	for _, n := range networks {
		network := protocol.Network{ID: n.ID, Name: n.Name, Driver: n.Driver, Internal: n.Internal}
		if len(n.IPAM.Config) > 0 {
			network.Subnet = n.IPAM.Config[0].Subnet
		}
		t.Networks = append(t.Networks, network)
	}
	t.Normalize()
	return t, nil
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(err.Error(), "status 404")
}
