// Package screen은 데스크톱 세션 안에서 실행되어 서버 화면을 중앙 서버로 보낸다.
//
// 기본은 화면을 읽기만 한다. 보는 사람이 없으면 화면을 읽지 않는다.
// 키보드와 마우스 입력은 이 서버의 설정에서 조작을 허용하고, 보는 사람이 조작을 켠 동안에만 받는다.
package screen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ckdqja135/MSP/internal/agent"
	"github.com/ckdqja135/MSP/internal/protocol"
)

const (
	pollTimeout  = 40 * time.Second
	frameTimeout = 30 * time.Second
	maxBackoff   = 60 * time.Second
	minInterval  = 200 * time.Millisecond
	maxInterval  = 10 * time.Second
	inputRetry   = 2 * time.Second
)

type Config struct {
	CentralURL string
	ServerID   string
	Token      string
	// Control이 없으면 입력을 받지 않는다. 조작을 허용한 설정에서만 넣는다.
	Control func() (Controller, error)
}

type Helper struct {
	cfg      Config
	capturer Capturer
	client   *http.Client
}

// status는 화면 상태에 이 서버가 조작을 허용하는지를 붙인다.
func (h *Helper) status() protocol.ScreenStatus {
	status := h.capturer.Status()
	status.Control = h.cfg.Control != nil && status.State == protocol.ScreenReady
	return status
}

func New(cfg Config, capturer Capturer) (*Helper, error) {
	if err := agent.ValidateCentralURL(cfg.CentralURL); err != nil {
		return nil, err
	}
	if cfg.ServerID == "" || cfg.Token == "" {
		return nil, errors.New("서버 ID와 토큰이 필요합니다")
	}
	return &Helper{cfg: cfg, capturer: capturer, client: &http.Client{}}, nil
}

func (h *Helper) post(ctx context.Context, timeout time.Duration, path string, contentType string, body []byte) (protocol.ScreenDemand, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var demand protocol.ScreenDemand
	target := strings.TrimRight(h.cfg.CentralURL, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return demand, err
	}
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-MSP-Server", h.cfg.ServerID)
	request.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	response, err := h.client.Do(request)
	if err != nil {
		return demand, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return demand, fmt.Errorf("중앙 서버 응답 %d: %s", response.StatusCode, strings.TrimSpace(string(message)))
	}
	err = json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&demand)
	return demand, err
}

// poll은 화면을 보는 사람이 생길 때까지 기다린다. 도우미가 먼저 연결하므로 인바운드 포트가 필요 없다.
func (h *Helper) poll(ctx context.Context, status protocol.ScreenStatus) (protocol.ScreenDemand, error) {
	body, err := json.Marshal(status)
	if err != nil {
		return protocol.ScreenDemand{}, err
	}
	return h.post(ctx, pollTimeout, "/agent/v1/screen/poll", "application/json", body)
}

func (h *Helper) send(ctx context.Context, frame Frame) (protocol.ScreenDemand, error) {
	if frame.Unchanged {
		return h.post(ctx, frameTimeout, "/agent/v1/screen/frame?unchanged=1", "application/octet-stream", nil)
	}
	query := url.Values{"width": {strconv.Itoa(frame.Width)}, "height": {strconv.Itoa(frame.Height)}}
	return h.post(ctx, frameTimeout, "/agent/v1/screen/frame?"+query.Encode(), "image/jpeg", frame.JPEG)
}

// receiveInput은 중앙 서버가 흘려 주는 입력을 한 줄씩 받아 화면에 넣는다. 흐름이 끝나면 돌아온다.
func (h *Helper) receiveInput(ctx context.Context, controller Controller) error {
	target := strings.TrimRight(h.cfg.CentralURL, "/") + "/agent/v1/screen/input"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	request.Header.Set("X-MSP-Server", h.cfg.ServerID)
	request.Header.Set("Authorization", "Bearer "+h.cfg.Token)
	response, err := h.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("중앙 서버 응답 %d", response.StatusCode)
	}
	lines := bufio.NewScanner(response.Body)
	for lines.Scan() {
		// 빈 줄은 연결이 살아 있다는 표시다.
		if len(lines.Bytes()) == 0 {
			continue
		}
		var input protocol.ScreenInput
		if err := json.Unmarshal(lines.Bytes(), &input); err != nil {
			continue
		}
		if err := controller.Input(input); err != nil {
			return err
		}
	}
	return lines.Err()
}

// control은 보는 사람이 조작을 켠 동안 입력을 받는다. 끝나면 눌린 채 남은 키와 버튼을 뗀다.
func (h *Helper) control(ctx context.Context) {
	for ctx.Err() == nil {
		controller, err := h.cfg.Control()
		if err == nil {
			err = h.receiveInput(ctx, controller)
			controller.Close()
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("조작 입력을 받지 못했습니다: %v", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(inputRetry):
		}
	}
}

// watch는 보는 사람이 있는 동안 화면을 읽어 보낸다. 끝나면 다음에 알릴 상태를 돌려준다.
func (h *Helper) watch(ctx context.Context, demand protocol.ScreenDemand) protocol.ScreenStatus {
	defer h.capturer.Rest()
	// 조작은 보는 사람이 켠 동안만 받는다. 꺼지거나 떠나면 바로 멈춘다.
	var stopControl func()
	defer func() {
		if stopControl != nil {
			stopControl()
		}
	}()
	for demand.Capture && ctx.Err() == nil {
		switch wanted := demand.Control && h.cfg.Control != nil; {
		case wanted && stopControl == nil:
			controlCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			go func() {
				defer close(done)
				h.control(controlCtx)
			}()
			stopControl = func() {
				cancel()
				<-done
			}
		case !wanted && stopControl != nil:
			stopControl()
			stopControl = nil
		}

		started := time.Now()
		frame, err := h.capturer.Capture()
		if err != nil {
			log.Printf("화면을 읽지 못했습니다: %v", err)
			status := h.status()
			status.State, status.Detail, status.Control = protocol.ScreenError, err.Error(), false
			return status
		}
		interval := min(max(time.Duration(demand.IntervalMS)*time.Millisecond, minInterval), maxInterval)
		if demand, err = h.send(ctx, frame); err != nil {
			log.Printf("화면을 보내지 못했습니다: %v", err)
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(max(interval-time.Since(started), 0)):
		}
	}
	return h.status()
}

// Run은 ctx가 끝날 때까지 중앙 서버의 요청을 기다리고, 보는 사람이 있을 때만 화면을 보낸다.
func (h *Helper) Run(ctx context.Context) error {
	backoff := time.Second
	status := h.status()
	for ctx.Err() == nil {
		demand, err := h.poll(ctx, status)
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		backoff = time.Second
		status = h.status()
		if demand.Capture {
			status = h.watch(ctx, demand)
		}
	}
	return nil
}
