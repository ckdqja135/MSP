// Package hostports는 호스트에서 열린 TCP 포트를 /proc에서 읽는다. 읽기만 하며 명령을 실행하지 않는다.
//
// 소켓 주인은 같은 계정의 프로세스만 알 수 있다. root 소유 프로세스는 Process가 비어 있다.
package hostports

import (
	"encoding/hex"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const stateListen = "0A"

type Listener struct {
	IP      string
	Port    int
	Process string
}

// ErrUnsupported는 /proc/net/tcp가 없는 환경이다.
var ErrUnsupported = errors.New("이 환경에서는 호스트 포트를 읽을 수 없습니다")

// parseAddress는 /proc/net/tcp의 주소를 해석한다. 4바이트 단위로 바이트 순서가 뒤집혀 있다.
func parseAddress(field string) (string, int, bool) {
	address, portText, found := strings.Cut(field, ":")
	if !found {
		return "", 0, false
	}
	port, err := strconv.ParseUint(portText, 16, 16)
	if err != nil {
		return "", 0, false
	}
	raw, err := hex.DecodeString(address)
	if err != nil || (len(raw) != net.IPv4len && len(raw) != net.IPv6len) {
		return "", 0, false
	}
	ordered := make([]byte, len(raw))
	for i := 0; i < len(raw); i += 4 {
		ordered[i], ordered[i+1], ordered[i+2], ordered[i+3] = raw[i+3], raw[i+2], raw[i+1], raw[i]
	}
	return net.IP(ordered).String(), int(port), true
}

type socket struct {
	ip    string
	port  int
	inode string
}

func readSockets(path string) ([]socket, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var sockets []socket
	for _, line := range strings.Split(string(body), "\n")[1:] {
		fields := strings.Fields(line)
		if len(fields) < 10 || fields[3] != stateListen {
			continue
		}
		ip, port, ok := parseAddress(fields[1])
		if !ok {
			continue
		}
		sockets = append(sockets, socket{ip: ip, port: port, inode: fields[9]})
	}
	return sockets, nil
}

func numericEntries(root string) []string {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err == nil {
			names = append(names, entry.Name())
		}
	}
	return names
}

func readComm(root, pid string) string {
	body, err := os.ReadFile(filepath.Join(root, pid, "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(body))
}

// ownersOf는 읽을 수 있는 프로세스의 fd에서 소켓 주인을 찾는다. 권한이 없는 프로세스는 건너뛴다.
func ownersOf(root string, wanted map[string]bool) map[string]string {
	owners := map[string]string{}
	for _, pid := range numericEntries(root) {
		fdDir := filepath.Join(root, pid, "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil || !strings.HasPrefix(target, "socket:[") {
				continue
			}
			inode := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
			if wanted[inode] && owners[inode] == "" {
				owners[inode] = readComm(root, pid)
			}
		}
	}
	return owners
}

// Scan은 열린 포트와 알아낼 수 있는 주인을 돌려준다.
func Scan(root string) ([]Listener, error) {
	v4, err := readSockets(filepath.Join(root, "net", "tcp"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrUnsupported
		}
		return nil, err
	}
	// IPv6가 꺼진 서버에는 tcp6 파일이 없다.
	v6, err := readSockets(filepath.Join(root, "net", "tcp6"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	sockets := append(v4, v6...)
	wanted := make(map[string]bool, len(sockets))
	for _, s := range sockets {
		wanted[s.inode] = true
	}
	owners := ownersOf(root, wanted)
	listeners := make([]Listener, 0, len(sockets))
	for _, s := range sockets {
		listeners = append(listeners, Listener{IP: s.ip, Port: s.port, Process: owners[s.inode]})
	}
	sort.Slice(listeners, func(i, j int) bool {
		if listeners[i].Port != listeners[j].Port {
			return listeners[i].Port < listeners[j].Port
		}
		return listeners[i].IP < listeners[j].IP
	})
	return listeners, nil
}

// inContainer는 cgroup 경로로 컨테이너 안의 프로세스인지 판단한다.
// 호스트의 /proc에는 컨테이너 프로세스도 보이므로 구분하지 않으면 이름을 잘못 추정한다.
func inContainer(root, pid string) bool {
	body, err := os.ReadFile(filepath.Join(root, pid, "cgroup"))
	if err != nil {
		return false
	}
	text := string(body)
	for _, mark := range []string{"docker", "containerd", "kubepods", "libpod", "lxc"} {
		if strings.Contains(text, mark) {
			return true
		}
	}
	return false
}

// RunningNames는 호스트에서 직접 도는 프로세스 이름만 돌려준다. 명령줄 인자는 읽지 않는다.
func RunningNames(root string) map[string]bool {
	names := map[string]bool{}
	for _, pid := range numericEntries(root) {
		if inContainer(root, pid) {
			continue
		}
		if name := readComm(root, pid); name != "" {
			names[name] = true
		}
	}
	return names
}
