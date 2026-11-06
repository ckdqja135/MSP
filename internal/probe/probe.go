// Package probe는 서비스 응답을 한 번 확인한다. 에이전트와 중앙 서버가 함께 쓴다.
package probe

import (
	"context"
	"errors"
	"net"
	"net/http"
	"syscall"
	"time"
)

type Result struct {
	OK      bool
	Failure string
	// LatencyMS는 응답을 받은 경우에만 값이 있다. 응답이 없으면 측정한 시간이 없다.
	LatencyMS *int64
}

// NewClient는 리다이렉트를 따라가지 않는 클라이언트를 만든다. 지정한 주소만 확인하기 위해서다.
func NewClient() *http.Client {
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func Run(ctx context.Context, client *http.Client, target string, timeout time.Duration) Result {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target, nil)
	if err != nil {
		return Result{Failure: "other"}
	}
	started := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return Result{Failure: classify(err)}
	}
	response.Body.Close()
	latency := time.Since(started).Milliseconds()
	if response.StatusCode >= http.StatusBadRequest {
		return Result{Failure: "status", LatencyMS: &latency}
	}
	return Result{OK: true, LatencyMS: &latency}
}

func classify(err error) string {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		return "timeout"
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return "refused"
	}
	return "other"
}
