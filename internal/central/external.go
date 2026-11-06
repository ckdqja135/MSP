package central

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/ckdqja135/MSP/internal/probe"
	"github.com/ckdqja135/MSP/internal/protocol"
)

const defaultExternalTimeout = 5 * time.Second

// ExternalProbe는 중앙 서버가 바깥에서 확인하는 주소다.
type ExternalProbe struct {
	Server    string `json:"server"`
	Service   string `json:"service"`
	URL       string `json:"url"`
	TimeoutMS int64  `json:"timeout_ms"`
}

// RunExternalProbes는 외부 관측점으로서 서비스 응답을 주기적으로 확인한다.
func (s *Server) RunExternalProbes(ctx context.Context, probes []ExternalProbe, every time.Duration) {
	if len(probes) == 0 {
		return
	}
	client := probe.NewClient()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkExternal(ctx, client, probes)
		}
	}
}

func (s *Server) checkExternal(ctx context.Context, client *http.Client, probes []ExternalProbe) {
	results := make([]protocol.Probe, len(probes))
	var wg sync.WaitGroup
	for i, target := range probes {
		wg.Add(1)
		go func(i int, target ExternalProbe) {
			defer wg.Done()
			timeout := time.Duration(target.TimeoutMS) * time.Millisecond
			if timeout <= 0 {
				timeout = defaultExternalTimeout
			}
			result := probe.Run(ctx, client, target.URL, timeout)
			results[i] = protocol.Probe{
				At: s.now(), ServiceKey: target.Service, Vantage: protocol.VantageExternal,
				Target: "GET " + target.URL, OK: result.OK, Failure: result.Failure,
				LatencyMS: result.LatencyMS, TimeoutMS: timeout.Milliseconds(),
			}
		}(i, target)
	}
	wg.Wait()
	byServer := map[string][]protocol.Probe{}
	for i, target := range probes {
		byServer[target.Server] = append(byServer[target.Server], results[i])
	}
	for serverID, list := range byServer {
		if err := s.store.RecordProbes(ctx, serverID, list, s.now()); err != nil {
			log.Printf("외부 응답 확인 저장 실패: %v", err)
			continue
		}
		s.hub.Publish(serverID)
	}
}

// RunHeartbeat는 중앙 서버가 살아 있다는 신호를 외부 서비스로 보낸다.
// 신호가 끊기면 외부 서비스가 알려 주므로 중앙 서버 자체의 중단도 감지할 수 있다.
func RunHeartbeat(ctx context.Context, target string, every time.Duration) {
	if target == "" {
		return
	}
	client := http.Client{Timeout: 10 * time.Second}
	send := func() {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			log.Print("heartbeat 주소가 잘못되었습니다")
			return
		}
		response, err := client.Do(request)
		if err != nil {
			log.Print("heartbeat 전송 실패")
			return
		}
		response.Body.Close()
	}
	send()
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			send()
		}
	}
}

// ConnectivityCheck는 공용 주소 중 하나라도 닿으면 true를 돌려준다. 주소가 없으면 확인하지 않는다.
func ConnectivityCheck(targets []string) func(ctx context.Context) bool {
	if len(targets) == 0 {
		return nil
	}
	client := probe.NewClient()
	return func(ctx context.Context) bool {
		for _, target := range targets {
			result := probe.Run(ctx, client, target, 3*time.Second)
			if result.LatencyMS != nil {
				return true
			}
		}
		return false
	}
}
