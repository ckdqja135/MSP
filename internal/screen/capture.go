package screen

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"math/bits"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	// 이보다 넓은 화면은 정수 배율로 줄여 보낸다. 전송량과 화면 보기 창의 부담을 줄이기 위해서다.
	maxFrameWidth = 2560
	jpegQuality   = 75
)

// Frame은 한 번 읽은 화면이다. Unchanged면 직전 화면과 같아 그림을 담지 않는다.
type Frame struct {
	JPEG      []byte
	Width     int
	Height    int
	Unchanged bool
}

// Capturer는 서버 화면을 읽는다. 읽기만 하고 화면에 아무것도 보내지 않는다.
type Capturer interface {
	// Status는 화면을 가져올 수 있는 상태인지 알린다.
	Status() protocol.ScreenStatus
	Capture() (Frame, error)
	// Rest는 보는 사람이 없어졌을 때 불린다. 연결과 직전 화면 기억을 놓는다.
	Rest()
}

// DetectSession은 환경 변수로 그래픽 세션 종류를 가린다.
// Wayland 세션에도 X 프로그램용 DISPLAY가 있으므로 Wayland를 먼저 본다.
func DetectSession(env func(string) string) string {
	if env("XDG_SESSION_TYPE") == protocol.ScreenSessionWayland || env("WAYLAND_DISPLAY") != "" {
		return protocol.ScreenSessionWayland
	}
	if env("DISPLAY") != "" {
		return protocol.ScreenSessionX11
	}
	return ""
}

type sessionCapturer struct {
	session string
	env     func(string) string
	conn    *x11Conn
	last    [sha256.Size]byte
	hasLast bool
}

// NewCapturer는 지금 세션에 맞는 화면 읽기를 만든다. 환경 변수는 env로 받는다.
func NewCapturer(env func(string) string) Capturer {
	return &sessionCapturer{session: DetectSession(env), env: env}
}

func (c *sessionCapturer) Status() protocol.ScreenStatus {
	switch c.session {
	case protocol.ScreenSessionX11:
		return protocol.ScreenStatus{Session: c.session, State: protocol.ScreenReady}
	case protocol.ScreenSessionWayland:
		return protocol.ScreenStatus{Session: c.session, State: protocol.ScreenUnsupported,
			Detail: "서버가 Wayland 세션으로 떠 있습니다. 지금은 X11 세션의 화면만 가져올 수 있습니다. 서버 로그인 화면의 톱니바퀴에서 Xorg 세션을 골라 로그인하면 볼 수 있습니다."}
	}
	return protocol.ScreenStatus{State: protocol.ScreenError,
		Detail: "그래픽 세션을 찾지 못했습니다. 데스크톱에 로그인한 사용자로 실행해야 합니다."}
}

func (c *sessionCapturer) Rest() {
	if c.conn != nil {
		c.conn.close()
		c.conn = nil
	}
	c.hasLast = false
}

// dialSession은 환경 변수가 가리키는 X 서버에 접속한다.
func dialSession(env func(string) string) (*x11Conn, error) {
	display, screen, ok := parseDisplay(env("DISPLAY"))
	if !ok {
		return nil, fmt.Errorf("DISPLAY 값을 해석하지 못했습니다: %q", env("DISPLAY"))
	}
	socket := "/tmp/.X11-unix/X" + strconv.Itoa(display)
	conn, err := net.DialTimeout("unix", socket, x11Timeout)
	if err != nil {
		// 소켓 파일 없이 이름만 있는 소켓으로 여는 X 서버도 있다.
		if conn, err = net.DialTimeout("unix", "@"+socket, x11Timeout); err != nil {
			return nil, fmt.Errorf("X 서버에 접속하지 못했습니다: %w", err)
		}
	}
	x, err := dialX11(conn, sessionCookie(env, display), screen)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return x, nil
}

func sessionCookie(env func(string) string, display int) []byte {
	path := env("XAUTHORITY")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		path = filepath.Join(home, ".Xauthority")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	hostname, _ := os.Hostname()
	return pickCookie(parseXauthority(body), hostname, display)
}

func (c *sessionCapturer) Capture() (Frame, error) {
	if c.session != protocol.ScreenSessionX11 {
		return Frame{}, errors.New(c.Status().Detail)
	}
	if c.conn == nil {
		conn, err := dialSession(c.env)
		if err != nil {
			return Frame{}, err
		}
		c.conn = conn
	}
	frame, err := c.read()
	if err != nil {
		// 끊긴 연결을 다음에 다시 열게 한다.
		c.Rest()
	}
	return frame, err
}

