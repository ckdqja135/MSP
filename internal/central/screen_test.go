package central

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

var testJPEG = []byte{0xff, 0xd8, 0xff, 0xe0, 1, 2, 3, 0xff, 0xd9}

func postScreen(t *testing.T, url, token, contentType string, body []byte) (int, protocol.ScreenDemand) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-MSP-Server", "home-1")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var demand protocol.ScreenDemand
	if response.StatusCode == http.StatusOK {
		if err := json.NewDecoder(response.Body).Decode(&demand); err != nil {
			t.Fatal(err)
		}
	}
	return response.StatusCode, demand
}

func getScreen(t *testing.T, url string) ScreenView {
	t.Helper()
	response, err := http.Get(url + "/api/v1/servers/home-1/screen")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var view ScreenView
	if err := json.NewDecoder(response.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

func readyStatus() []byte {
	body, _ := json.Marshal(protocol.ScreenStatus{Session: protocol.ScreenSessionX11, State: protocol.ScreenReady})
	return body
}

func TestScreenReachesViewerOnlyOnDemand(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	ctx := context.Background()
	if err := store.Enroll(ctx, "home-1", "agent-token"); err != nil {
		t.Fatal(err)
	}
	if err := store.EnrollScreen(ctx, "home-1", "screen-token"); err != nil {
		t.Fatal(err)
	}
	ts := newTestServer(t, store, "")

	if view := getScreen(t, ts.URL); view.Helper != "offline" {
		t.Errorf("도우미가 없는데 연결됐다고 했다: %+v", view)
	}
	// 에이전트 토큰으로는 화면을 보낼 수 없다.
	if code, _ := postScreen(t, ts.URL+"/agent/v1/screen/poll", "agent-token", "application/json", readyStatus()); code != http.StatusUnauthorized {
		t.Errorf("에이전트 토큰: %d", code)
	}
	if code, _ := postScreen(t, ts.URL+"/agent/v1/screen/frame?width=4&height=2", "agent-token", "image/jpeg", testJPEG); code != http.StatusUnauthorized {
		t.Errorf("에이전트 토큰으로 화면 전송: %d", code)
	}

	// 도우미는 보는 사람이 생길 때까지 기다린다.
	polled := make(chan protocol.ScreenDemand, 1)
	go func() {
		_, demand := postScreen(t, ts.URL+"/agent/v1/screen/poll", "screen-token", "application/json", readyStatus())
		polled <- demand
	}()
	select {
	case demand := <-polled:
		t.Fatalf("보는 사람이 없는데 화면을 요청했다: %+v", demand)
	case <-time.After(150 * time.Millisecond):
	}

	deadline := time.Now().Add(3 * time.Second)
	for getScreen(t, ts.URL).Helper != "online" {
		if time.Now().After(deadline) {
			t.Fatal("기다리는 도우미를 연결된 것으로 보지 않았다")
		}
		time.Sleep(10 * time.Millisecond)
	}
	select {
	case demand := <-polled:
		if !demand.Capture || demand.IntervalMS <= 0 {
			t.Errorf("보는 사람이 생겼는데 화면을 요청하지 않았다: %+v", demand)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("보는 사람이 생겨도 도우미를 깨우지 않았다")
	}

	if code, _ := postScreen(t, ts.URL+"/agent/v1/screen/frame?width=4&height=2", "screen-token", "image/jpeg", []byte("<html>")); code != http.StatusBadRequest {
		t.Errorf("JPEG가 아닌 내용: %d", code)
	}
	if code, _ := postScreen(t, ts.URL+"/agent/v1/screen/frame?width=0&height=2", "screen-token", "image/jpeg", testJPEG); code != http.StatusBadRequest {
		t.Errorf("잘못된 크기: %d", code)
	}
	code, demand := postScreen(t, ts.URL+"/agent/v1/screen/frame?width=4&height=2", "screen-token", "image/jpeg", testJPEG)
	if code != http.StatusOK || !demand.Capture {
		t.Fatalf("화면 전송: %d %+v", code, demand)
	}
	view := getScreen(t, ts.URL)
	if view.Seq != 1 || view.Width != 4 || view.Height != 2 || view.CapturedAt == nil || view.Session != protocol.ScreenSessionX11 {
		t.Errorf("view = %+v", view)
	}
	// 같은 화면이면 그림은 그대로 두고 번호도 올리지 않는다.
	if code, _ := postScreen(t, ts.URL+"/agent/v1/screen/frame?unchanged=1", "screen-token", "application/octet-stream", nil); code != http.StatusOK {
		t.Errorf("같은 화면 알림: %d", code)
	}
	if view := getScreen(t, ts.URL); view.Seq != 1 {
		t.Errorf("같은 화면인데 번호가 올랐다: %d", view.Seq)
	}

	response, err := http.Get(ts.URL + "/api/v1/servers/home-1/screen/frame")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.Header.Get("Content-Type") != "image/jpeg" || !bytes.Equal(body, testJPEG) {
		t.Errorf("화면 그림 = %s %v", response.Header.Get("Content-Type"), body)
	}
	if response.Header.Get("Cache-Control") != "no-store" {
		t.Error("화면 그림을 브라우저가 저장할 수 있다")
	}
}

func TestDemoServerRejectsScreenHelper(t *testing.T) {
	store := openStore(t, protocol.ModeDemo)
	ts := newTestServer(t, store, "")
	if code, _ := postScreen(t, ts.URL+"/agent/v1/screen/poll", "any", "application/json", readyStatus()); code != http.StatusForbidden {
		t.Errorf("데모 서버가 화면 도우미를 거부하지 않았다: %d", code)
	}
}

type relayClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *relayClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *relayClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func stoppedContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func TestScreenIsNotKeptAfterViewerLeaves(t *testing.T) {
	clock := &relayClock{t: baseTime}
	relay := NewScreenRelay()
	relay.now = clock.now
	ready := protocol.ScreenStatus{Session: protocol.ScreenSessionX11, State: protocol.ScreenReady}

	relay.wait(stoppedContext(), "home-1", ready)
	if view := relay.watch("home-1", ""); view.Helper != "online" {
		t.Fatalf("view = %+v", view)
	}
	if demand := relay.push("home-1", testJPEG, 4, 2); !demand.Capture {
		t.Fatal("보는 사람이 있는데 화면을 그만 받겠다고 했다")
	}

	// 화면 보기 창이 닫히면 조회가 끊긴다. 그 뒤로는 받아 둔 화면도 내주지 않는다.
	clock.advance(screenWantedFor + time.Second)
	if frame, _ := relay.image("home-1"); frame != nil {
		t.Error("보는 사람이 없는데 화면을 내줬다")
	}
	if demand := relay.push("home-1", append([]byte{}, testJPEG...), 8, 4); demand.Capture {
		t.Error("보는 사람이 없는데 화면을 계속 요청했다")
	}
	if relay.servers["home-1"].frame != nil {
		t.Error("보는 사람이 없는데 화면을 메모리에 남겼다")
	}
	relay.wait(stoppedContext(), "home-1", ready)

	// 도우미가 끊기면 남은 화면을 지금 화면처럼 보여주지 않는다.
	relay.watch("home-1", "")
	relay.push("home-1", testJPEG, 4, 2)
	clock.advance(screenOnlineWithin + time.Second)
	if view := relay.watch("home-1", ""); view.Helper != "offline" || view.Seq != 0 {
		t.Errorf("끊긴 도우미 = %+v", view)
	}
	if frame, _ := relay.image("home-1"); frame != nil {
		t.Error("끊긴 도우미의 화면을 남겼다")
	}
	if view := relay.watch("never-seen", ""); view.Helper != "offline" {
		t.Errorf("모르는 서버 = %+v", view)
	}
	if len(relay.servers) != 1 {
		t.Error("화면 조회만으로 서버 기록을 만들었다")
	}
}

func TestHelperThatCannotCaptureIsNotWoken(t *testing.T) {
	relay := NewScreenRelay()
	status := cleanScreenStatus(protocol.ScreenStatus{Session: "wayland", State: protocol.ScreenUnsupported, Detail: strings.Repeat("가", maxScreenDetail+10)})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan protocol.ScreenDemand, 1)
	go func() { returned <- relay.wait(ctx, "home-1", status) }()

	deadline := time.Now().Add(3 * time.Second)
	var view ScreenView
	for view.Helper != "online" {
		if time.Now().After(deadline) {
			t.Fatal("도우미가 연결되지 않았다")
		}
		time.Sleep(5 * time.Millisecond)
		view = relay.watch("home-1", "")
	}
	if view.State != protocol.ScreenUnsupported || view.Session != "wayland" || len([]rune(view.Detail)) != maxScreenDetail {
		t.Errorf("가져올 수 없는 이유를 전하지 않았다: %+v", view)
	}
	select {
	case demand := <-returned:
		t.Fatalf("화면을 가져올 수 없는 도우미를 깨웠다: %+v", demand)
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	if demand := <-returned; demand.Capture {
		t.Error("화면을 가져올 수 없는 도우미에게 화면을 요청했다")
	}
	if got := cleanScreenStatus(protocol.ScreenStatus{Session: "vnc", State: "weird"}); got.Session != "" || got.State != protocol.ScreenError {
		t.Errorf("모르는 값을 그대로 받았다: %+v", got)
	}
}

func TestConsoleIsReadThroughAgentTask(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	if err := store.Enroll(context.Background(), "home-1", "agent-token"); err != nil {
		t.Fatal(err)
	}
	ts := newTestServer(t, store, "")
	readConsole := func(query string) (int, protocol.ConsoleScreen) {
		response, err := http.Get(ts.URL + "/api/v1/servers/home-1/console" + query)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var screen protocol.ConsoleScreen
		json.NewDecoder(response.Body).Decode(&screen)
		return response.StatusCode, screen
	}

	if _, screen := readConsole(""); !strings.Contains(screen.Error, "연결되어 있지 않아") {
		t.Errorf("에이전트가 없는데 이유를 알리지 않았다: %+v", screen)
	}
	if code, _ := readConsole("?tty=99"); code != http.StatusBadRequest {
		t.Errorf("잘못된 tty: %d", code)
	}

	agent := func(method, path string, body []byte) *http.Response {
		request, _ := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
		request.Header.Set("X-MSP-Server", "home-1")
		request.Header.Set("Authorization", "Bearer agent-token")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Error(err)
			return nil
		}
		return response
	}
	go func() {
		response := agent(http.MethodGet, "/agent/v1/tasks", nil)
		if response == nil {
			return
		}
		defer response.Body.Close()
		var task protocol.Task
		json.NewDecoder(response.Body).Decode(&task)
		if task.Kind != protocol.TaskConsole || task.Console == nil || task.Console.TTY != 3 {
			t.Errorf("task = %+v", task)
			return
		}
		body, _ := json.Marshal(protocol.ConsoleScreen{TTY: 3, Active: 2, Rows: 1, Cols: 80, Lines: []string{"login:"}})
		if reply := agent(http.MethodPost, "/agent/v1/tasks/"+task.ID+"/console", body); reply != nil {
			reply.Body.Close()
		}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		_, screen := readConsole("?tty=3")
		if screen.Error == "" {
			if screen.TTY != 3 || screen.Active != 2 || len(screen.Lines) != 1 || screen.Lines[0] != "login:" {
				t.Errorf("screen = %+v", screen)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("콘솔 화면을 받지 못했다: %s", screen.Error)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func controlStatus() protocol.ScreenStatus {
	return protocol.ScreenStatus{Session: protocol.ScreenSessionX11, State: protocol.ScreenReady, Control: true}
}

func TestInputPassesOnlyWhileControlIsOn(t *testing.T) {
	clock := &relayClock{t: baseTime}
	relay := NewScreenRelay()
	relay.now = clock.now
	click := []protocol.ScreenInput{{Type: protocol.InputDown, X: 0.5, Y: 0.5, Button: 1}}

	// 조작을 허용하지 않은 서버에는 보는 사람이 조작을 켜도 입력이 가지 않는다.
	relay.wait(stoppedContext(), "home-1", protocol.ScreenStatus{Session: protocol.ScreenSessionX11, State: protocol.ScreenReady})
	if view := relay.watch("home-1", "1"); view.Control || view.ControlLive {
		t.Errorf("허용하지 않은 서버를 조작할 수 있다고 했다: %+v", view)
	}
	if err := relay.input("home-1", click); err != ErrControlOff {
		t.Errorf("허용하지 않은 서버: %v", err)
	}
	if _, _, ok := relay.openInput("home-1"); ok {
		t.Error("허용하지 않은 서버에 입력 통로를 열었다")
	}

	// 허용한 서버라도 보는 사람이 조작을 켜기 전에는 가지 않는다.
	relay.wait(stoppedContext(), "home-1", controlStatus())
	if view := relay.watch("home-1", ""); !view.Control || view.ControlLive {
		t.Errorf("view = %+v", view)
	}
	if demand := relay.push("home-1", testJPEG, 4, 2); demand.Control {
		t.Error("조작을 켜지 않았는데 도우미에게 입력을 받으라고 했다")
	}
	if err := relay.input("home-1", click); err != ErrControlNotReady {
		t.Errorf("조작을 켜기 전: %v", err)
	}

	relay.watch("home-1", "1")
	demand := relay.push("home-1", testJPEG, 4, 2)
	if !demand.Control || demand.IntervalMS >= int(screenInterval/time.Millisecond) {
		t.Errorf("조작 중에는 입력을 받고 화면을 더 자주 보내야 한다: %+v", demand)
	}
	// 받아 갈 도우미가 붙기 전의 입력은 쌓아 두지 않는다.
	if err := relay.input("home-1", click); err != ErrControlNotReady {
		t.Errorf("도우미가 붙기 전: %v", err)
	}
	inputs, release, ok := relay.openInput("home-1")
	if !ok {
		t.Fatal("조작 중인데 입력 통로를 열지 못했다")
	}
	if view := relay.watch("home-1", "1"); !view.ControlLive {
		t.Errorf("입력이 전달되는 상태를 알리지 않았다: %+v", view)
	}
	if err := relay.input("home-1", click); err != nil {
		t.Fatal(err)
	}
	if got := <-inputs; got != click[0] {
		t.Errorf("input = %+v", got)
	}

	// 조작을 끄면 바로 막힌다.
	relay.watch("home-1", "0")
	if err := relay.input("home-1", click); err != ErrControlNotReady {
		t.Errorf("조작을 끈 뒤: %v", err)
	}
	if relay.controlling("home-1") {
		t.Error("조작을 껐는데 켜져 있다고 했다")
	}

	// 조작을 켠 창이 닫혀 조회가 끊겨도 꺼진다.
	relay.watch("home-1", "1")
	clock.advance(screenWantedFor + time.Second)
	if relay.controlling("home-1") {
		t.Error("보는 사람이 없는데 조작이 켜져 있다")
	}
	release()
	if relay.servers["home-1"].streams != 0 {
		t.Error("닫힌 입력 통로를 세고 있다")
	}
}

func postInput(t *testing.T, url string, headers map[string]string, body string) int {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, url+"/api/v1/servers/home-1/screen/input", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	return response.StatusCode
}

func TestInputTravelsFromViewerToHelper(t *testing.T) {
	store := openStore(t, protocol.ModeLive)
	if err := store.EnrollScreen(context.Background(), "home-1", "screen-token"); err != nil {
		t.Fatal(err)
	}
	ts := newTestServer(t, store, "")
	jsonHeader := map[string]string{"Content-Type": "application/json"}
	click := `{"inputs":[{"type":"down","x":0.25,"y":0.5,"button":1}]}`

	if code := postInput(t, ts.URL, jsonHeader, click); code != http.StatusForbidden {
		t.Errorf("도우미가 없는 서버: %d", code)
	}

	status, _ := json.Marshal(controlStatus())
	go func() {
		request, _ := http.NewRequest(http.MethodPost, ts.URL+"/agent/v1/screen/poll", bytes.NewReader(status))
		request.Header.Set("X-MSP-Server", "home-1")
		request.Header.Set("Authorization", "Bearer screen-token")
		if response, err := http.DefaultClient.Do(request); err == nil {
			response.Body.Close()
		}
	}()
	watch := func(control string) ScreenView {
		response, err := http.Get(ts.URL + "/api/v1/servers/home-1/screen?control=" + control)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var view ScreenView
		json.NewDecoder(response.Body).Decode(&view)
		return view
	}
	deadline := time.Now().Add(3 * time.Second)
	for !watch("1").Control {
		if time.Now().After(deadline) {
			t.Fatal("조작을 허용한 도우미를 알아보지 못했다")
		}
		time.Sleep(10 * time.Millisecond)
	}

	request, _ := http.NewRequest(http.MethodGet, ts.URL+"/agent/v1/screen/input", nil)
	request.Header.Set("X-MSP-Server", "home-1")
	request.Header.Set("Authorization", "Bearer screen-token")
	stream, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != http.StatusOK {
		t.Fatalf("입력 통로: %d", stream.StatusCode)
	}
	for !watch("1").ControlLive {
		if time.Now().After(deadline) {
			t.Fatal("입력 통로가 열린 것을 알리지 않았다")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// 다른 사이트가 브라우저를 시켜 보낸 요청은 받지 않는다.
	refused := []map[string]string{
		{"Content-Type": "text/plain"},
		{"Content-Type": "application/json", "Origin": "http://evil.example.test"},
		{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"},
	}
	for _, headers := range refused {
		if code := postInput(t, ts.URL, headers, click); code != http.StatusForbidden {
			t.Errorf("%v: %d", headers, code)
		}
	}
	if code := postInput(t, ts.URL, jsonHeader, `{"inputs":[{"type":"down","x":3,"y":0.5,"button":1}]}`); code != http.StatusBadRequest {
		t.Errorf("화면 밖 위치: %d", code)
	}
	if code := postInput(t, ts.URL, jsonHeader, `{"inputs":[{"type":"exec","code":"rm"}]}`); code != http.StatusBadRequest {
		t.Errorf("모르는 입력: %d", code)
	}
	same := map[string]string{"Content-Type": "application/json", "Origin": ts.URL, "Sec-Fetch-Site": "same-origin"}
	if code := postInput(t, ts.URL, same, click); code != http.StatusNoContent {
		t.Fatalf("이 화면에서 보낸 입력: %d", code)
	}

	lines := bufio.NewScanner(stream.Body)
	var got protocol.ScreenInput
	for lines.Scan() {
		if len(lines.Bytes()) == 0 {
			continue
		}
		if err := json.Unmarshal(lines.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		break
	}
	if got.Type != protocol.InputDown || got.X != 0.25 || got.Y != 0.5 || got.Button != 1 {
		t.Errorf("도우미가 받은 입력 = %+v", got)
	}

	// 조작을 끄면 통로가 닫힌다. 도우미는 이때 눌린 키를 뗀다.
	watch("0")
	closed := make(chan struct{})
	go func() {
		for lines.Scan() {
		}
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("조작을 껐는데 입력 통로가 열려 있다")
	}
	if code := postInput(t, ts.URL, jsonHeader, click); code != http.StatusConflict {
		t.Errorf("조작을 끈 뒤: %d", code)
	}
}
