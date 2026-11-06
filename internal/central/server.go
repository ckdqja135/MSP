package central

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const maxBatchBytes = 16 << 20

// Config는 중앙 서버 설정이다. 비밀번호가 없으면 루프백에서만 시작할 수 있다.
type Config struct {
	Listen            string
	WebDir            string
	DashboardPassword string
	// LogStreamer는 로그를 가져오는 방법이다. 없으면 에이전트에 요청한다. 데모는 가짜 로그를 넣는다.
	LogStreamer LogStreamer
}

// ValidateListen은 인증 없이 외부 주소에 바인딩하는 설정을 거부한다.
func ValidateListen(listen, password string) error {
	if isLoopbackAddr(listen) || password != "" {
		return nil
	}
	return fmt.Errorf("%s는 루프백 주소가 아닙니다. MSP_DASHBOARD_PASSWORD를 설정해야 시작할 수 있습니다", listen)
}

func isLoopbackAddr(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Hub는 데이터가 바뀌었다는 신호를 화면에 전달한다. 화면은 신호를 받으면 다시 조회한다.
type Hub struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan string]struct{}{}} }

func (h *Hub) subscribe() chan string {
	ch := make(chan string, 8)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

func (h *Hub) unsubscribe(ch chan string) {
	h.mu.Lock()
	delete(h.subs, ch)
	h.mu.Unlock()
}

// Publish는 느린 구독자를 기다리지 않는다. 신호는 다시 조회를 유도할 뿐이라 일부를 놓쳐도 된다.
func (h *Hub) Publish(message string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- message:
		default:
		}
	}
}

type Server struct {
	store *Store
	hub   *Hub
	cfg   Config
	logs  *LogRelay
	now   func() time.Time
}

func NewServer(store *Store, hub *Hub, cfg Config) *Server {
	relay := NewLogRelay()
	if cfg.LogStreamer == nil {
		cfg.LogStreamer = relay.Stream
	}
	return &Server{store: store, hub: hub, cfg: cfg, logs: relay, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Server) Hub() *Hub { return s.hub }

func (s *Server) Handler() http.Handler {
	api := http.NewServeMux()
	api.HandleFunc("GET /api/v1/meta", s.handleMeta)
	api.HandleFunc("GET /api/v1/servers", s.handleServers)
	api.HandleFunc("GET /api/v1/servers/{id}/topology", s.handleTopology)
	api.HandleFunc("GET /api/v1/servers/{id}/events", s.handleEvents)
	api.HandleFunc("GET /api/v1/servers/{id}/gaps", s.handleGaps)
	api.HandleFunc("GET /api/v1/servers/{id}/samples", s.handleSamples)
	api.HandleFunc("GET /api/v1/servers/{id}/logs", s.handleLogs)
	api.HandleFunc("GET /api/v1/alerts", s.handleAlerts)
	api.HandleFunc("GET /api/v1/stream", s.handleStream)
	if s.cfg.WebDir != "" {
		api.Handle("GET /", spaHandler(s.cfg.WebDir))
	}

	root := http.NewServeMux()
	root.HandleFunc("POST /agent/v1/batches", s.handleBatch)
	root.HandleFunc("GET /agent/v1/tasks", s.handleTaskPoll)
	root.HandleFunc("POST /agent/v1/tasks/{id}/logs", s.handleLogChunk)
	root.Handle("/", s.dashboardGuard(api))
	return root
}

// dashboardGuard는 대시보드 접근을 제한한다.
// 루프백 전용일 때는 Host도 확인해 DNS 리바인딩으로 외부 페이지가 읽어가는 것을 막는다.
func (s *Server) dashboardGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.DashboardPassword == "" {
			if !isLoopbackHost(r.Host) {
				http.Error(w, "허용되지 않은 Host입니다", http.StatusForbidden)
				return
			}
		} else {
			_, password, ok := r.BasicAuth()
			if !ok || subtle.ConstantTimeCompare([]byte(password), []byte(s.cfg.DashboardPassword)) != 1 {
				w.Header().Set("WWW-Authenticate", `Basic realm="MSP"`)
				http.Error(w, "인증이 필요합니다", http.StatusUnauthorized)
				return
			}
		}
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func isLoopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// authenticateAgent는 에이전트 요청을 인증하고 서버 ID를 돌려준다.
func (s *Server) authenticateAgent(w http.ResponseWriter, r *http.Request) (string, bool) {
	if s.store.Mode() == protocol.ModeDemo {
		http.Error(w, "데모 모드는 실제 에이전트 데이터를 받지 않습니다", http.StatusForbidden)
		return "", false
	}
	serverID := r.Header.Get("X-MSP-Server")
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if serverID == "" || !s.store.Authenticate(r.Context(), serverID, token) {
		http.Error(w, "인증 실패", http.StatusUnauthorized)
		return "", false
	}
	return serverID, true
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	serverID, ok := s.authenticateAgent(w, r)
	if !ok {
		return
	}

	var batch protocol.Batch
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBatchBytes)).Decode(&batch); err != nil {
		http.Error(w, "잘못된 배치입니다", http.StatusBadRequest)
		return
	}
	if batch.ServerID != serverID {
		http.Error(w, "서버 ID가 인증 정보와 다릅니다", http.StatusForbidden)
		return
	}
	if batch.ProtocolVersion != protocol.Version {
		http.Error(w, "지원하지 않는 프로토콜 버전입니다", http.StatusBadRequest)
		return
	}

	ack, err := s.Ingest(r.Context(), batch)
	if err != nil {
		log.Printf("수신 실패 server=%s: %v", serverID, err)
		http.Error(w, "저장 실패", http.StatusInternalServerError)
		return
	}
	writeJSON(w, ack)
}

