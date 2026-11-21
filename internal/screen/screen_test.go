package screen

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

func xauthRecord(family uint16, fields ...string) []byte {
	out := binary.BigEndian.AppendUint16(nil, family)
	for _, field := range fields {
		out = binary.BigEndian.AppendUint16(out, uint16(len(field)))
		out = append(out, field...)
	}
	return out
}

func TestCookieIsPickedForThisDisplay(t *testing.T) {
	body := xauthRecord(xauthFamilyLocal, "other-host", "0", xauthCookieName, "OTHER")
	body = append(body, xauthRecord(xauthFamilyLocal, "home", "1", xauthCookieName, "DISPLAY1")...)
	body = append(body, xauthRecord(xauthFamilyLocal, "home", "0", "XDM-AUTHORIZATION-1", "XDM")...)
	body = append(body, xauthRecord(xauthFamilyLocal, "home", "0", xauthCookieName, "MINE")...)
	entries := parseXauthority(append(body, 0x01))
	if len(entries) != 4 {
		t.Fatalf("entries = %d", len(entries))
	}
	if got := string(pickCookie(entries, "home", 0)); got != "MINE" {
		t.Errorf("호스트와 디스플레이가 맞는 쿠키를 고르지 않았다: %q", got)
	}
	if got := string(pickCookie(entries, "renamed", 0)); got != "OTHER" {
		t.Errorf("호스트 이름이 달라도 디스플레이가 맞는 쿠키를 써야 한다: %q", got)
	}
	if got := pickCookie(entries, "home", 2); got != nil {
		t.Errorf("다른 디스플레이의 쿠키를 골랐다: %q", got)
	}
	wild := parseXauthority(xauthRecord(xauthFamilyWild, "", "", xauthCookieName, "WILD"))
	if got := string(pickCookie(wild, "home", 3)); got != "WILD" {
		t.Errorf("모든 디스플레이용 쿠키를 쓰지 않았다: %q", got)
	}
}

func TestParseDisplay(t *testing.T) {
	cases := []struct {
		value           string
		display, screen int
		ok              bool
	}{
		{":0", 0, 0, true},
		{":1.0", 1, 0, true},
		{"unix:2.1", 2, 1, true},
		{"otherhost:0", 0, 0, false},
		{"", 0, 0, false},
		{":x", 0, 0, false},
	}
	for _, c := range cases {
		display, screen, ok := parseDisplay(c.value)
		if display != c.display || screen != c.screen || ok != c.ok {
			t.Errorf("parseDisplay(%q) = %d, %d, %v", c.value, display, screen, ok)
		}
	}
}