func (c *sessionCapturer) read() (Frame, error) {
	width, height, err := c.conn.geometry()
	if err != nil {
		return Frame{}, err
	}
	raw, err := c.conn.image(width, height)
	if err != nil {
		return Frame{}, err
	}
	sum := sha256.Sum256(raw)
	if c.hasLast && sum == c.last {
		return Frame{Unchanged: true}, nil
	}
	picture, err := toRGBA(raw, width, height, c.conn.format, (width+maxFrameWidth-1)/maxFrameWidth)
	if err != nil {
		return Frame{}, err
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, picture, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return Frame{}, err
	}
	c.last, c.hasLast = sum, true
	size := picture.Bounds().Size()
	return Frame{JPEG: out.Bytes(), Width: size.X, Height: size.Y}, nil
}

type channel struct {
	mask  uint32
	shift int
	bits  int
}

func newChannel(mask uint32) channel {
	return channel{mask: mask, shift: bits.TrailingZeros32(mask), bits: bits.OnesCount32(mask)}
}

// value는 화소에서 한 색을 꺼내 8비트로 맞춘다.
func (c channel) value(pixel uint32) uint32 {
	if c.bits == 0 {
		return 0
	}
	v := (pixel & c.mask) >> c.shift
	if c.bits >= 8 {
		return v >> (c.bits - 8)
	}
	return v * 255 / (1<<c.bits - 1)
}

// toRGBA는 X 서버가 준 화소를 그림으로 바꾼다. factor가 2 이상이면 그만큼 묶어 평균을 낸다.
func toRGBA(raw []byte, width, height int, format pixelFormat, factor int) (*image.RGBA, error) {
	bytesPerPixel := format.bitsPerPixel / 8
	if format.bitsPerPixel%8 != 0 || bytesPerPixel < 2 || bytesPerPixel > 4 || format.scanlinePad < 8 {
		return nil, fmt.Errorf("지원하지 않는 화면 형식입니다 (화소당 %d비트)", format.bitsPerPixel)
	}
	stride := (width*format.bitsPerPixel + format.scanlinePad - 1) / format.scanlinePad * format.scanlinePad / 8
	if width < 1 || height < 1 || len(raw) < stride*height {
		return nil, errors.New("받은 화면이 화면 크기와 맞지 않습니다")
	}
	factor = max(1, min(factor, width, height))
	outWidth, outHeight := width/factor, height/factor
	picture := image.NewRGBA(image.Rect(0, 0, outWidth, outHeight))

	// 가장 흔한 형식은 바이트를 바로 옮긴다.
	little := format.order == binary.LittleEndian
	if factor == 1 && bytesPerPixel == 4 && little && format.red == 0xff0000 && format.green == 0xff00 && format.blue == 0xff {
		for y := range height {
			src := raw[y*stride : y*stride+width*4]
			dst := picture.Pix[y*picture.Stride : y*picture.Stride+width*4]
			for x := 0; x < len(src); x += 4 {
				dst[x], dst[x+1], dst[x+2], dst[x+3] = src[x+2], src[x+1], src[x], 0xff
			}
		}
		return picture, nil
	}

	red, green, blue := newChannel(format.red), newChannel(format.green), newChannel(format.blue)
	pixel := func(x, y int) uint32 {
		at := raw[y*stride+x*bytesPerPixel:]
		switch bytesPerPixel {
		case 2:
			return uint32(format.order.Uint16(at))
		case 3:
			if little {
				return uint32(at[0]) | uint32(at[1])<<8 | uint32(at[2])<<16
			}
			return uint32(at[2]) | uint32(at[1])<<8 | uint32(at[0])<<16
		}
		return format.order.Uint32(at)
	}
	count := uint32(factor * factor)
	for oy := range outHeight {
		for ox := range outWidth {
			var r, g, b uint32
			for dy := range factor {
				for dx := range factor {
					p := pixel(ox*factor+dx, oy*factor+dy)
					r += red.value(p)
					g += green.value(p)
					b += blue.value(p)
				}
			}
			at := oy*picture.Stride + ox*4
			picture.Pix[at], picture.Pix[at+1], picture.Pix[at+2], picture.Pix[at+3] = uint8(r/count), uint8(g/count), uint8(b/count), 0xff
		}
	}
	return picture, nil
}
