//go:build !windows

package dockerapi

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shortTempDir은 이름이 짧은 임시 폴더를 만든다. 시험 이름이 붙은 긴 경로에는 유닉스 소켓을 만들 수 없다.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "msp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestEndpointsFollowDockerHostOrKnownPlaces(t *testing.T) {
	home := shortTempDir(t)
	if got := Endpoints(envOf(map[string]string{"DOCKER_HOST": "unix:///custom/docker.sock"}), home); len(got) != 1 || got[0] != "/custom/docker.sock" {
		t.Errorf("DOCKER_HOST = %q", got)
	}
	if got := Endpoints(envOf(map[string]string{"DOCKER_HOST": "npipe:////./pipe/docker_engine"}), home); len(got) != 1 || got[0] != `\\.\pipe\docker_engine` {
		t.Errorf("파이프 = %q", got)
	}
	// 다른 컴퓨터의 Docker는 읽지 않는다. 알려진 자리만 본다.
	candidates := Endpoints(envOf(map[string]string{"DOCKER_HOST": "tcp://192.0.2.10:2375"}), home)
	if len(candidates) < 2 || candidates[0] != defaultEndpoint {
		t.Errorf("후보 = %q", candidates)
	}
	for _, candidate := range candidates {
		if strings.Contains(candidate, "192.0.2.10") {
			t.Errorf("네트워크 주소를 받았다: %q", candidate)
		}
	}
}

// serveDocker는 주어진 자리에서 Docker처럼 응답한다.
func serveDocker(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("OK")) })}
	go server.Serve(listener)
	t.Cleanup(func() { server.Close() })
}

func TestLocalClientFindsDockerStartedLater(t *testing.T) {
	home := shortTempDir(t)
	first := filepath.Join(home, "first.sock")
	second := filepath.Join(home, "second.sock")
	client := newSearching(func() []string { return []string{first, second} })
	ctx := context.Background()
	if err := client.Ping(ctx); err == nil || !Missing(err) {
		t.Fatalf("아무 자리도 없을 때: %v", err)
	}

	// 꺼진 Docker가 소켓만 남긴 자리는 건너뛰고, 받아 주는 자리를 쓴다.
	dead, err := net.Listen("unix", first)
	if err != nil {
		t.Fatal(err)
	}
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	dead.Close()
	if err := client.Ping(ctx); err == nil || Missing(err) {
		t.Errorf("자리는 있는데 받지 않는 경우를 없는 것으로 봤다: %v", err)
	}
	// 실행한 뒤에 켠 Docker도 다음 접속에서 잡는다.
	serveDocker(t, second)
	if err := client.Ping(ctx); err != nil {
		t.Errorf("나중에 켠 Docker를 찾지 못했다: %v", err)
	}
}

func TestNewLocalUsesDockerHost(t *testing.T) {
	home := shortTempDir(t)
	path := filepath.Join(home, "docker.sock")
	serveDocker(t, path)
	if err := NewLocal(envOf(map[string]string{"DOCKER_HOST": "unix://" + path}), home).Ping(context.Background()); err != nil {
		t.Errorf("DOCKER_HOST의 Docker에 접속하지 못했다: %v", err)
	}
}

func TestMissingSocketIsNotTreatedAsOutage(t *testing.T) {
	home := shortTempDir(t)
	if err := New(filepath.Join(home, "none.sock")).Ping(context.Background()); err == nil || !Missing(err) {
		t.Errorf("소켓이 없는 경우를 구분하지 못했다: %v", err)
	}

	// 소켓은 있는데 받는 쪽이 없으면 Docker가 멈춘 것이다.
	path := filepath.Join(home, "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	listener.Close()
	if err := New(path).Ping(context.Background()); err == nil || Missing(err) {
		t.Errorf("멈춘 Docker를 없는 것으로 봤다: %v", err)
	}
}
