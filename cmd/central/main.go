// msp-central는 에이전트 데이터를 받아 저장하고 대시보드를 제공한다.
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
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	flags.Parse(args)
	if *serverID == "" {
		return errors.New("--server가 필요합니다")
	}
	store, err := central.OpenStore(*dbPath, protocol.ModeLive)
	if err != nil {
		return err
	}
	defer store.Close()

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)
	if err := store.Enroll(context.Background(), *serverID, token); err != nil {
		return err
	}
	fmt.Println(token)
	return nil
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
	flags.Parse(args)

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

	srv := &http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()

	log.Printf("중앙 서버 시작: http://%s (모드: %s, DB: %s)", *listen, mode, path)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
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
