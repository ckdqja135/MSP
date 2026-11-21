package central

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	screenInterval = time.Second
	// 조작하는 동안에는 화면을 더 자주 받는다. 누른 결과가 바로 보여야 하기 때문이다.
	screenControlInterval = 250 * time.Millisecond
	// 화면을 보는 쪽은 이 시간 안에 다시 조회해야 한다. 끊기면 도우미가 화면 읽기를 멈춘다.
	screenWantedFor = 5 * time.Second
	// 도우미는 화면을 읽는 동안 매 간격마다 연락한다. 이보다 오래 조용하면 끊긴 것으로 본다.
	screenOnlineWithin = 10 * time.Second
	maxScreenFrame     = 4 << 20
	maxScreenSide      = 16384
	maxScreenDetail    = 500
	// 한 번에 받는 입력 수와 도우미로 넘기기 전에 쌓아 두는 입력 수의 상한이다.
	maxInputBatch = 256
	inputBacklog  = 1024
)

var (
	ErrControlOff      = errors.New("이 서버는 원격 조작을 허용하지 않습니다")
	ErrControlNotReady = errors.New("조작 연결을 준비하고 있습니다")
)

type screenServer struct {
	lastContact time.Time
	// polling은 지금 대기 중인 도우미 연결 수다.
	polling     int
	status      protocol.ScreenStatus
	wantedUntil time.Time
	wake        chan struct{}
	frame       []byte
	seq         uint64
	capturedAt  time.Time
	width       int
	height      int
	// controlUntil까지 보는 사람이 조작을 켜 둔 것으로 본다. streams는 입력을 받아 가는 도우미 연결 수다.
	controlUntil time.Time
	inputs       chan protocol.ScreenInput
	streams      int
}

// ScreenRelay는 서버 화면을 메모리에만 둔다. 디스크와 DB에 남기지 않고, 보는 사람이 없어지면 바로 지운다.
type ScreenRelay struct {
	mu      sync.Mutex
	servers map[string]*screenServer
	now     func() time.Time
}

func NewScreenRelay() *ScreenRelay {
	return &ScreenRelay{servers: map[string]*screenServer{}, now: time.Now}
}

// ScreenView는 화면 보기 창이 받는 상태다. 화면 그림은 따로 받는다.
type ScreenView struct {
	// Helper는 화면 도우미의 연결 상태다. online 또는 offline이다.
	Helper     string               `json:"helper"`
	Session    string               `json:"session"`
	State      protocol.ScreenState `json:"state"`
	Detail     string               `json:"detail"`
	Seq        uint64               `json:"seq"`
	CapturedAt *time.Time           `json:"captured_at"`
	Width      int                  `json:"width"`
	Height     int                  `json:"height"`
	// Control은 이 서버가 원격 조작을 허용하는지, ControlLive는 지금 입력이 서버로 전달되는지다.
	Control     bool `json:"control"`
	ControlLive bool `json:"control_live"`
}

func (s *screenServer) online(now time.Time) bool {
	return s.polling > 0 || now.Sub(s.lastContact) <= screenOnlineWithin
}

func (s *screenServer) dropFrame() {
	s.frame = nil
	s.width, s.height = 0, 0
	s.capturedAt = time.Time{}
}

// controlling은 도우미가 조작을 허용했고 보는 사람이 조작을 켠 상태인지 본다.
func (s *screenServer) controlling(now time.Time) bool {
	return s.status.Control && now.Before(s.wantedUntil) && now.Before(s.controlUntil)
}

func (s *screenServer) demand(now time.Time) protocol.ScreenDemand {
	if !now.Before(s.wantedUntil) {
		return protocol.ScreenDemand{}
	}
	if s.controlling(now) {
		return protocol.ScreenDemand{Capture: true, IntervalMS: int(screenControlInterval / time.Millisecond), Control: true}
	}
	return protocol.ScreenDemand{Capture: true, IntervalMS: int(screenInterval / time.Millisecond)}
}

// watch는 화면을 보는 사람이 있다고 표시하고 지금 상태를 돌려준다.
// control이 "1"이면 보는 사람이 조작을 켜 둔 것이고 "0"이면 방금 끈 것이다. 비어 있으면 조작 상태를 건드리지 않는다.
// 도우미가 한 번도 연결하지 않은 서버는 기록을 만들지 않는다.
func (r *ScreenRelay) watch(serverID string, control string) ScreenView {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[serverID]
	now := r.now()
	if s == nil || !s.online(now) {
		if s != nil {
			s.dropFrame()
		}
		return ScreenView{Helper: "offline"}
	}
	s.wantedUntil = now.Add(screenWantedFor)
	// 조작을 끄면 기다리지 않고 바로 끊는다. 켠 창이 닫혀 조회가 끊겨도 곧 꺼진다.
	switch control {
	case "1":
		// 허용하지 않은 서버에 켜 달라는 요청은 남겨 두지 않는다. 나중에 허용으로 바뀌어도 저절로 켜지지 않게 한다.
		if s.status.Control {
			s.controlUntil = s.wantedUntil
		}
	case "0":
		s.controlUntil = time.Time{}
	}
	if s.wake != nil {
		close(s.wake)
		s.wake = nil
	}
	view := ScreenView{Helper: "online", Session: s.status.Session, State: s.status.State, Detail: s.status.Detail,
		Control: s.status.Control, ControlLive: s.controlling(now) && s.streams > 0}
	if s.frame != nil {
		at := s.capturedAt
		view.Seq, view.CapturedAt, view.Width, view.Height = s.seq, &at, s.width, s.height
	}
	return view
}

