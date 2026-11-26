package hostports

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

const scanTimeout = 5 * time.Second

// ScanSystem은 /proc이 없는 운영체제에서 열린 포트와 주인을 읽는다.
// 운영체제가 보여 주는 만큼만 돌려준다. 다른 계정의 프로세스는 빠지거나 이름이 비어 있을 수 있다.
func ScanSystem(ctx context.Context) ([]Listener, error) {
	// 목록을 얻는 쪽이 멈춰도 수집 전체를 붙잡지 않게 한다.
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	connections, err := net.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil, err
	}
	names := map[int32]string{}
	seen := map[Listener]bool{}
	var listeners []Listener
	for _, connection := range connections {
		if connection.Status != "LISTEN" {
			continue
		}
		name, known := names[connection.Pid]
		if !known && connection.Pid != 0 {
			if owner, err := process.NewProcessWithContext(ctx, connection.Pid); err == nil {
				name, _ = owner.NameWithContext(ctx)
			}
			names[connection.Pid] = name
		}
		listener := Listener{
			IP: listenAddress(connection.Laddr.IP, connection.Family), Port: int(connection.Laddr.Port),
			Process: programName(name), PID: int(connection.Pid),
		}
		// 한 프로세스가 같은 소켓을 여러 번 쥐고 있으면 같은 줄이 되풀이된다.
		if !seen[listener] {
			seen[listener] = true
			listeners = append(listeners, listener)
		}
	}
	sortListeners(listeners)
	return listeners, nil
}

// listenAddress는 모든 주소를 뜻하는 표기를 리눅스와 같게 맞춘다.
func listenAddress(ip string, family uint32) string {
	if ip != "*" && ip != "" {
		return ip
	}
	if family == syscall.AF_INET {
		return "0.0.0.0"
	}
	return "::"
}

// programName은 실행 파일 이름에서 확장자를 뗀다. Windows는 이름에 .exe가 붙는다.
func programName(name string) string {
	if strings.EqualFold(filepath.Ext(name), ".exe") {
		return name[:len(name)-len(".exe")]
	}
	return name
}
