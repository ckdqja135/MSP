package screen

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

// Controller는 서버 화면에 마우스와 키보드 입력을 넣는다. 도우미 설정에서 조작을 허용했을 때만 만든다.
type Controller interface {
	Input(input protocol.ScreenInput) error
	// ReleaseAll은 눌린 채 남은 키와 버튼을 뗀다. 연결이 끊겨 뗌 입력이 오지 않아도 눌린 채로 두지 않기 위해서다.
	ReleaseAll()
	Close()
}

// XTEST 확장의 입력 종류다. 실제 장치가 보낸 것과 같은 입력을 X 서버가 만들어 낸다.
const (
	xtestFakeInput = 2

	xtestKeyPress      = 2
	xtestKeyRelease    = 3
	xtestButtonPress   = 4
	xtestButtonRelease = 5
	xtestMotion        = 6
)

// 리눅스 X 서버의 키 번호는 커널 키 번호에 8을 더한 값이다.
const evdevOffset = 8

// keyCodes는 브라우저가 알려 주는 물리 키 이름을 커널 키 번호로 바꾼다.
// 글자가 아니라 키 자리로 보내므로 서버의 키보드 배열과 한글 입력기가 그대로 적용된다.
var keyCodes = map[string]byte{
	"Escape": 1, "Digit1": 2, "Digit2": 3, "Digit3": 4, "Digit4": 5, "Digit5": 6, "Digit6": 7, "Digit7": 8,
	"Digit8": 9, "Digit9": 10, "Digit0": 11, "Minus": 12, "Equal": 13, "Backspace": 14, "Tab": 15,
	"KeyQ": 16, "KeyW": 17, "KeyE": 18, "KeyR": 19, "KeyT": 20, "KeyY": 21, "KeyU": 22, "KeyI": 23, "KeyO": 24, "KeyP": 25,
	"BracketLeft": 26, "BracketRight": 27, "Enter": 28, "ControlLeft": 29,
	"KeyA": 30, "KeyS": 31, "KeyD": 32, "KeyF": 33, "KeyG": 34, "KeyH": 35, "KeyJ": 36, "KeyK": 37, "KeyL": 38,
	"Semicolon": 39, "Quote": 40, "Backquote": 41, "ShiftLeft": 42, "Backslash": 43,
	"KeyZ": 44, "KeyX": 45, "KeyC": 46, "KeyV": 47, "KeyB": 48, "KeyN": 49, "KeyM": 50,
	"Comma": 51, "Period": 52, "Slash": 53, "ShiftRight": 54, "NumpadMultiply": 55, "AltLeft": 56, "Space": 57, "CapsLock": 58,
	"F1": 59, "F2": 60, "F3": 61, "F4": 62, "F5": 63, "F6": 64, "F7": 65, "F8": 66, "F9": 67, "F10": 68,
	"NumLock": 69, "ScrollLock": 70, "Numpad7": 71, "Numpad8": 72, "Numpad9": 73, "NumpadSubtract": 74,
	"Numpad4": 75, "Numpad5": 76, "Numpad6": 77, "NumpadAdd": 78, "Numpad1": 79, "Numpad2": 80, "Numpad3": 81,
	"Numpad0": 82, "NumpadDecimal": 83, "IntlBackslash": 86, "F11": 87, "F12": 88,
	"NumpadEnter": 96, "ControlRight": 97, "NumpadDivide": 98, "PrintScreen": 99, "AltRight": 100,
	"Home": 102, "ArrowUp": 103, "PageUp": 104, "ArrowLeft": 105, "ArrowRight": 106, "End": 107, "ArrowDown": 108,
	"PageDown": 109, "Insert": 110, "Delete": 111, "Pause": 119, "Lang1": 122, "Lang2": 123,
	"MetaLeft": 125, "MetaRight": 126, "ContextMenu": 127,
}

type x11Controller struct {
	mu      sync.Mutex
	conn    *x11Conn
	opcode  byte
	width   int
	height  int
	keys    map[byte]bool
	buttons map[byte]bool
}

// newX11Controller는 입력용 연결을 따로 연다. 화면 읽기 연결과 섞이면 응답 순서가 어긋난다.
func newX11Controller(conn *x11Conn) (*x11Controller, error) {
	opcode, err := conn.extension("XTEST")
	if err != nil {
		return nil, err
	}
	width, height, err := conn.geometry()
	if err != nil {
		return nil, err
	}
	if width < 1 || height < 1 {
		return nil, errors.New("화면 크기를 읽지 못했습니다")
	}
	// 입력 요청에는 응답이 없다. 서버가 보내는 이벤트와 오류는 읽어서 버린다.
	conn.conn.SetReadDeadline(time.Time{})
	go io.Copy(io.Discard, conn.conn)
	return &x11Controller{conn: conn, opcode: opcode, width: width, height: height, keys: map[byte]bool{}, buttons: map[byte]bool{}}, nil
}