func TestSessionDetection(t *testing.T) {
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	// Wayland 세션에도 DISPLAY가 있다. X11로 잘못 보면 검은 화면만 읽는다.
	if got := DetectSession(env(map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"})); got != protocol.ScreenSessionWayland {
		t.Errorf("Wayland 세션 = %q", got)
	}
	if got := DetectSession(env(map[string]string{"XDG_SESSION_TYPE": "x11", "DISPLAY": ":1"})); got != protocol.ScreenSessionX11 {
		t.Errorf("X11 세션 = %q", got)
	}
	wayland := NewCapturer(env(map[string]string{"XDG_SESSION_TYPE": "wayland"}))
	if status := wayland.Status(); status.State != protocol.ScreenUnsupported || status.Detail == "" {
		t.Errorf("Wayland 상태 = %+v", status)
	}
	if _, err := wayland.Capture(); err == nil {
		t.Error("Wayland 세션에서 화면을 읽었다고 했다")
	}
	if status := NewCapturer(env(nil)).Status(); status.State != protocol.ScreenError {
		t.Errorf("세션 없음 = %+v", status)
	}
}

const (
	testXTest  = 140
	testRoot   = 0x123
	testVisual = 0x21
	testCookie = "0123456789abcdef"
)

// fakeX는 X 서버의 접속 응답과 화면 읽기 응답만 흉내 낸다. 받은 요청 번호를 기록한다.
type fakeX struct {
	mu      sync.Mutex
	opcodes []byte
	// fakes는 받은 입력 요청이다. 종류, 키나 버튼 번호, 위치 순서다.
	fakes   [][4]int
	noXTest bool
	cookie  string
	width   int
	height  int
	pixels  []byte
}

func (f *fakeX) setup() []byte {
	body := make([]byte, 32)
	binary.LittleEndian.PutUint16(body[16:], 4)
	body[20], body[21] = 1, 1
	body = append(body, "test"...)
	body = append(body, 24, 32, 32, 0, 0, 0, 0, 0)
	screen := make([]byte, 40)
	binary.LittleEndian.PutUint32(screen, testRoot)
	binary.LittleEndian.PutUint32(screen[32:], testVisual)
	screen[38], screen[39] = 24, 1
	depth := []byte{24, 0, 1, 0, 0, 0, 0, 0}
	visual := make([]byte, 24)
	binary.LittleEndian.PutUint32(visual, testVisual)
	binary.LittleEndian.PutUint32(visual[8:], 0xff0000)
	binary.LittleEndian.PutUint32(visual[12:], 0xff00)
	binary.LittleEndian.PutUint32(visual[16:], 0xff)
	body = append(append(append(body, screen...), depth...), visual...)
	head := []byte{1, 0, 11, 0, 0, 0, 0, 0}
	binary.LittleEndian.PutUint16(head[6:], uint16(len(body)/4))
	return append(head, body...)
}

func (f *fakeX) serve(conn net.Conn) {
	defer conn.Close()
	head := make([]byte, 12)
	if _, err := io.ReadFull(conn, head); err != nil {
		return
	}
	nameSize, dataSize := int(binary.LittleEndian.Uint16(head[6:])), int(binary.LittleEndian.Uint16(head[8:]))
	auth := make([]byte, pad4(nameSize)+pad4(dataSize))
	io.ReadFull(conn, auth)
	f.mu.Lock()
	f.cookie = string(auth[pad4(nameSize) : pad4(nameSize)+dataSize])
	f.mu.Unlock()
	if f.cookie != testCookie {
		reason := "bad cookie!!"
		conn.Write(append([]byte{0, byte(len(reason)), 11, 0, 0, 0, byte(len(reason) / 4), 0}, reason...))
		return
	}
	conn.Write(f.setup())
	for {
		request := make([]byte, 4)
		if _, err := io.ReadFull(conn, request); err != nil {
			return
		}
		rest := make([]byte, int(binary.LittleEndian.Uint16(request[2:]))*4-4)
		io.ReadFull(conn, rest)
		f.mu.Lock()
		f.opcodes = append(f.opcodes, request[0])
		width, height, pixels := f.width, f.height, f.pixels
		f.mu.Unlock()
		reply := make([]byte, 32)
		reply[0] = 1
		switch request[0] {
		case x11QueryExtension:
			if !f.noXTest {
				reply[8], reply[9] = 1, testXTest
			}
			conn.Write(reply)
		case testXTest:
			// 입력 요청에는 응답이 없다.
			f.mu.Lock()
			f.fakes = append(f.fakes, [4]int{int(rest[0]), int(rest[1]),
				int(binary.LittleEndian.Uint16(rest[20:])), int(binary.LittleEndian.Uint16(rest[22:]))})
			f.mu.Unlock()
		case x11GetGeometry:
			binary.LittleEndian.PutUint16(reply[16:], uint16(width))
			binary.LittleEndian.PutUint16(reply[18:], uint16(height))
			conn.Write(reply)
		case x11GetImage:
			// 응답 앞에 이벤트가 끼어 와도 화면을 읽어야 한다.
			event := make([]byte, 32)
			event[0] = 34
			conn.Write(event)
			generic := make([]byte, 36)
			generic[0] = 35
			binary.LittleEndian.PutUint32(generic[4:], 1)
			conn.Write(generic)
			binary.LittleEndian.PutUint32(reply[4:], uint32(len(pixels)/4))
			conn.Write(append(reply, pixels...))
		default:
			failure := make([]byte, 32)
			failure[1] = 1
			conn.Write(failure)
		}
	}
}

func dialFake(t *testing.T, fake *fakeX, cookie string) (*x11Conn, error) {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { client.Close() })
	go fake.serve(server)
	return dialX11(client, []byte(cookie), 0)
}

