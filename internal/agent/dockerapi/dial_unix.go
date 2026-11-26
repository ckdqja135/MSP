//go:build !windows

package dockerapi

import (
	"context"
	"net"
)

// dialEndpoint는 유닉스 소켓으로 Docker에 접속한다.
func dialEndpoint(ctx context.Context, endpoint string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", endpoint)
}

const defaultEndpoint = "/var/run/docker.sock"

// endpointCandidates는 Docker가 소켓을 두는 자리다. 데스크톱용 Docker는 사용자 폴더 아래에 둔다.
func endpointCandidates(env func(string) string, home string) []string {
	candidates := []string{defaultEndpoint}
	if runtime := env("XDG_RUNTIME_DIR"); runtime != "" {
		candidates = append(candidates, runtime+"/docker.sock")
	}
	if home != "" {
		for _, path := range []string{".docker/run/docker.sock", ".docker/desktop/docker.sock", ".orbstack/run/docker.sock", ".colima/default/docker.sock", ".rd/docker.sock"} {
			candidates = append(candidates, home+"/"+path)
		}
	}
	return candidates
}