// wait는 화면을 보는 사람이 생길 때까지 도우미를 기다리게 한다.
// 화면을 가져올 수 없는 도우미는 깨우지 않는다. 깨워도 할 일이 없어 다시 묻기만 반복하기 때문이다.
func (r *ScreenRelay) wait(ctx context.Context, serverID string, status protocol.ScreenStatus) protocol.ScreenDemand {
	r.mu.Lock()
	s := r.servers[serverID]
	if s == nil {
		s = &screenServer{}
		r.servers[serverID] = s
	}
	now := r.now()
	s.lastContact = now
	s.status = status
	if !now.Before(s.wantedUntil) {
		s.dropFrame()
	}
	var wake chan struct{}
	if status.State == protocol.ScreenReady {
		if demand := s.demand(now); demand.Capture {
			r.mu.Unlock()
			return demand
		}
		if s.wake == nil {
			s.wake = make(chan struct{})
		}
		wake = s.wake
	}
	s.polling++
	r.mu.Unlock()

	timer := time.NewTimer(taskWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	case <-wake:
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	s.polling--
	now = r.now()
	s.lastContact = now
	if wake == nil {
		return protocol.ScreenDemand{}
	}
	return s.demand(now)
}

// push는 새 화면을 받는다. frame이 nil이면 직전 화면과 같다는 뜻이다.
func (r *ScreenRelay) push(serverID string, frame []byte, width, height int) protocol.ScreenDemand {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[serverID]
	if s == nil {
		s = &screenServer{}
		r.servers[serverID] = s
	}
	now := r.now()
	s.lastContact = now
	demand := s.demand(now)
	// 보는 사람이 없으면 받은 화면도, 앞서 받은 화면도 두지 않는다.
	if !demand.Capture {
		s.dropFrame()
		return demand
	}
	if frame != nil {
		s.frame, s.width, s.height = frame, width, height
		s.seq++
	}
	if s.frame != nil {
		s.capturedAt = now
	}
	return demand
}

func (r *ScreenRelay) image(serverID string) ([]byte, uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[serverID]
	if s == nil || s.frame == nil || !r.now().Before(s.wantedUntil) {
		return nil, 0
	}
	return s.frame, s.seq
}

// openInput은 도우미가 입력을 받아 갈 통로를 연다. 조작 중이 아니면 열지 않는다.
// 앞서 쌓인 입력은 버린다. 한참 전에 누른 것이 뒤늦게 들어가면 안 되기 때문이다.
func (r *ScreenRelay) openInput(serverID string) (<-chan protocol.ScreenInput, func(), bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[serverID]
	if s == nil || !s.controlling(r.now()) {
		return nil, nil, false
	}
	if s.inputs == nil {
		s.inputs = make(chan protocol.ScreenInput, inputBacklog)
	}
	for len(s.inputs) > 0 {
		<-s.inputs
	}
	s.streams++
	return s.inputs, func() {
		r.mu.Lock()
		s.streams--
		r.mu.Unlock()
	}, true
}

func (r *ScreenRelay) controlling(serverID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[serverID]
	return s != nil && s.controlling(r.now())
}

// input은 화면 보기 창의 입력을 도우미로 넘긴다. 받아 갈 도우미가 없으면 쌓지 않고 거절한다.
func (r *ScreenRelay) input(serverID string, inputs []protocol.ScreenInput) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[serverID]
	now := r.now()
	if s == nil || !s.online(now) || !s.status.Control {
		return ErrControlOff
	}
	if !s.controlling(now) || s.streams == 0 || len(s.inputs)+len(inputs) > cap(s.inputs) {
		return ErrControlNotReady
	}
	for _, input := range inputs {
		s.inputs <- input
	}
	return nil
}

func cleanScreenStatus(status protocol.ScreenStatus) protocol.ScreenStatus {
	switch status.Session {
	case protocol.ScreenSessionX11, protocol.ScreenSessionWayland:
	default:
		status.Session = ""
	}
	switch status.State {
	case protocol.ScreenReady, protocol.ScreenUnsupported:
	default:
		status.State = protocol.ScreenError
	}
	if detail := []rune(status.Detail); len(detail) > maxScreenDetail {
		status.Detail = string(detail[:maxScreenDetail])
	}
	status.Control = status.Control && status.State == protocol.ScreenReady
	return status
}

