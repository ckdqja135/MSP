package console

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConsole은 가상 터미널 장치와 같은 형식의 파일을 만든다.
func writeConsole(t *testing.T, tty string, rows, cols int, text []string, unicode bool) (dev, sys string) {
	t.Helper()
	dev, sys = t.TempDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Join(sys, "class/tty/tty0"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(sys, "class/tty/tty0/active"), []byte("tty2\n"), 0o644)
	narrow := []byte{byte(rows), byte(cols), 3, 1}
	var wide []byte
	for row := range rows {
		line := []rune(text[row])
		for col := range cols {
			char := ' '
			if col < len(line) {
				char = line[col]
			}
			glyph := byte(char)
			if char > 0xff {
				glyph = '?'
			}
			narrow = binary.NativeEndian.AppendUint16(narrow, uint16(glyph)|0x0700)
			wide = binary.NativeEndian.AppendUint32(wide, uint32(char))
		}
	}
	os.WriteFile(filepath.Join(dev, "vcsa"+tty), narrow, 0o644)
	if unicode {
		os.WriteFile(filepath.Join(dev, "vcsu"+tty), wide, 0o644)
	}
	return dev, sys
}

func TestActiveConsoleIsRead(t *testing.T) {
	dev, sys := writeConsole(t, "2", 3, 12, []string{"home login:", "", "한글 ok"}, true)
	screen, err := Read(dev, sys, 0)
	if err != nil {
		t.Fatal(err)
	}
	if screen.TTY != 2 || screen.Active != 2 || screen.Rows != 3 || screen.Cols != 12 {
		t.Errorf("screen = %+v", screen)
	}
	if screen.CursorCol != 3 || screen.CursorRow != 1 {
		t.Errorf("커서 = %d,%d", screen.CursorRow, screen.CursorCol)
	}
	if got := strings.Join(screen.Lines, "|"); got != "home login:||한글 ok" {
		t.Errorf("lines = %q", got)
	}
}

func TestConsoleWithoutUnicodeDeviceStillReads(t *testing.T) {
	dev, sys := writeConsole(t, "3", 1, 8, []string{"ok\x01 $"}, false)
	screen, err := Read(dev, sys, 3)
	if err != nil {
		t.Fatal(err)
	}
	// 화면에 찍을 수 없는 값은 빈칸으로 둔다.
	if screen.TTY != 3 || screen.Active != 2 || screen.Lines[0] != "ok  $" {
		t.Errorf("screen = %+v", screen)
	}
}

func TestConsoleFailuresAreExplained(t *testing.T) {
	dev, sys := writeConsole(t, "2", 1, 4, []string{"a"}, true)
	if _, err := Read(dev, sys, 5); err == nil || !strings.Contains(err.Error(), "tty5") {
		t.Errorf("없는 터미널: %v", err)
	}
	if _, err := Read(dev, sys, 64); err == nil {
		t.Error("범위를 벗어난 번호를 받았다")
	}
	os.WriteFile(filepath.Join(dev, "vcsa2"), []byte{25, 80, 0, 0, 'a'}, 0o644)
	if _, err := Read(dev, sys, 2); err == nil {
		t.Error("잘린 화면을 받아들였다")
	}
	if os.Getuid() != 0 {
		os.Chmod(filepath.Join(dev, "vcsa2"), 0)
		if _, err := Read(dev, sys, 2); err == nil || !strings.Contains(err.Error(), "권한") {
			t.Errorf("권한 없음: %v", err)
		}
	}
	// 켜져 있는 터미널을 모르면 지정한 터미널은 읽고, 지금 터미널은 읽지 못한다.
	if _, err := Read(dev, t.TempDir(), 0); err == nil {
		t.Error("켜져 있는 터미널을 모르는데 읽었다고 했다")
	}
}
