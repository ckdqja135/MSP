// msp-agent는 대상 서버의 상태를 읽어 중앙 서버로 보낸다. 서버를 변경하지 않는다.
package main

import (
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

	"github.com/ckdqja135/MSP/internal/agent"
	"github.com/ckdqja135/MSP/internal/agent/dockerapi"
)

type fileConfig struct {
	CentralURL   string                      `json:"central_url"`
	ServerID     string                      `json:"server_id"`
	TokenFile    string                      `json:"token_file"`
	DockerSocket string                      `json:"docker_socket"`
	SpoolDir     string                      `json:"spool_dir"`
	SpoolMaxMB   int64                       `json:"spool_max_mb"`
	DiskPath     string                      `json:"disk_path"`
	Probes       []agent.ProbeConfig         `json:"probes"`
	HostServices []agent.DeclaredHostService `json:"host_services"`
}

func loadToken(config fileConfig) (string, error) {
	if token := os.Getenv("MSP_AGENT_TOKEN"); token != "" {
		return token, nil
	}
	if config.TokenFile == "" {
		return "", errors.New("MSP_AGENT_TOKEN 또는 token_file이 필요합니다")
	}
	body, err := os.ReadFile(config.TokenFile)
	if err != nil {
		return "", fmt.Errorf("토큰 파일을 읽지 못했습니다: %w", err)
	}
	return strings.TrimSpace(string(body)), nil
}

func run() error {
	configPath := flag.String("config", "agent.json", "설정 파일 경로")
	flag.Parse()

	body, err := os.ReadFile(*configPath)
	if err != nil {
		return fmt.Errorf("설정 파일을 읽지 못했습니다: %w", err)
	}
	var config fileConfig
	if err := json.Unmarshal(body, &config); err != nil {
		return fmt.Errorf("설정 파일 형식이 잘못되었습니다: %w", err)
	}
	if config.DockerSocket == "" {
		config.DockerSocket = "/var/run/docker.sock"
	}
	if config.SpoolDir == "" {
		config.SpoolDir = "data/agent-spool"
	}
	if config.SpoolMaxMB <= 0 {
		config.SpoolMaxMB = 200
	}
	if config.DiskPath == "" {
		config.DiskPath = "/"
	}
	token, err := loadToken(config)
	if err != nil {
		return err
	}

	instance, err := agent.New(agent.Config{
		CentralURL:   config.CentralURL,
		ServerID:     config.ServerID,
		Token:        token,
		SpoolDir:     config.SpoolDir,
		SpoolBytes:   config.SpoolMaxMB << 20,
		Probes:       config.Probes,
		HostServices: config.HostServices,
	}, dockerapi.New(config.DockerSocket), agent.SystemHost{DiskPath: config.DiskPath})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Printf("에이전트 시작: 서버 %s, 중앙 %s", config.ServerID, config.CentralURL)
	return instance.Run(ctx)
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
