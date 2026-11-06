package hostports

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
)

const tcpHeader = "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeProc(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, "net", "tcp"), tcpHeader+
		"   0: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1001 1 0 100 0 0 10 0\n"+
		"   1: 0100007F:0277 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1002 1 0 100 0 0 10 0\n"+
		"   2: 0A00A8C0:C350 0100007F:0050 01 00000000:00000000 00:00000000 00000000  1000        0 1003 1 0 100 0 0 10 0\n")
	write(t, filepath.Join(root, "net", "tcp6"), tcpHeader+
		"   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1004 1 0 100 0 0 10 0\n"+
		"   1: 00000000000000000000000001000000:0277 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1005 1 0 100 0 0 10 0\n")
	write(t, filepath.Join(root, "1234", "comm"), "nginx\n")
	if err := os.MkdirAll(filepath.Join(root, "1234", "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("socket:[1001]", filepath.Join(root, "1234", "fd", "6")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/dev/null", filepath.Join(root, "1234", "fd", "0")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "5678", "comm"), "sshd\n")
	return root
}

func TestScanReadsOnlyListeningSockets(t *testing.T) {
	listeners, err := Scan(fakeProc(t))
	if err != nil {
		t.Fatal(err)
	}
	want := []Listener{
		{IP: "0.0.0.0", Port: 80, Process: "nginx"},
		{IP: "::", Port: 443},
		{IP: "127.0.0.1", Port: 631},
		{IP: "::1", Port: 631},
	}
	if len(listeners) != len(want) {
		t.Fatalf("listeners = %+v", listeners)
	}
	for i := range want {
		if listeners[i] != want[i] {
			t.Errorf("listeners[%d] = %+v, want %+v", i, listeners[i], want[i])
		}
	}
}

func TestUnknownOwnerIsLeftEmpty(t *testing.T) {
	listeners, err := Scan(fakeProc(t))
	if err != nil {
		t.Fatal(err)
	}
	if listeners[1].Process != "" {
		t.Errorf("주인을 꾸며냈다: %+v", listeners[1])
	}
}

func TestMissingProcIsUnsupported(t *testing.T) {
	if _, err := Scan(t.TempDir()); !errors.Is(err, ErrUnsupported) {
		t.Errorf("err = %v", err)
	}
}

func TestMissingIPv6FileIsFine(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "net", "tcp"), tcpHeader)
	if _, err := Scan(root); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestRunningNames(t *testing.T) {
	names := RunningNames(fakeProc(t))
	if !names["nginx"] || !names["sshd"] || len(names) != 2 {
		t.Errorf("names = %v", names)
	}
}

func TestRunningNamesExcludeContainerProcesses(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "10", "comm"), "nginx\n")
	write(t, filepath.Join(root, "10", "cgroup"), "0::/system.slice/nginx.service\n")
	write(t, filepath.Join(root, "20", "comm"), "apache2\n")
	write(t, filepath.Join(root, "20", "cgroup"), "0::/system.slice/docker-abc123.scope\n")
	write(t, filepath.Join(root, "30", "comm"), "mysqld\n")
	write(t, filepath.Join(root, "30", "cgroup"), "12:memory:/docker/abc123\n")
	names := RunningNames(root)
	if !names["nginx"] || names["apache2"] || names["mysqld"] {
		t.Errorf("컨테이너 프로세스를 호스트 프로세스로 봤다: %v", names)
	}
}

func TestMalformedLinesAreSkipped(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "net", "tcp"), tcpHeader+"garbage line\n   0: ZZZ:0050 0:0 0A 0 0 0 0 0 1 0\n")
	listeners, err := Scan(root)
	if err != nil || len(listeners) != 0 {
		t.Errorf("listeners = %+v err = %v", listeners, err)
	}
}

// 실제 /proc가 있는 Linux에서만 돈다. 자기 프로세스의 루프백 포트만 열어 다른 것에 영향을 주지 않는다.
func TestScanFindsOwnListenerOnRealProc(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("/proc/net/tcp가 없는 환경")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	listeners, err := Scan("/proc")
	if err != nil {
		t.Fatal(err)
	}
	for _, found := range listeners {
		if found.Port == port {
			if found.IP != "127.0.0.1" || found.Process == "" {
				t.Errorf("found = %+v", found)
			}
			t.Logf("찾음: %+v, 전체 %d개", found, len(listeners))
			running := RunningNames("/proc")
			for _, name := range []string{"nginx", "apache2", "mysqld", "mariadbd", "sshd"} {
				t.Logf("호스트 프로세스 %s: %v", name, running[name])
			}
			return
		}
	}
	t.Errorf("포트 %d를 찾지 못했다: %+v", port, listeners)
}
