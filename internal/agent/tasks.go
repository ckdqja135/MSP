package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ckdqja135/MSP/internal/agent/dockerapi"
	"github.com/ckdqja135/MSP/internal/agent/mask"
	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	taskPollTimeout   = 40 * time.Second
	logChunkLines     = 200
	logFlushEvery     = 500 * time.Millisecond
	logLinesPerSecond = 1000
	maxLogSession     = 30 * time.Minute
	maxLogTasks       = 3
)

// resolveLogContainer는 요청을 자신의 구성에 있는 컨테이너로만 연결한다.
func (a *Agent) resolveLogContainer(request protocol.LogRequest) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.topology == nil {
		return "", errors.New("구성을 아직 읽지 못했습니다")
	}
	for _, c := range a.topology.Containers {
		if c.ServiceKey() != request.ServiceKey {
			continue
		}
		if request.ContainerID == "" || request.ContainerID == c.ID {
			return c.ID, nil
		}
	}
	return "", errors.New("이 서버의 구성에 없는 컨테이너입니다")
}

// runLogTask는 Docker 로그를 읽어 마스킹한 뒤 조금씩 보낸다.
func (a *Agent) runLogTask(ctx context.Context, task protocol.Task) {
	ctx, cancel := context.WithTimeout(ctx, maxLogSession)
	defer cancel()
	request := *task.Logs

	var mu sync.Mutex
	var lines []protocol.LogLine
	dropped := 0
	windowStart := time.Now()
	windowCount := 0

	send := func(done bool, message string) {
		mu.Lock()
		chunk := protocol.LogChunk{Lines: lines, Dropped: dropped, Done: done, Error: message}
		lines = nil
		dropped = 0
		mu.Unlock()
		if len(chunk.Lines) == 0 && chunk.Dropped == 0 && !done {
			return
		}
		if keep, err := a.postLogChunk(ctx, task.ID, chunk); err != nil || !keep {
			cancel()
		}
	}

	containerID, err := a.resolveLogContainer(request)
	if err != nil {
		send(true, err.Error())
		return
	}

	stop := make(chan struct{})
	var flusher sync.WaitGroup
	flusher.Add(1)
	go func() {
		defer flusher.Done()
		ticker := time.NewTicker(logFlushEvery)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				send(false, "")
			}
		}
	}()

	needle := strings.ToLower(request.Query)
	options := dockerapi.LogOptions{Tail: request.Tail, Follow: request.Follow}
	if request.Since != nil {
		options.Since = *request.Since
	}
	if request.Until != nil {
		options.Until = *request.Until
	}
	err = a.docker.Logs(ctx, containerID, options, func(entry dockerapi.LogEntry) bool {
		// 민감정보를 먼저 가린다. 검색어로 가려진 값을 찾을 수 없게 하기 위해서다.
		text := mask.Line(entry.Text)
		if needle != "" && !strings.Contains(strings.ToLower(text), needle) {
			return ctx.Err() == nil
		}
		mu.Lock()
		now := time.Now()
		if now.Sub(windowStart) >= time.Second {
			windowStart, windowCount = now, 0
		}
		windowCount++
		over := windowCount > logLinesPerSecond
		if over {
			dropped++
		} else {
			lines = append(lines, protocol.LogLine{At: entry.At, Stream: entry.Stream, Text: text})
		}
		full := len(lines) >= logChunkLines
		mu.Unlock()
		if full {
			send(false, "")
		}
		return ctx.Err() == nil
	})
	close(stop)
	flusher.Wait()

	message := ""
	if err != nil && ctx.Err() == nil {
		message = err.Error()
	}
	if ctx.Err() == nil {
		send(true, message)
	}
}

func (a *Agent) postLogChunk(ctx context.Context, taskID string, chunk protocol.LogChunk) (bool, error) {
	body, err := json.Marshal(chunk)
	if err != nil {
		return false, err
	}
	target := strings.TrimRight(a.cfg.CentralURL, "/") + "/agent/v1/tasks/" + taskID + "/logs"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	a.authorize(request)
	response, err := a.client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false, fmt.Errorf("중앙 서버 응답 %d", response.StatusCode)
	}
	var ack protocol.ChunkAck
	if err := json.NewDecoder(response.Body).Decode(&ack); err != nil {
		return false, err
	}
	return ack.Continue, nil
}

// fetchTask는 중앙 서버가 줄 작업을 기다린다. 에이전트가 먼저 연결하므로 인바운드 포트가 필요 없다.
func (a *Agent) fetchTask(ctx context.Context) (*protocol.Task, error) {
	target := strings.TrimRight(a.cfg.CentralURL, "/") + "/agent/v1/tasks"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	a.authorize(request)
	client := http.Client{Timeout: taskPollTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusNoContent:
		return nil, nil
	case http.StatusOK:
		var task protocol.Task
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&task); err != nil {
			return nil, err
		}
		return &task, nil
	}
	return nil, fmt.Errorf("중앙 서버 응답 %d", response.StatusCode)
}

func (a *Agent) pollTasks(ctx context.Context) {
	backoff := time.Second
	limit := make(chan struct{}, maxLogTasks)
	for ctx.Err() == nil {
		task, err := a.fetchTask(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = time.Second
		if task == nil {
			continue
		}
		// 알 수 없는 작업은 실행하지 않는다.
		if task.Kind != protocol.TaskLogs || task.Logs == nil {
			log.Printf("지원하지 않는 작업을 무시합니다: %q", task.Kind)
			continue
		}
		select {
		case limit <- struct{}{}:
			go func(task protocol.Task) {
				defer func() { <-limit }()
				a.runLogTask(ctx, task)
			}(*task)
		default:
			if _, err := a.postLogChunk(ctx, task.ID, protocol.LogChunk{Done: true, Error: "동시 로그 조회 수를 넘었습니다"}); err != nil {
				log.Printf("로그 조회 거절 전송 실패: %v", err)
			}
		}
	}
}
