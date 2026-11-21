// msp-screen은 데스크톱 세션 안에서 서버 화면을 읽어 중앙 서버로 보낸다. 화면에 입력을 보내지 않는다.
package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/ckdqja135/MSP/internal/screen"
)

type fileConfig struct {
	CentralURL string `json:"central_url"`
	ServerID   string `json:"server_id"`
	TokenFile  string `json:"token_file"`
	// AllowControl이 참일 때만 화면보기에서 보낸 마우스와 키보드 입력을 받는다.
	AllowControl bool `json:"allow_control"`
}

func loadToken(config fileConfig) (string, error) {
	if token := os.Getenv("MSP_SCREEN_TOKEN"); token != "" {
		return token, nil
	}
	if config.TokenFile == "" {
		return "", errors.New("MSP_SCREEN_TOKEN 또는 token_file이 필요합니다")
	}
	body, err := os.ReadFile(config.TokenFile)
	if err != nil {
		return "", fmt.Errorf("토큰 파일을 읽지 못했습니다: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

// check는 화면을 한 번 읽어 보고 결과만 알린다. 중앙 서버로 아무것도 보내지 않는다.
func check(capturer screen.Capturer) error {
	status := capturer.Status()
	fmt.Printf("세션: %s, 상태: %s\n", cmp.Or(status.Session, "알 수 없음"), status.State)
	if status.Detail != "" {
		fmt.Println(status.Detail)
	}
	frame, err := capturer.Capture()
	if err != nil {
		return fmt.Errorf("화면을 읽지 못했습니다: %w", err)
	}
	fmt.Printf("화면을 읽었습니다: %dx%d, JPEG %dKB\n", frame.Width, frame.Height, len(frame.JPEG)/1024)
	return nil
}

func run() error {
	configPath := flag.String("config", "screen.json", "설정 파일 경로")
	checkOnly := flag.Bool("check", false, "화면을 한 번 읽어 보고 끝낸다. 중앙 서버로 보내지 않는다")
	flag.Parse()

	capturer := screen.NewCapturer(os.Getenv)
	if *checkOnly {
		return check(capturer)
	}

	body, err := os.ReadFile(*configPath)
	if err != nil {
		return fmt.Errorf("설정 파일을 읽지 못했습니다: %w", err)
	}
	var config fileConfig
	if err := json.Unmarshal(body, &config); err != nil {
		return fmt.Errorf("설정 파일 형식이 잘못되었습니다: %w", err)
	}
	token, err := loadToken(config)
	if err != nil {
		return err
	}
	settings := screen.Config{CentralURL: config.CentralURL, ServerID: config.ServerID, Token: token}
	if config.AllowControl {
		settings.Control = func() (screen.Controller, error) { return screen.NewController(os.Getenv) }
	}
	helper, err := screen.New(settings, capturer)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	status := capturer.Status()
	mode := "보기 전용"
	if config.AllowControl {
		mode = "원격 조작 허용"
	}
	log.Printf("화면 도우미 시작: 서버 %s, 중앙 %s, 세션 %s, %s", config.ServerID, config.CentralURL, cmp.Or(status.Session, "알 수 없음"), mode)
	return helper.Run(ctx)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
