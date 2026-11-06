// Package dockerapi는 Docker Engine API의 읽기 전용 클라이언트다.
//
// 공식 SDK 대신 직접 작성한 이유는 변경 API를 코드에 포함하지 않기 위해서다.
// 전송 계층에서도 GET과 허용 경로 외의 요청을 거부한다.
package dockerapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
)

// ErrNotAllowed는 허용 목록 밖의 Docker API 요청을 나타낸다.
var ErrNotAllowed = errors.New("dockerapi: request not allowed")

var allowedPaths = []*regexp.Regexp{
	regexp.MustCompile(`^/_ping$`),
	regexp.MustCompile(`^/containers/json$`),
	regexp.MustCompile(`^/containers/[0-9a-f]{12,64}/json$`),
	regexp.MustCompile(`^/containers/[0-9a-f]{12,64}/stats$`),
	regexp.MustCompile(`^/containers/[0-9a-f]{12,64}/logs$`),
	regexp.MustCompile(`^/networks$`),
	regexp.MustCompile(`^/events$`),
}

func allowed(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	for _, p := range allowedPaths {
		if p.MatchString(r.URL.Path) {
			return true
		}
	}
	return false
}

type readOnlyTransport struct {
	next http.RoundTripper
}

func (t readOnlyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !allowed(r) {
		return nil, fmt.Errorf("%w: %s %s", ErrNotAllowed, r.Method, r.URL.Path)
	}
	return t.next.RoundTrip(r)
}

type Client struct {
	http *http.Client
	base string
}

// New는 유닉스 소켓으로 Docker에 접속하는 클라이언트를 만든다.
func New(socketPath string) *Client {
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socketPath)
	}
	return newClient("http://docker", &http.Transport{DialContext: dial})
}

func newClient(base string, next http.RoundTripper) *Client {
	return &Client{
		base: base,
		http: &http.Client{
			Transport: readOnlyTransport{next: next},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *Client) open(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	target := c.base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, fmt.Errorf("dockerapi: GET %s: status %d", path, resp.StatusCode)
	}
	return resp, nil
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	resp, err := c.open(ctx, path, query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// Ping은 Docker 데몬 응답 여부를 확인한다.
func (c *Client) Ping(ctx context.Context) error {
	resp, err := c.open(ctx, "/_ping", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}