// Ingest는 배치를 저장하고 화면에 변경 신호를 보낸다.
func (s *Server) Ingest(ctx context.Context, batch protocol.Batch) (protocol.Ack, error) {
	ack, err := s.store.Ingest(ctx, batch, s.now())
	if err == nil && !ack.Duplicate {
		s.hub.Publish(batch.ServerID)
	}
	return ack, err
}

func (s *Server) handleMeta(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"mode":             s.store.Mode(),
		"protocol_version": protocol.Version,
		"read_only":        true,
		"integrations": map[string]string{
			"request_tracing": "not_connected",
			"packet_capture":  "not_connected",
			"stored_logs":     "not_connected",
		},
	})
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	alerts, err := s.store.Alerts(r.Context(), s.now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, alerts)
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.Servers(r.Context(), s.now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, servers)
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	var at *time.Time
	if text := r.URL.Query().Get("at"); text != "" {
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			http.Error(w, "at 형식이 잘못되었습니다", http.StatusBadRequest)
			return
		}
		at = &parsed
	}
	view, err := s.store.Topology(r.Context(), r.PathValue("id"), at, s.now())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, view)
}

func (s *Server) timeRange(r *http.Request) (time.Time, time.Time, error) {
	to := s.now()
	from := to.Add(-time.Hour)
	if text := r.URL.Query().Get("from"); text != "" {
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return from, to, err
		}
		from = parsed
	}
	if text := r.URL.Query().Get("to"); text != "" {
		parsed, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return from, to, err
		}
		to = parsed
	}
	if !from.Before(to) {
		return from, to, errors.New("from은 to보다 앞서야 합니다")
	}
	return from, to, nil
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.timeRange(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query := r.URL.Query()
	filter := EventFilter{
		From: from, To: to,
		Services:   query["service"],
		Categories: query["category"],
		ErrorsOnly: query.Get("errors") == "true",
	}
	filter.BeforeID, _ = strconv.ParseInt(query.Get("before"), 10, 64)
	filter.Limit, _ = strconv.Atoi(query.Get("limit"))
	events, err := s.store.Events(r.Context(), r.PathValue("id"), filter)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, events)
}

func (s *Server) handleGaps(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.timeRange(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	gaps, err := s.store.Gaps(r.Context(), r.PathValue("id"), from, to)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, gaps)
}

func (s *Server) handleSamples(w http.ResponseWriter, r *http.Request) {
	from, to, err := s.timeRange(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	query := r.URL.Query()
	scope := query.Get("scope")
	if scope == "" || len(query["name"]) == 0 {
		http.Error(w, "scope와 name이 필요합니다", http.StatusBadRequest)
		return
	}
	points, _ := strconv.Atoi(query.Get("points"))
	series, step, err := s.store.Samples(r.Context(), r.PathValue("id"), scope, query["name"], from, to, points)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, map[string]any{"step_ms": step, "series": series})
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "스트리밍을 지원하지 않습니다", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher.Flush()

	ch := s.hub.subscribe()
	defer s.hub.unsubscribe(ch)
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case server := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", server)
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
		}
		flusher.Flush()
	}
}

func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := filepath.Join(dir, filepath.Clean(r.URL.Path))
		if info, err := os.Stat(target); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		// 없는 에셋에 첫 화면을 돌려주면 브라우저가 HTML을 스크립트로 읽어 빈 화면이 된다.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		// 빌드하면 에셋 이름이 바뀌므로 첫 화면은 매번 확인하게 한다. 옛 화면이 남으면 새 기능이 보이지 않는다.
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFile(w, r, filepath.Join(dir, "index.html"))
	})
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("응답 실패: %v", err)
	}
}

func writeError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	log.Printf("요청 실패: %v", err)
	http.Error(w, "서버 오류", http.StatusInternalServerError)
}
