package screen

import (
	"encoding/binary"
	"strconv"
	"strings"
)

const (
	xauthFamilyLocal = 256
	xauthFamilyWild  = 65535
	xauthCookieName  = "MIT-MAGIC-COOKIE-1"
)

type xauthEntry struct {
	family  uint16
	address string
	number  string
	name    string
	data    []byte
}

// parseXauthority는 Xauthority 파일을 읽는다. 항목은 2바이트 종류와 길이가 붙은 네 필드로 이어진다.
// 잘린 파일은 읽을 수 있는 데까지만 돌려준다.
func parseXauthority(body []byte) []xauthEntry {
	var entries []xauthEntry
	for len(body) >= 2 {
		entry := xauthEntry{family: binary.BigEndian.Uint16(body)}
		body = body[2:]
		fields := make([][]byte, 4)
		for i := range fields {
			if len(body) < 2 {
				return entries
			}
			size := int(binary.BigEndian.Uint16(body))
			if len(body) < 2+size {
				return entries
			}
			fields[i], body = body[2:2+size], body[2+size:]
		}
		entry.address, entry.number, entry.name, entry.data = string(fields[0]), string(fields[1]), string(fields[2]), fields[3]
		entries = append(entries, entry)
	}
	return entries
}

// pickCookie는 이 디스플레이에 쓸 쿠키를 고른다. 호스트 이름까지 맞는 항목을 먼저 쓰고,
// 없으면 디스플레이 번호만 맞는 항목을 쓴다. 호스트 이름을 바꾼 뒤에도 접속할 수 있게 하기 위해서다.
func pickCookie(entries []xauthEntry, hostname string, display int) []byte {
	number := strconv.Itoa(display)
	var fallback []byte
	for _, entry := range entries {
		if entry.name != xauthCookieName || (entry.number != "" && entry.number != number) {
			continue
		}
		if entry.family == xauthFamilyWild || (entry.family == xauthFamilyLocal && entry.address == hostname) {
			return entry.data
		}
		if fallback == nil {
			fallback = entry.data
		}
	}
	return fallback
}

// parseDisplay는 DISPLAY 값에서 디스플레이 번호와 스크린 번호를 읽는다. 다른 장비의 디스플레이는 받지 않는다.
func parseDisplay(value string) (display, screen int, ok bool) {
	host, rest, found := strings.Cut(value, ":")
	if !found || (host != "" && host != "unix") {
		return 0, 0, false
	}
	number, screenText, hasScreen := strings.Cut(rest, ".")
	display, err := strconv.Atoi(number)
	if err != nil || display < 0 {
		return 0, 0, false
	}
	if hasScreen {
		if screen, err = strconv.Atoi(screenText); err != nil || screen < 0 {
			return 0, 0, false
		}
	}
	return display, screen, true
}
