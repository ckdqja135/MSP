package central

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	taskWait          = 25 * time.Second
	agentOnlineWithin = 2 * taskWait
	logFirstReplyWait = 10 * time.Second
	chunkPushWait     = 5 * time.Second
	maxLogStreams     = 3
	defaultLogTail    = 300
	maxLogTail        = 5000
)

// ErrAgentOffline은 요청을 맡길 에이전트가 없는 경우다. 작업을 쌓아 두지 않고 바로 알린다.
var ErrAgentOffline = errors.New("에이전트가 연결되어 있지 않아 로그를 조회할 수 없습니다")

// ErrTooManyStreams는 서버별 동시 조회 수를 넘은 경우다.
var ErrTooManyStreams = errors.New("동시에 볼 수 있는 로그 수를 넘었습니다")

// LogStreamer는 로그를 조금씩 전달한다. emit이 false를 돌려주면 멈춘다.
type LogStreamer func(ctx context.Context, serverID string, request protocol.LogRequest, emit func(protocol.LogChunk) bool) error

type logSession struct {
	serverID string
	chunks   chan protocol.LogChunk
	closed   chan struct{}
}

// LogRelay는 화면의 로그 요청을 에이전트 작업으로 바꾸고 결과를 전달한다.
type LogRelay struct {
	mu       sync.Mutex
	queues   map[string]chan protocol.Task
	sessions map[string]*logSession
	lastPoll map[string]time.Time
	counts   map[string]int
}

func NewLogRelay() *LogRelay {
	return &LogRelay{
		queues:   map[string]chan protocol.Task{},
		sessions: map[string]*logSession{},
		lastPoll: map[string]time.Time{},
		counts:   map[string]int{},
	}
}

func (r *LogRelay) queue(serverID string) chan protocol.Task {
	queue, ok := r.queues[serverID]
	if !ok {
		queue = make(chan protocol.Task, maxLogStreams)
		r.queues[serverID] = queue
	}
	return queue
}

// Stream은 작업을 에이전트에 맡기고 도착한 내용을 전달한다.
func (r *LogRelay) Stream(ctx context.Context, serverID string, request protocol.LogRequest, emit func(protocol.LogChunk) bool) error {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	id := hex.EncodeToString(raw)
	session := &logSession{serverID: serverID, chunks: make(chan protocol.LogChunk, 64), closed: make(chan struct{})}

	r.mu.Lock()
	if time.Since(r.lastPoll[serverID]) > agentOnlineWithin {
		r.mu.Unlock()
		return ErrAgentOffline
	}
	if r.counts[serverID] >= maxLogStreams {
		r.mu.Unlock()
		return ErrTooManyStreams
	}
	r.counts[serverID]++
	r.sessions[id] = session
	queue := r.queue(serverID)
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		delete(r.sessions, id)
		r.counts[serverID]--
		r.mu.Unlock()
		close(session.closed)
	}()

	select {
	case queue <- protocol.Task{ID: id, Kind: protocol.TaskLogs, Logs: &request}:
	default:
		return ErrTooManyStreams
	}

	// 첫 응답이 오기 전에 멈춘 에이전트를 기다리지 않는다.
	timeout := time.NewTimer(logFirstReplyWait)
	defer timeout.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timeout.C:
			return ErrAgentOffline
		case chunk := <-session.chunks:
			timeout.Stop()
			if !emit(chunk) || chunk.Done {
				return nil
			}
		}
	}
}

func (s *Server) handleTaskPoll(w http.ResponseWriter, r *http.Request) {
	serverID, ok := s.authenticateAgent(w, r)
	if !ok {
		return
	}
	s.logs.mu.Lock()
	s.logs.lastPoll[serverID] = time.Now()
	queue := s.logs.queue(serverID)
	s.logs.mu.Unlock()

	timer := time.NewTimer(taskWait)
	defer timer.Stop()
	select {
	case <-r.Context().Done():
	case <-timer.C:
		w.WriteHeader(http.StatusNoContent)
	case task := <-queue:
		writeJSON(w, task)
	}
}

func (s *Server) handleLogChunk(w http.ResponseWriter, r *http.Request) {
	serverID, ok := s.authenticateAgent(w, r)
	if !ok {
		return
	}
	var chunk protocol.LogChunk
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBatchBytes)).Decode(&chunk); err != nil {
		http.Error(w, "잘못된 요청입니다", http.StatusBadRequest)
		return
	}
	s.logs.mu.Lock()
	session := s.logs.sessions[r.PathValue("id")]
	s.logs.mu.Unlock()
	// 화면이 떠났거나 다른 서버의 작업이면 에이전트가 읽기를 멈추게 한다.
	if session == nil || session.serverID != serverID {
		writeJSON(w, protocol.ChunkAck{Continue: false})
		return
	}
	timer := time.NewTimer(chunkPushWait)
	defer timer.Stop()
	select {
	case session.chunks <- chunk:
		writeJSON(w, protocol.ChunkAck{Continue: !chunk.Done})
	case <-session.closed:
		writeJSON(w, protocol.ChunkAck{Continue: false})
	case <-timer.C:
		writeJSON(w, protocol.ChunkAck{Continue: false})
	}
}

func parseOptionalTime(text string) (*time.Time, error) {
	if text == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, text)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "스트리밍을 지원하지 않습니다", http.StatusInternalServerError)
		return
	}
	query := r.URL.Query()
	request := protocol.LogRequest{
		ServiceKey:  query.Get("service"),
		ContainerID: query.Get("container"),
		Query:       query.Get("q"),
		Follow:      query.Get("follow") == "true",
	}
	if request.ServiceKey == "" {
		http.Error(w, "service가 필요합니다", http.StatusBadRequest)
		return
	}
	var err error
	if request.Since, err = parseOptionalTime(query.Get("since")); err != nil {
		http.Error(w, "since 형식이 잘못되었습니다", http.StatusBadRequest)
		return
	}
	if request.Until, err = parseOptionalTime(query.Get("until")); err != nil {
		http.Error(w, "until 형식이 잘못되었습니다", http.StatusBadRequest)
		return
	}
	request.Tail, _ = strconv.Atoi(query.Get("tail"))
	if request.Tail <= 0 {
		request.Tail = defaultLogTail
	}
	if request.Tail > maxLogTail {
		request.Tail = maxLogTail
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	write := func(chunk protocol.LogChunk) bool {
		body, err := json.Marshal(chunk)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if err := s.cfg.LogStreamer(r.Context(), r.PathValue("id"), request, write); err != nil {
		write(protocol.LogChunk{Done: true, Error: err.Error()})
	}
}