// 3x2 화면이다. X 서버는 파랑, 초록, 빨강 순서로 준다.
var testPixels = []byte{
	0, 0, 255, 0, 0, 255, 0, 0, 255, 0, 0, 0,
	10, 20, 30, 0, 255, 255, 255, 0, 0, 0, 0, 0,
}

func TestScreenIsReadFromXServer(t *testing.T) {
	fake := &fakeX{width: 3, height: 2, pixels: testPixels}
	conn, err := dialFake(t, fake, testCookie)
	if err != nil {
		t.Fatal(err)
	}
	capturer := &sessionCapturer{session: protocol.ScreenSessionX11, conn: conn}
	frame, err := capturer.Capture()
	if err != nil {
		t.Fatal(err)
	}
	if frame.Width != 3 || frame.Height != 2 || frame.Unchanged {
		t.Fatalf("frame = %dx%d unchanged=%v", frame.Width, frame.Height, frame.Unchanged)
	}
	if _, err := jpeg.Decode(bytes.NewReader(frame.JPEG)); err != nil {
		t.Fatalf("JPEG가 아니다: %v", err)
	}
	if conn.root != testRoot {
		t.Errorf("root = %#x", conn.root)
	}

	again, err := capturer.Capture()
	if err != nil {
		t.Fatal(err)
	}
	if !again.Unchanged || again.JPEG != nil {
		t.Error("같은 화면을 다시 보냈다")
	}
	fake.mu.Lock()
	fake.pixels = append([]byte{1, 2, 3, 0}, testPixels[4:]...)
	fake.mu.Unlock()
	changed, err := capturer.Capture()
	if err != nil {
		t.Fatal(err)
	}
	if changed.Unchanged {
		t.Error("바뀐 화면을 같다고 했다")
	}

	// 보는 사람이 없어졌다가 다시 오면 같은 화면이라도 처음부터 보낸다.
	capturer.Rest()
	if capturer.conn != nil || capturer.hasLast {
		t.Error("쉬는 동안 연결이나 직전 화면을 놓지 않았다")
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	for _, opcode := range fake.opcodes {
		if opcode != x11GetGeometry && opcode != x11GetImage {
			t.Errorf("화면 읽기가 아닌 요청을 보냈다: %d", opcode)
		}
	}
}

func (f *fakeX) waitFakes(t *testing.T, count int) [][4]int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		f.mu.Lock()
		fakes := append([][4]int{}, f.fakes...)
		f.mu.Unlock()
		if len(fakes) >= count {
			return fakes
		}
		if time.Now().After(deadline) {
			t.Fatalf("입력 %d개를 기다렸는데 %d개만 왔다: %v", count, len(fakes), fakes)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInputIsInjectedThroughXTest(t *testing.T) {
	fake := &fakeX{width: 200, height: 100}
	conn, err := dialFake(t, fake, testCookie)
	if err != nil {
		t.Fatal(err)
	}
	controller, err := newX11Controller(conn)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []protocol.ScreenInput{
		{Type: protocol.InputMove, X: 0.5, Y: 0.25},
		{Type: protocol.InputDown, X: 1, Y: 1, Button: 1},
		{Type: protocol.InputUp, X: 1, Y: 1, Button: 1},
		{Type: protocol.InputWheel, X: 0, Y: 0, DY: 2},
		{Type: protocol.InputKeyDown, Code: "KeyA"},
		{Type: protocol.InputKeyDown, Code: "ShiftLeft"},
		// 모르는 키와 범위를 벗어난 값은 넣지 않는다.
		{Type: protocol.InputKeyDown, Code: "LaunchRocket"},
		{Type: protocol.InputMove, X: 2, Y: 0},
		{Type: "exec"},
	}
	for _, input := range inputs {
		if err := controller.Input(input); err != nil {
			t.Fatal(err)
		}
	}
	want := [][4]int{
		{xtestMotion, 0, 100, 25},
		{xtestMotion, 0, 199, 99}, {xtestButtonPress, 1, 0, 0},
		{xtestMotion, 0, 199, 99}, {xtestButtonRelease, 1, 0, 0},
		{xtestMotion, 0, 0, 0}, {xtestButtonPress, 5, 0, 0}, {xtestButtonRelease, 5, 0, 0}, {xtestButtonPress, 5, 0, 0}, {xtestButtonRelease, 5, 0, 0},
		{xtestKeyPress, 30 + evdevOffset, 0, 0},
		{xtestKeyPress, 42 + evdevOffset, 0, 0},
	}
	got := fake.waitFakes(t, len(want))
	if len(got) != len(want) {
		t.Fatalf("입력 수 = %d, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("입력 %d = %v, want %v", i, got[i], want[i])
		}
	}

	// 뗌 입력이 오지 않고 끊겨도 눌린 키를 그대로 두지 않는다.
	controller.Close()
	released := map[int]bool{}
	for _, fake := range fake.waitFakes(t, len(want)+2)[len(want):] {
		if fake[0] != xtestKeyRelease {
			t.Errorf("뗌이 아닌 입력을 보냈다: %v", fake)
		}
		released[fake[1]] = true
	}
	if !released[30+evdevOffset] || !released[42+evdevOffset] {
		t.Errorf("눌린 키를 떼지 않았다: %v", released)
	}
}

func TestControlNeedsInputExtension(t *testing.T) {
	conn, err := dialFake(t, &fakeX{width: 10, height: 10, noXTest: true}, testCookie)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newX11Controller(conn); err == nil {
		t.Error("입력 확장이 없는데 조작할 수 있다고 했다")
	}
	wayland := func(key string) string { return map[string]string{"XDG_SESSION_TYPE": "wayland"}[key] }
	if _, err := NewController(wayland); err == nil {
		t.Error("Wayland 세션에서 조작할 수 있다고 했다")
	}
}

type fakeController struct {
	mu     sync.Mutex
	inputs []protocol.ScreenInput
	closed int
}

func (f *fakeController) Input(input protocol.ScreenInput) error {
	f.mu.Lock()
	f.inputs = append(f.inputs, input)
	f.mu.Unlock()
	return nil
}

func (f *fakeController) ReleaseAll() {}

func (f *fakeController) Close() {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
}

func TestHelperTakesInputOnlyWhileViewerControls(t *testing.T) {
	var mu sync.Mutex
	var statuses []protocol.ScreenStatus
	frames, streams := 0, 0
	done := make(chan struct{})
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/agent/v1/screen/input":
			mu.Lock()
			streams++
			mu.Unlock()
			w.Write([]byte("\n{\"type\":\"keydown\",\"code\":\"KeyA\"}\n{\"type\":\"move\",\"x\":0.5,\"y\":0.5}\n"))
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/agent/v1/screen/poll":
			mu.Lock()
			defer mu.Unlock()
			var status protocol.ScreenStatus
			json.NewDecoder(r.Body).Decode(&status)
			statuses = append(statuses, status)
			if len(statuses) == 2 {
				close(done)
			}
			json.NewEncoder(w).Encode(protocol.ScreenDemand{Capture: len(statuses) == 1, IntervalMS: 1})
		case "/agent/v1/screen/frame":
			mu.Lock()
			defer mu.Unlock()
			frames++
			// 처음에는 보기만 하다가 조작을 켜고, 다시 끈 뒤 떠난다.
			json.NewEncoder(w).Encode(protocol.ScreenDemand{Capture: frames < 40, IntervalMS: 200, Control: frames >= 2 && frames < 4})
		}
	}))
	defer central.Close()

	controller := &fakeController{}
	opened := 0
	helper, err := New(Config{CentralURL: central.URL, ServerID: "home-1", Token: "t", Control: func() (Controller, error) {
		opened++
		return controller, nil
	}}, &fakeCapturer{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go helper.Run(ctx)
	deadline := time.After(10 * time.Second)
	for {
		controller.mu.Lock()
		got, closed := len(controller.inputs), controller.closed
		controller.mu.Unlock()
		if got >= 2 && closed >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("입력 %d개, 닫힘 %d번", got, closed)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.inputs[0].Code != "KeyA" || controller.inputs[1].Type != protocol.InputMove {
		t.Errorf("inputs = %+v", controller.inputs)
	}
	mu.Lock()
	defer mu.Unlock()
	if !statuses[0].Control {
		t.Error("조작을 허용한 설정을 알리지 않았다")
	}
	if streams != 1 || opened != 1 {
		t.Errorf("조작을 켠 동안에만 입력을 받아야 한다: 통로 %d번, 연결 %d번", streams, opened)
	}
}

func TestHelperWithoutControlNeverOpensInput(t *testing.T) {
	var mu sync.Mutex
	polls, frames, streams := 0, 0, 0
	var first protocol.ScreenStatus
	done := make(chan struct{})
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/agent/v1/screen/input":
			streams++
		case "/agent/v1/screen/poll":
			polls++
			if polls == 1 {
				json.NewDecoder(r.Body).Decode(&first)
			}
			if polls == 2 {
				close(done)
			}
			json.NewEncoder(w).Encode(protocol.ScreenDemand{Capture: polls == 1, IntervalMS: 1, Control: true})
		case "/agent/v1/screen/frame":
			frames++
			// 중앙 서버가 조작을 켜라고 해도 이 서버의 설정이 허용하지 않으면 받지 않는다.
			json.NewEncoder(w).Encode(protocol.ScreenDemand{Capture: frames < 3, IntervalMS: 1, Control: true})
		}
	}))
	defer central.Close()
	helper, err := New(Config{CentralURL: central.URL, ServerID: "home-1", Token: "t"}, &fakeCapturer{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go helper.Run(ctx)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("도우미가 다시 기다리지 않았다")
	}
	mu.Lock()
	defer mu.Unlock()
	if first.Control {
		t.Error("허용하지 않았는데 조작할 수 있다고 알렸다")
	}
	if streams != 0 {
		t.Error("허용하지 않았는데 입력 통로를 열었다")
	}
}