// extension은 확장 기능의 요청 번호를 묻는다. 없으면 오류다.
func (c *x11Conn) extension(name string) (byte, error) {
	c.conn.SetDeadline(time.Now().Add(x11Timeout))
	request := make([]byte, 8+pad4(len(name)))
	request[0] = x11QueryExtension
	binary.LittleEndian.PutUint16(request[2:], uint16(len(request)/4))
	binary.LittleEndian.PutUint16(request[4:], uint16(len(name)))
	copy(request[8:], name)
	if _, err := c.conn.Write(request); err != nil {
		return 0, err
	}
	head, _, err := c.reply()
	if err != nil {
		return 0, err
	}
	if head[8] == 0 {
		return 0, errors.New("X 서버에 입력 확장(" + name + ")이 없어 조작할 수 없습니다")
	}
	return head[9], nil
}

func (c *x11Controller) fake(kind, detail byte, x, y int) error {
	request := make([]byte, 36)
	request[0], request[1] = c.opcode, xtestFakeInput
	binary.LittleEndian.PutUint16(request[2:], 9)
	request[4], request[5] = kind, detail
	if kind == xtestMotion {
		binary.LittleEndian.PutUint32(request[12:], c.conn.root)
	}
	binary.LittleEndian.PutUint16(request[24:], uint16(int16(x)))
	binary.LittleEndian.PutUint16(request[26:], uint16(int16(y)))
	c.conn.conn.SetWriteDeadline(time.Now().Add(x11Timeout))
	_, err := c.conn.conn.Write(request)
	return err
}

func (c *x11Controller) move(input protocol.ScreenInput) error {
	x := min(int(input.X*float64(c.width)), c.width-1)
	y := min(int(input.Y*float64(c.height)), c.height-1)
	return c.fake(xtestMotion, 0, x, y)
}

func (c *x11Controller) click(button byte) error {
	if err := c.fake(xtestButtonPress, button, 0, 0); err != nil {
		return err
	}
	return c.fake(xtestButtonRelease, button, 0, 0)
}

// Input은 입력 하나를 X 서버에 넣는다. 모르는 키와 범위를 벗어난 값은 넣지 않는다.
func (c *x11Controller) Input(input protocol.ScreenInput) error {
	if !input.Valid() {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch input.Type {
	case protocol.InputMove:
		return c.move(input)
	case protocol.InputDown, protocol.InputUp:
		if err := c.move(input); err != nil {
			return err
		}
		button := byte(input.Button)
		if input.Type == protocol.InputDown {
			c.buttons[button] = true
			return c.fake(xtestButtonPress, button, 0, 0)
		}
		delete(c.buttons, button)
		return c.fake(xtestButtonRelease, button, 0, 0)
	case protocol.InputWheel:
		if err := c.move(input); err != nil {
			return err
		}
		// 휠은 버튼 4(위), 5(아래), 6(왼쪽), 7(오른쪽)을 한 번씩 누르는 것으로 전한다.
		steps := []struct {
			count  int
			button byte
		}{{-input.DY, 4}, {input.DY, 5}, {-input.DX, 6}, {input.DX, 7}}
		for _, step := range steps {
			for range max(step.count, 0) {
				if err := c.click(step.button); err != nil {
					return err
				}
			}
		}
		return nil
	}
	code, known := keyCodes[input.Code]
	if !known {
		return nil
	}
	keycode := code + evdevOffset
	if input.Type == protocol.InputKeyDown {
		c.keys[keycode] = true
		return c.fake(xtestKeyPress, keycode, 0, 0)
	}
	delete(c.keys, keycode)
	return c.fake(xtestKeyRelease, keycode, 0, 0)
}

func (c *x11Controller) ReleaseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for keycode := range c.keys {
		c.fake(xtestKeyRelease, keycode, 0, 0)
	}
	for button := range c.buttons {
		c.fake(xtestButtonRelease, button, 0, 0)
	}
	clear(c.keys)
	clear(c.buttons)
}

func (c *x11Controller) Close() {
	c.ReleaseAll()
	c.conn.close()
}

// NewController는 지금 세션에 입력을 넣을 연결을 연다. X11 세션에서만 된다.
func NewController(env func(string) string) (Controller, error) {
	if DetectSession(env) != protocol.ScreenSessionX11 {
		return nil, errors.New("X11 세션에서만 조작할 수 있습니다")
	}
	conn, err := dialSession(env)
	if err != nil {
		return nil, err
	}
	controller, err := newX11Controller(conn)
	if err != nil {
		conn.close()
		return nil, err
	}
	return controller, nil
}
