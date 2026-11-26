// msp-central는 에이전트 데이터를 받아 저장하고 대시보드를 제공한다.
// --local을 주면 수집기까지 함께 띄워 설치 없이 이 컴퓨터를 보여준다.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/ckdqja135/MSP/internal/agent"
	"github.com/ckdqja135/MSP/internal/agent/dockerapi"
	"github.com/ckdqja135/MSP/internal/central"
	"github.com/ckdqja135/MSP/internal/demo"
	"github.com/ckdqja135/MSP/internal/protocol"
)

func main() {
	var err error
	if len(os.Args) > 1 && os.Args[1] == "enroll" {
		err = enroll(os.Args[2:])
	} else {
		err = serve(os.Args[1:])
	}
	if err != nil {
		log.Fatal(err)
	}
}

// enroll은 서버를 등록하고 토큰을 한 번만 출력한다. DB에는 해시만 저장한다.
func enroll(args []string) error {
	flags := flag.NewFlagSet("enroll", flag.ExitOnError)
	dbPath := flags.String("db", "data/msp.db", "DB 파일 경로")
	serverID := flags.String("server", "", "서버 ID")
	screen := flags.Bool("screen", false, "에이전트 대신 화면 도우미의 토큰을 등록")
	flags.Parse(args)
	if *serverID == "" {
		return errors.New("--server가 필요합니다")
	}
	store, err := central.OpenStore(*dbPath, protocol.ModeLive)
	if err != nil {
		return err
	}
	defer store.Close()

	token, err := newToken()
	if err != nil {
		return err
	}
	register := store.Enroll
	if *screen {
		register = store.EnrollScreen
	}
	if err := register(context.Background(), *serverID, token); err != nil {
		return err
	}
	fmt.Println(token)
	return nil
}

func newToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// fileConfig는 선택 설정이다. 웹훅 주소에 비밀값이 들어가므로 저장소에 올리지 않는다.
type fileConfig struct {
	PublicURL        string                  `json:"public_url"`
	AlertWebhookURL  string                  `json:"alert_webhook_url"`
	HeartbeatURL     string                  `json:"heartbeat_url"`
	ConnectivityURLs []string                `json:"connectivity_urls"`
	ExternalProbes   []central.ExternalProbe `json:"external_probes"`
	SameHostServers  []string                `json:"same_host_servers"`
}