func TestXServerRefusalIsReported(t *testing.T) {
	if _, err := dialFake(t, &fakeX{}, "wrong-cookie-0000"); err == nil {
		t.Error("거절된 접속을 성공으로 봤다")
	}
}

func TestPixelsAreConverted(t *testing.T) {
	standard := pixelFormat{bitsPerPixel: 32, scanlinePad: 32, order: binary.LittleEndian, red: 0xff0000, green: 0xff00, blue: 0xff}
	picture, err := toRGBA(testPixels, 3, 2, standard, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := picture.Pix[:8]; !bytes.Equal(got, []byte{255, 0, 0, 255, 0, 255, 0, 255}) {
		t.Errorf("첫 두 화소 = %v", got)
	}
	if got := picture.Pix[picture.Stride : picture.Stride+4]; !bytes.Equal(got, []byte{30, 20, 10, 255}) {
		t.Errorf("둘째 줄 첫 화소 = %v", got)
	}

	// 16비트 화면은 줄 끝을 4바이트에 맞춰 채운다.
	high := pixelFormat{bitsPerPixel: 16, scanlinePad: 32, order: binary.LittleEndian, red: 0xf800, green: 0x07e0, blue: 0x001f}
	raw := []byte{0x00, 0xf8, 0xe0, 0x07, 0x1f, 0x00, 0, 0}
	picture, err = toRGBA(raw, 3, 1, high, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := picture.Pix; !bytes.Equal(got, []byte{255, 0, 0, 255, 0, 255, 0, 255, 0, 0, 255, 255}) {
		t.Errorf("16비트 화소 = %v", got)
	}

	// 넓은 화면은 묶어서 평균을 낸다.
	wide := []byte{
		0, 0, 100, 0, 0, 0, 200, 0,
		0, 0, 0, 0, 0, 0, 100, 0,
	}
	picture, err = toRGBA(wide, 2, 2, standard, 2)
	if err != nil {
		t.Fatal(err)
	}
	if size := picture.Bounds().Size(); size.X != 1 || size.Y != 1 || picture.Pix[0] != 100 {
		t.Errorf("줄인 화면 = %v %v", size, picture.Pix)
	}

	if _, err := toRGBA(testPixels[:10], 3, 2, standard, 1); err == nil {
		t.Error("모자란 화면을 받아들였다")
	}
	if _, err := toRGBA(testPixels, 3, 2, pixelFormat{bitsPerPixel: 8, scanlinePad: 32, order: binary.LittleEndian}, 1); err == nil {
		t.Error("지원하지 않는 형식을 받아들였다")
	}
}

type fakeCapturer struct {
	mu       sync.Mutex
	captures int
	rests    int
	fail     error
}

func (f *fakeCapturer) Status() protocol.ScreenStatus {
	return protocol.ScreenStatus{Session: protocol.ScreenSessionX11, State: protocol.ScreenReady}
}

func (f *fakeCapturer) Capture() (Frame, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.captures++
	if f.fail != nil {
		return Frame{}, f.fail
	}
	return Frame{JPEG: []byte{0xff, 0xd8, 0xff, 0xd9}, Width: 4, Height: 2}, nil
}

func (f *fakeCapturer) Rest() {
	f.mu.Lock()
	f.rests++
	f.mu.Unlock()
}

func (f *fakeCapturer) counts() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.captures, f.rests
}

func TestHelperCapturesOnlyWhileSomeoneWatches(t *testing.T) {
	var mu sync.Mutex
	polls, frames := 0, 0
	var statuses []protocol.ScreenStatus
	done := make(chan struct{})
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer screen-token" || r.Header.Get("X-MSP-Server") != "home-1" {
			http.Error(w, "인증 실패", http.StatusUnauthorized)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		demand := protocol.ScreenDemand{}
		switch r.URL.Path {
		case "/agent/v1/screen/poll":
			var status protocol.ScreenStatus
			json.NewDecoder(r.Body).Decode(&status)
			statuses = append(statuses, status)
			polls++
			// 첫 요청에는 보는 사람이 없고, 둘째 요청에 생긴다.
			demand.Capture = polls == 2
			if polls == 3 {
				close(done)
			}
		case "/agent/v1/screen/frame":
			frames++
			if r.URL.Query().Get("width") != "4" || r.URL.Query().Get("height") != "2" {
				t.Errorf("화면 크기를 보내지 않았다: %s", r.URL.RawQuery)
			}
			// 두 장을 받은 뒤 보는 사람이 떠난다.
			demand.Capture = frames < 2
		}
		demand.IntervalMS = 1
		json.NewEncoder(w).Encode(demand)
	}))
	defer central.Close()

	capturer := &fakeCapturer{}
	helper, err := New(Config{CentralURL: central.URL, ServerID: "home-1", Token: "screen-token"}, capturer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go helper.Run(ctx)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("도우미가 다시 기다리지 않았다")
	}
	captures, rests := capturer.counts()
	if captures != 2 {
		t.Errorf("보는 사람이 있을 때만 읽어야 한다: %d번 읽음", captures)
	}
	if rests != 1 {
		t.Errorf("보는 사람이 떠난 뒤 쉬지 않았다: %d", rests)
	}
}

