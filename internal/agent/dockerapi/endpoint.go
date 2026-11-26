package dockerapi

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"strings"
)

// Endpoints는 이 컴퓨터에서 Docker가 접속을 받는 자리의 후보다. 설정 없이 실행할 때 쓴다.
// DOCKER_HOST가 소켓이나 파이프를 가리키면 그것만 쓰고, 아니면 알려진 자리를 차례로 본다.
// 네트워크 주소(tcp://)는 받지 않는다. 이 클라이언트는 같은 컴퓨터의 Docker만 읽는다.
func Endpoints(env func(string) string, home string) []string {
	host := env("DOCKER_HOST")
	if path, found := strings.CutPrefix(host, "unix://"); found && path != "" {
		return []string{path}
	}
	if path, found := strings.CutPrefix(host, "npipe://"); found && path != "" {
		return []string{strings.ReplaceAll(path, "/", `\`)}
	}
	return endpointCandidates(env, home)
}

// NewLocal은 접속할 때마다 후보 가운데 받아 주는 자리를 찾는 클라이언트를 만든다.
// 실행한 뒤에 켠 Docker도 잡고, 꺼진 Docker가 남긴 소켓에 붙들리지 않는다.
func NewLocal(env func(string) string, home string) *Client {
	return newSearching(func() []string { return Endpoints(env, home) })
}

func newSearching(endpoints func() []string) *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var failure error
		for _, endpoint := range endpoints() {
			conn, err := dialEndpoint(ctx, endpoint)
			if err == nil {
				return conn, nil
			}
			// 자리가 없다는 오류보다 자리는 있는데 받지 않는다는 오류가 상황을 더 잘 알려 준다.
			if failure == nil || (Missing(failure) && !Missing(err)) {
				failure = err
			}
		}
		return nil, failure
	}
	return newClient("http://docker", &http.Transport{DialContext: dial})
}

// Missing은 접속할 소켓이나 파이프가 아예 없는 경우다. Docker가 설치되어 있지 않거나 꺼져 있다는 뜻이다.
func Missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist)
}
