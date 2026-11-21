package screen

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"time"
)

// 화면 읽기에 쓰는 요청은 화면 크기 조회와 화면 읽기 둘뿐이다.
// 입력을 넣는 요청은 조작을 허용한 설정에서만 여는 별도 연결(control.go)에서만 보낸다.
const (
	x11GetGeometry    = 14
	x11GetImage       = 73
	x11QueryExtension = 98
	x11ZPixmap        = 2

	x11Timeout = 15 * time.Second
	// 한 번에 받는 화면의 상한이다. 잘못된 응답 길이로 메모리를 잡지 않게 한다.
	maxImageBytes = 512 << 20
)

// pixelFormat은 X 서버가 화면을 내주는 형식이다.
type pixelFormat struct {
	bitsPerPixel int
	scanlinePad  int
	order        binary.ByteOrder
	red          uint32
	green        uint32
	blue         uint32
}

type x11Conn struct {
	conn   net.Conn
	root   uint32
	format pixelFormat
}

func pad4(n int) int { return (n + 3) &^ 3 }

// dialX11은 X 서버에 접속해 루트 창과 화면 형식을 읽는다. cookie가 없으면 인증 없이 시도한다.
func dialX11(conn net.Conn, cookie []byte, screen int) (*x11Conn, error) {
	conn.SetDeadline(time.Now().Add(x11Timeout))
	name := ""
	if len(cookie) > 0 {
		name = xauthCookieName
	}
	request := make([]byte, 12+pad4(len(name))+pad4(len(cookie)))
	request[0] = 'l'
	binary.LittleEndian.PutUint16(request[2:], 11)
	binary.LittleEndian.PutUint16(request[6:], uint16(len(name)))
	binary.LittleEndian.PutUint16(request[8:], uint16(len(cookie)))
	copy(request[12:], name)
	copy(request[12+pad4(len(name)):], cookie)
	if _, err := conn.Write(request); err != nil {
		return nil, err
	}

	head := make([]byte, 8)
	if _, err := io.ReadFull(conn, head); err != nil {
		return nil, err
	}
	body := make([]byte, int(binary.LittleEndian.Uint16(head[6:]))*4)
	if _, err := io.ReadFull(conn, body); err != nil {
		return nil, err
	}
	if head[0] != 1 {
		reason := body
		if int(head[1]) <= len(body) {
			reason = body[:head[1]]
		}
		return nil, fmt.Errorf("X 서버가 접속을 거절했습니다: %s", string(reason))
	}
	c := &x11Conn{conn: conn}
	if err := c.readSetup(body, screen); err != nil {
		return nil, err
	}
	return c, nil
}

var errSetup = errors.New("X 서버의 접속 응답을 해석하지 못했습니다")

func (c *x11Conn) readSetup(body []byte, screen int) error {
	if len(body) < 32 {
		return errSetup
	}
	vendor := int(binary.LittleEndian.Uint16(body[16:]))
	screens, formats := int(body[20]), int(body[21])
	c.format.order = binary.LittleEndian
	if body[22] != 0 {
		c.format.order = binary.BigEndian
	}
	at := 32 + pad4(vendor)
	if len(body) < at+formats*8 {
		return errSetup
	}
	formatList := body[at : at+formats*8]
	at += formats * 8
	if screen >= screens {
		return fmt.Errorf("스크린 %d번이 없습니다. X 서버의 스크린은 %d개입니다", screen, screens)
	}

	for index := 0; ; index++ {
		if len(body) < at+40 {
			return errSetup
		}
		root := binary.LittleEndian.Uint32(body[at:])
		rootVisual := binary.LittleEndian.Uint32(body[at+32:])
		rootDepth, depths := body[at+38], int(body[at+39])
		at += 40
		found := false
		for range depths {
			if len(body) < at+8 {
				return errSetup
			}
			visuals := int(binary.LittleEndian.Uint16(body[at+2:]))
			at += 8
			if len(body) < at+visuals*24 {
				return errSetup
			}
			for range visuals {
				if binary.LittleEndian.Uint32(body[at:]) == rootVisual {
					c.format.red = binary.LittleEndian.Uint32(body[at+8:])
					c.format.green = binary.LittleEndian.Uint32(body[at+12:])
					c.format.blue = binary.LittleEndian.Uint32(body[at+16:])
					found = true
				}
				at += 24
			}
		}
		if index != screen {
			continue
		}
		if !found {
			return errors.New("루트 창의 색 형식을 찾지 못했습니다")
		}
		c.root = root
		for i := 0; i < len(formatList); i += 8 {
			if formatList[i] == rootDepth {
				c.format.bitsPerPixel, c.format.scanlinePad = int(formatList[i+1]), int(formatList[i+2])
				return nil
			}
		}
		return fmt.Errorf("색 깊이 %d의 화면 형식을 찾지 못했습니다", rootDepth)
	}
}

func (c *x11Conn) close() { c.conn.Close() }

// reply는 다음 응답을 읽는다. 사이에 끼어 오는 이벤트는 건너뛴다.
func (c *x11Conn) reply() (head []byte, extra []byte, err error) {
	for {
		head = make([]byte, 32)
		if _, err := io.ReadFull(c.conn, head); err != nil {
			return nil, nil, err
		}
		length := int64(binary.LittleEndian.Uint32(head[4:])) * 4
		switch head[0] {
		case 0:
			return nil, nil, fmt.Errorf("X 서버가 요청을 거절했습니다 (오류 %d)", head[1])
		case 1:
			if length > maxImageBytes {
				return nil, nil, errors.New("X 서버 응답이 너무 큽니다")
			}
			extra = make([]byte, length)
			if _, err := io.ReadFull(c.conn, extra); err != nil {
				return nil, nil, err
			}
			return head, extra, nil
		}
		// 확장 이벤트만 32바이트 뒤로 내용이 더 온다.
		if head[0]&0x7f == 35 {
			if _, err := io.CopyN(io.Discard, c.conn, length); err != nil {
				return nil, nil, err
			}
		}
	}
}

// geometry는 루트 창의 지금 크기를 읽는다. 해상도가 바뀌어도 맞는 크기로 읽기 위해 매번 묻는다.
func (c *x11Conn) geometry() (width, height int, err error) {
	c.conn.SetDeadline(time.Now().Add(x11Timeout))
	request := make([]byte, 8)
	request[0] = x11GetGeometry
	binary.LittleEndian.PutUint16(request[2:], 2)
	binary.LittleEndian.PutUint32(request[4:], c.root)
	if _, err := c.conn.Write(request); err != nil {
		return 0, 0, err
	}
	head, _, err := c.reply()
	if err != nil {
		return 0, 0, err
	}
	return int(binary.LittleEndian.Uint16(head[16:])), int(binary.LittleEndian.Uint16(head[18:])), nil
}

// image는 루트 창 전체를 읽는다. 돌려주는 값은 X 서버가 준 화소 그대로다.
func (c *x11Conn) image(width, height int) ([]byte, error) {
	c.conn.SetDeadline(time.Now().Add(x11Timeout))
	request := make([]byte, 20)
	request[0] = x11GetImage
	request[1] = x11ZPixmap
	binary.LittleEndian.PutUint16(request[2:], 5)
	binary.LittleEndian.PutUint32(request[4:], c.root)
	binary.LittleEndian.PutUint16(request[12:], uint16(width))
	binary.LittleEndian.PutUint16(request[14:], uint16(height))
	binary.LittleEndian.PutUint32(request[16:], 0xffffffff)
	if _, err := c.conn.Write(request); err != nil {
		return nil, err
	}
	_, data, err := c.reply()
	return data, err
}
