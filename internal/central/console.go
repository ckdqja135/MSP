package central

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	consoleReplyWait = 10 * time.Second
	maxConsoleTTY    = 63
	maxConsoleLines  = 512
)

var (
	ErrConsoleOffline = errors.New("에이전트가 연결되어 있지 않아 콘솔을 읽을 수 없습니다")
	ErrConsoleBusy    = errors.New("에이전트가 다른 조회를 처리하고 있습니다. 잠시 뒤 다시 읽습니다")
	ErrConsoleSilent  = errors.New("에이전트가 응답하지 않았습니다. 콘솔 보기를 지원하지 않는 버전일 수 있습니다")
)

type consoleSession struct {
	serverID string
	reply    chan protocol.ConsoleScreen
}

// Console은 에이전트에 콘솔 화면을 한 번 읽어 달라고 맡긴다.
func (r *LogRelay) Console(ctx context.Context, serverID string, request protocol.ConsoleRequest) (protocol.ConsoleScreen, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return protocol.ConsoleScreen{}, err
	}
	id := hex.EncodeToString(raw)
	session := &consoleSession{serverID: serverID, reply: make(chan protocol.ConsoleScreen, 1)}

	r.mu.Lock()
	if time.Since(r.lastPoll[serverID]) > agentOnlineWithin {
		r.mu.Unlock()
		return protocol.ConsoleScreen{}, ErrConsoleOffline
	}
	r.consoles[id] = session
	queue := r.queue(serverID)
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.consoles, id)
		r.mu.Unlock()
	}()

	select {
	case queue <- protocol.Task{ID: id, Kind: protocol.TaskConsole, Console: &request}:
	default:
		return protocol.ConsoleScreen{}, ErrConsoleBusy
	}
	timeout := time.NewTimer(consoleReplyWait)
	defer timeout.Stop()
	select {
	case <-ctx.Done():
		return protocol.ConsoleScreen{}, ctx.Err()
	case <-timeout.C:
		return protocol.ConsoleScreen{}, ErrConsoleSilent
	case screen := <-session.reply:
		return screen, nil
	}
}

func (s *Server) handleConsoleReply(w http.ResponseWriter, r *http.Request) {
	serverID, ok := s.authenticateAgent(w, r)
	if !ok {
		return
	}
	var screen protocol.ConsoleScreen
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&screen); err != nil {
		http.Error(w, "잘못된 요청입니다", http.StatusBadRequest)
		return
	}
	if len(screen.Lines) > maxConsoleLines {
		screen.Lines = screen.Lines[:maxConsoleLines]
	}
	s.logs.mu.Lock()
	session := s.logs.consoles[r.PathValue("id")]
	s.logs.mu.Unlock()
	// 화면이 떠났거나 다른 서버의 작업이면 버린다.
	if session != nil && session.serverID == serverID {
		select {
		case session.reply <- screen:
		default:
		}
	}
	writeJSON(w, protocol.ChunkAck{})
}

func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	tty := 0
	if text := r.URL.Query().Get("tty"); text != "" {
		parsed, err := strconv.Atoi(text)
		if err != nil || parsed < 0 || parsed > maxConsoleTTY {
			http.Error(w, "tty 번호가 잘못되었습니다", http.StatusBadRequest)
			return
		}
		tty = parsed
	}
	screen, err := s.logs.Console(r.Context(), r.PathValue("id"), protocol.ConsoleRequest{TTY: tty})
	if err != nil {
		// 읽지 못한 이유를 화면에 그대로 보여준다.
		screen = protocol.ConsoleScreen{TTY: tty, Error: err.Error()}
	}
	writeJSON(w, screen)
}
