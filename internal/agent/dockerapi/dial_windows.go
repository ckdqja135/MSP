//go:build windows

package dockerapi

import (
	"context"
	"net"

	"github.com/Microsoft/go-winio"
)

// dialEndpoint는 이름 있는 파이프로 Docker에 접속한다. Windows의 Docker는 유닉스 소켓 대신 파이프를 연다.
func dialEndpoint(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}

const defaultEndpoint = `\\.\pipe\docker_engine`

func endpointCandidates(func(string) string, string) []string {
	return []string{defaultEndpoint}
}