func TestHelperReportsCaptureFailure(t *testing.T) {
	var mu sync.Mutex
	var statuses []protocol.ScreenStatus
	done := make(chan struct{})
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		var status protocol.ScreenStatus
		json.NewDecoder(r.Body).Decode(&status)
		statuses = append(statuses, status)
		if len(statuses) == 2 {
			close(done)
		}
		json.NewEncoder(w).Encode(protocol.ScreenDemand{Capture: len(statuses) == 1, IntervalMS: 1})
	}))
	defer central.Close()

	capturer := &fakeCapturer{fail: io.ErrUnexpectedEOF}
	helper, err := New(Config{CentralURL: central.URL, ServerID: "home-1", Token: "t"}, capturer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go helper.Run(ctx)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("실패를 알리지 않았다")
	}
	mu.Lock()
	defer mu.Unlock()
	if got := statuses[1]; got.State != protocol.ScreenError || got.Detail == "" {
		t.Errorf("읽기 실패를 상태로 알리지 않았다: %+v", got)
	}
}

func TestHelperRefusesPlainHTTPOutsideLoopback(t *testing.T) {
	if _, err := New(Config{CentralURL: "http://central.example.test", ServerID: "home-1", Token: "t"}, &fakeCapturer{}); err == nil {
		t.Error("토큰이 평문으로 나가는 주소를 받아들였다")
	}
}