func (s *Server) handleScreenPoll(w http.ResponseWriter, r *http.Request) {
	serverID, ok := s.authenticateScreen(w, r)
	if !ok {
		return
	}
	var status protocol.ScreenStatus
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&status); err != nil {
		http.Error(w, "잘못된 요청입니다", http.StatusBadRequest)
		return
	}
	writeJSON(w, s.screens.wait(r.Context(), serverID, cleanScreenStatus(status)))
}

func (s *Server) handleScreenFrame(w http.ResponseWriter, r *http.Request) {
	serverID, ok := s.authenticateScreen(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	if query.Get("unchanged") == "1" {
		writeJSON(w, s.screens.push(serverID, nil, 0, 0))
		return
	}
	width, _ := strconv.Atoi(query.Get("width"))
	height, _ := strconv.Atoi(query.Get("height"))
	if width < 1 || height < 1 || width > maxScreenSide || height > maxScreenSide {
		http.Error(w, "화면 크기가 잘못되었습니다", http.StatusBadRequest)
		return
	}
	frame, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxScreenFrame))
	if err != nil {
		http.Error(w, "화면이 너무 큽니다", http.StatusRequestEntityTooLarge)
		return
	}
	// JPEG가 아닌 내용을 그림으로 내보내지 않는다.
	if len(frame) < 4 || frame[0] != 0xff || frame[1] != 0xd8 {
		http.Error(w, "JPEG가 아닙니다", http.StatusBadRequest)
		return
	}
	writeJSON(w, s.screens.push(serverID, frame, width, height))
}

func (s *Server) handleScreen(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.screens.watch(r.PathValue("id"), r.URL.Query().Get("control")))
}

// handleScreenInputStream은 도우미에게 입력을 한 줄씩 흘려 준다. 조작이 꺼지면 연결을 닫는다.
func (s *Server) handleScreenInputStream(w http.ResponseWriter, r *http.Request) {
	serverID, ok := s.authenticateScreen(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "스트리밍을 지원하지 않습니다", http.StatusInternalServerError)
		return
	}
	inputs, release, ok := s.screens.openInput(serverID)
	if !ok {
		http.Error(w, "조작 중이 아닙니다", http.StatusConflict)
		return
	}
	defer release()
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	flusher.Flush()

	encoder := json.NewEncoder(w)
	check := time.NewTicker(time.Second)
	defer check.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case input := <-inputs:
			if encoder.Encode(input) != nil {
				return
			}
			// 함께 온 입력은 한 번에 내보낸다.
			for more := true; more; {
				select {
				case input = <-inputs:
					if encoder.Encode(input) != nil {
						return
					}
				default:
					more = false
				}
			}
		case <-check.C:
			if !s.screens.controlling(serverID) {
				return
			}
			// 빈 줄로 연결이 살아 있음을 알린다.
			if _, err := io.WriteString(w, "\n"); err != nil {
				return
			}
		}
		flusher.Flush()
	}
}

// sameOrigin은 이 화면에서 보낸 요청인지 본다. 다른 사이트가 브라우저를 시켜 입력을 보내지 못하게 한다.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && parsed.Host == r.Host
}

// handleScreenInput은 화면 보기 창의 마우스와 키보드 입력을 받는다. 대시보드에서 서버를 바꾸는 유일한 길이다.
// 도우미 설정에서 조작을 허용한 서버에만, 보는 사람이 조작을 켠 동안에만 전달한다.
func (s *Server) handleScreenInput(w http.ResponseWriter, r *http.Request) {
	// JSON이 아닌 요청과 다른 사이트에서 온 요청은 받지 않는다. 브라우저가 다른 사이트의 JSON 요청은 먼저 막는다.
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if mediaType != "application/json" || !sameOrigin(r) {
		http.Error(w, "이 화면에서 보낸 요청만 받습니다", http.StatusForbidden)
		return
	}
	var body struct {
		Inputs []protocol.ScreenInput `json:"inputs"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || len(body.Inputs) > maxInputBatch {
		http.Error(w, "잘못된 요청입니다", http.StatusBadRequest)
		return
	}
	for _, input := range body.Inputs {
		if !input.Valid() {
			http.Error(w, "잘못된 입력입니다", http.StatusBadRequest)
			return
		}
	}
	switch err := s.screens.input(r.PathValue("id"), body.Inputs); err {
	case nil:
		w.WriteHeader(http.StatusNoContent)
	case ErrControlOff:
		http.Error(w, err.Error(), http.StatusForbidden)
	default:
		http.Error(w, err.Error(), http.StatusConflict)
	}
}

func (s *Server) handleScreenImage(w http.ResponseWriter, r *http.Request) {
	frame, seq := s.screens.image(r.PathValue("id"))
	if frame == nil {
		http.Error(w, "받은 화면이 없습니다", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-MSP-Screen-Seq", strconv.FormatUint(seq, 10))
	w.Write(frame)
}