func loadConfig(path string) (fileConfig, error) {
	var config fileConfig
	if path == "" {
		return config, nil
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("설정 파일을 읽지 못했습니다: %w", err)
	}
	if err := json.Unmarshal(body, &config); err != nil {
		return config, fmt.Errorf("설정 파일 형식이 잘못되었습니다: %w", err)
	}
	return config, nil
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := flags.String("config", "", "선택 설정 파일 경로")
	listen := flags.String("listen", "127.0.0.1:8787", "접속 주소")
	dbPath := flags.String("db", "", "DB 파일 경로 (기본: data/msp.db, 데모: data/demo.db)")
	webDir := flags.String("web-dir", "web/dist", "웹 화면 빌드 경로")
	demoMode := flags.Bool("demo", false, "가짜 데이터로 실행")
	localMode := flags.Bool("local", false, "설치와 설정 없이 이 컴퓨터를 바로 본다")
	flags.Parse(args)
	if *demoMode && *localMode {
		return errors.New("--demo와 --local은 함께 쓸 수 없습니다")
	}

	password := os.Getenv("MSP_DASHBOARD_PASSWORD")
	if err := central.ValidateListen(*listen, password); err != nil {
		return err
	}

	mode := protocol.ModeLive
	path := "data/msp.db"
	if *demoMode {
		mode = protocol.ModeDemo
		path = "data/demo.db"
	}
	if *localMode {
		// 서버에서 받은 기록과 섞이지 않게 따로 둔다.
		path = "data/local.db"
	}
	if *dbPath != "" {
		path = *dbPath
	}
	store, err := central.OpenStore(path, mode)
	if err != nil {
		return err
	}
	defer store.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	config := central.Config{Listen: *listen, WebDir: *webDir, DashboardPassword: password}
	var scenario *demo.Scenario
	if *demoMode {
		scenario = demo.New(time.Now().UTC())
		config.LogStreamer = scenario.StreamLogs
	}
	app := central.NewServer(store, central.NewHub(), config)
	if *demoMode {
		if err := startDemo(ctx, store, app, scenario); err != nil {
			return err
		}
	}
	go purgeLoop(ctx, store)

	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if err := central.ValidateExternalProbes(settings.ExternalProbes, settings.SameHostServers); err != nil {
		return err
	}
	if *localMode {
		settings.SameHostServers = append(settings.SameHostServers, localServerID)
	}
	store.SetSameHost(settings.SameHostServers)
	// 데모는 외부로 아무것도 보내지 않는다.
	var notifier central.Notifier
	if !*demoMode && settings.AlertWebhookURL != "" {
		notifier = central.WebhookNotifier{URL: settings.AlertWebhookURL, Attempts: 3, Backoff: 2 * time.Second}
	}
	startedAt := time.Now().UTC()
	if *demoMode {
		// 데모는 처음부터 데이터가 있으므로 판정을 미루지 않는다.
		startedAt = startedAt.Add(-time.Hour)
	}
	engine := central.NewAlertEngine(store, notifier, app.Hub(), settings.PublicURL, startedAt)
	engine.SelfCheck = central.ConnectivityCheck(settings.ConnectivityURLs)
	go engine.Run(ctx, 10*time.Second)
	if !*demoMode {
		go app.RunExternalProbes(ctx, settings.ExternalProbes, 30*time.Second)
		go central.RunHeartbeat(ctx, settings.HeartbeatURL, time.Minute)
	}

	// 수집기가 붙기 전에 포트를 먼저 연다.
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Printf("중앙 서버 시작: http://%s (모드: %s, DB: %s)", *listen, mode, path)
	if *localMode {
		if err := startLocalAgent(ctx, store, listener.Addr(), filepath.Join(filepath.Dir(path), "local-spool")); err != nil {
			return err
		}
	}
	if err := srv.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// localServerID는 --local로 볼 때 이 컴퓨터를 가리키는 이름이다. 컴퓨터 이름은 쓰지 않는다.
const localServerID = "local"

// startLocalAgent는 같은 프로세스 안에서 수집기를 띄워 이 컴퓨터의 Docker와 열린 포트를 읽는다.
// 토큰은 실행할 때마다 새로 만들고 파일로 남기지 않는다.
func startLocalAgent(ctx context.Context, store *central.Store, address net.Addr, spoolDir string) error {
	// 수집기는 같은 컴퓨터 안의 주소로만 중앙 서버에 붙는다. 토큰이 평문으로 나가기 때문이다.
	bound := address.(*net.TCPAddr)
	host := bound.IP
	switch {
	case host.IsUnspecified() && host.To4() == nil:
		host = net.IPv6loopback
	case host.IsUnspecified():
		host = net.IPv4(127, 0, 0, 1)
	case !host.IsLoopback():
		return fmt.Errorf("--local은 이 컴퓨터 안에서 닿는 주소로 열어야 합니다: %s", bound)
	}
	target := "http://" + net.JoinHostPort(host.String(), strconv.Itoa(bound.Port))

	token, err := newToken()
	if err != nil {
		return err
	}
	if err := store.Enroll(ctx, localServerID, token); err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	instance, err := agent.New(agent.Config{
		CentralURL: target, ServerID: localServerID, Token: token,
		SpoolDir: spoolDir, SpoolBytes: 50 << 20, Local: true,
	}, dockerapi.NewLocal(os.Getenv, home), agent.SystemHost{DiskPath: localDiskPath()})
	if err != nil {
		return err
	}
	log.Printf("이 컴퓨터를 봅니다. 화면: %s", target)
	go func() {
		if err := instance.Run(ctx); err != nil {
			log.Printf("수집 중단: %v", err)
		}
	}()
	return nil
}

// localDiskPath는 용량을 볼 디스크다. Windows는 시스템 드라이브를 본다.
func localDiskPath() string {
	if drive := os.Getenv("SystemDrive"); runtime.GOOS == "windows" && drive != "" {
		return drive + `\`
	}
	return "/"
}

func startDemo(ctx context.Context, store *central.Store, app *central.Server, scenario *demo.Scenario) error {
	if err := store.ResetDemo(ctx); err != nil {
		return err
	}
	cursor, err := scenario.Seed(ctx, time.Now().UTC(), func(ctx context.Context, batch protocol.Batch, receivedAt time.Time) error {
		_, err := store.Ingest(ctx, batch, receivedAt)
		return err
	})
	if err != nil {
		return err
	}
	go func() {
		err := scenario.Run(ctx, cursor, func(ctx context.Context, batch protocol.Batch) error {
			_, err := app.Ingest(ctx, batch)
			return err
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("데모 중단: %v", err)
		}
	}()
	return nil
}

func purgeLoop(ctx context.Context, store *central.Store) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := store.Purge(ctx, now.UTC()); err != nil {
				log.Printf("보관 기간 정리 실패: %v", err)
			}
		}
	}
}
