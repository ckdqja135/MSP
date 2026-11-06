package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"strconv"
	"time"
)

// Stats는 컨테이너의 누적 값이다. 비율 계산은 호출자가 이전 값과 비교해 수행한다.
type Stats struct {
	CPUTotal    uint64
	SystemCPU   uint64
	OnlineCPUs  uint32
	MemUsage    uint64
	MemLimit    uint64
	MemInactive uint64
	NetRx       uint64
	NetTx       uint64
}

// Stats는 한 번만 읽는다. 스트림을 열어 두지 않아 컨테이너 수만큼 연결이 쌓이지 않는다.
func (c *Client) Stats(ctx context.Context, id string) (Stats, error) {
	var raw struct {
		CPU struct {
			Usage struct {
				Total uint64 `json:"total_usage"`
			} `json:"cpu_usage"`
			System uint64 `json:"system_cpu_usage"`
			Online uint32 `json:"online_cpus"`
		} `json:"cpu_stats"`
		Memory struct {
			Usage uint64 `json:"usage"`
			Limit uint64 `json:"limit"`
			Stats struct {
				InactiveFile uint64 `json:"inactive_file"`
			} `json:"stats"`
		} `json:"memory_stats"`
		Networks map[string]struct {
			Rx uint64 `json:"rx_bytes"`
			Tx uint64 `json:"tx_bytes"`
		} `json:"networks"`
	}
	query := url.Values{"stream": {"false"}, "one-shot": {"true"}}
	if err := c.getJSON(ctx, "/containers/"+id+"/stats", query, &raw); err != nil {
		return Stats{}, err
	}
	stats := Stats{
		CPUTotal:    raw.CPU.Usage.Total,
		SystemCPU:   raw.CPU.System,
		OnlineCPUs:  raw.CPU.Online,
		MemUsage:    raw.Memory.Usage,
		MemLimit:    raw.Memory.Limit,
		MemInactive: raw.Memory.Stats.InactiveFile,
	}
	for _, network := range raw.Networks {
		stats.NetRx += network.Rx
		stats.NetTx += network.Tx
	}
	return stats, nil
}

// Event는 Docker 이벤트에서 허용한 속성만 담는다. 임의의 라벨은 전달하지 않는다.
type Event struct {
	Type        string
	Action      string
	ActorID     string
	At          time.Time
	Name        string
	ExitCode    string
	Signal      string
	Project     string
	Service     string
	Replica     int
	ContainerID string
}

// Events는 스트림이 끝나거나 ctx가 취소될 때까지 이벤트를 전달한다.
func (c *Client) Events(ctx context.Context, since time.Time, handle func(Event)) error {
	query := url.Values{"filters": {`{"type":["container","network"]}`}}
	if !since.IsZero() {
		query.Set("since", strconv.FormatInt(since.Unix(), 10))
	}
	resp, err := c.open(ctx, "/events", query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	decoder := json.NewDecoder(resp.Body)
	for {
		var raw struct {
			Type   string `json:"Type"`
			Action string `json:"Action"`
			Actor  struct {
				ID         string            `json:"ID"`
				Attributes map[string]string `json:"Attributes"`
			} `json:"Actor"`
			TimeNano int64 `json:"timeNano"`
		}
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		attributes := raw.Actor.Attributes
		event := Event{
			Type:        raw.Type,
			Action:      raw.Action,
			ActorID:     raw.Actor.ID,
			At:          time.Unix(0, raw.TimeNano).UTC(),
			Name:        attributes["name"],
			ExitCode:    attributes["exitCode"],
			Signal:      attributes["signal"],
			Project:     attributes[labelProject],
			Service:     attributes[labelService],
			ContainerID: attributes["container"],
		}
		event.Replica, _ = strconv.Atoi(attributes[labelNumber])
		handle(event)
	}
}
