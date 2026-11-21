// Package console은 리눅스 가상 터미널의 글자 화면을 읽는다.
//
// 화면 내용을 담은 장치(/dev/vcsa, /dev/vcsu)를 읽기 전용으로 열 뿐이다. 터미널 장치는 열지 않으므로 입력을 보낼 수 없다.
package console

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxTTY는 리눅스가 두는 가상 터미널 수의 상한이다.
const MaxTTY = 63

// Screen은 가상 터미널 한 곳의 글자 화면이다.
type Screen struct {
	TTY       int
	Active    int
	Rows      int
	Cols      int
	CursorRow int
	CursorCol int
	Lines     []string
}

// Active는 서버 모니터에 지금 나오는 가상 터미널 번호를 읽는다.
func Active(sysRoot string) (int, error) {
	body, err := os.ReadFile(filepath.Join(sysRoot, "class/tty/tty0/active"))
	if errors.Is(err, fs.ErrNotExist) {
		return 0, errors.New("이 서버에는 텍스트 콘솔이 없습니다")
	}
	if err != nil {
		return 0, fmt.Errorf("켜져 있는 가상 터미널을 읽지 못했습니다: %w", err)
	}
	number, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(string(body)), "tty"))
	if err != nil || number < 1 || number > MaxTTY {
		return 0, fmt.Errorf("켜져 있는 가상 터미널을 알 수 없습니다: %q", strings.TrimSpace(string(body)))
	}
	return number, nil
}

// Read는 가상 터미널의 글자 화면을 읽는다. tty가 0이면 지금 켜져 있는 터미널이다.
func Read(devRoot, sysRoot string, tty int) (Screen, error) {
	if tty < 0 || tty > MaxTTY {
		return Screen{}, fmt.Errorf("가상 터미널 번호가 잘못되었습니다: %d", tty)
	}
	active, activeErr := Active(sysRoot)
	if tty == 0 {
		if activeErr != nil {
			return Screen{}, activeErr
		}
		tty = active
	}
	screen := Screen{TTY: tty, Active: active}

	// 앞 4바이트는 줄 수, 칸 수, 커서 위치다. 뒤로 칸마다 글자와 색 속성이 2바이트씩 온다.
	body, err := os.ReadFile(filepath.Join(devRoot, "vcsa"+strconv.Itoa(tty)))
	if err != nil {
		return screen, describe(err, tty)
	}
	if len(body) < 4 {
		return screen, errors.New("콘솔 장치에서 화면 크기를 읽지 못했습니다")
	}
	rows, cols := int(body[0]), int(body[1])
	cells := body[4:]
	if rows == 0 || cols == 0 || len(cells) < rows*cols*2 {
		return screen, errors.New("콘솔 장치의 내용이 화면 크기와 맞지 않습니다")
	}
	screen.Rows, screen.Cols = rows, cols
	screen.CursorCol, screen.CursorRow = int(body[2]), int(body[3])

	// vcsu는 칸마다 유니코드 값을 준다. 없거나 그 사이 화면 크기가 바뀌었으면 글꼴 번호를 그대로 쓴다.
	wide, err := os.ReadFile(filepath.Join(devRoot, "vcsu"+strconv.Itoa(tty)))
	if err != nil || len(wide) != rows*cols*4 {
		wide = nil
	}
	screen.Lines = make([]string, rows)
	var line strings.Builder
	for row := range rows {
		line.Reset()
		for col := range cols {
			cell := row*cols + col
			var char rune
			if wide != nil {
				char = rune(binary.NativeEndian.Uint32(wide[cell*4:]))
			} else {
				char = rune(binary.NativeEndian.Uint16(cells[cell*2:]) & 0xff)
			}
			if !utf8.ValidRune(char) || !unicode.IsPrint(char) {
				char = ' '
			}
			line.WriteRune(char)
		}
		screen.Lines[row] = strings.TrimRight(line.String(), " ")
	}
	return screen, nil
}

func describe(err error, tty int) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("tty%d의 콘솔 장치가 없습니다. 이 터미널이 한 번도 열리지 않았거나 이 서버에 텍스트 콘솔이 없습니다", tty)
	case errors.Is(err, fs.ErrPermission):
		return errors.New("콘솔 장치를 읽을 권한이 없습니다. 에이전트 서비스 파일을 새 것으로 바꿔 설치해야 합니다")
	}
	return fmt.Errorf("콘솔 장치를 읽지 못했습니다: %w", err)
}
