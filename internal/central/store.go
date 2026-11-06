// Package central은 에이전트 데이터를 저장하고 화면에 필요한 뷰를 만든다.
package central

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ckdqja135/MSP/internal/protocol"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS servers (
  id TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL DEFAULT '',
  last_seen INTEGER NOT NULL DEFAULT 0,
  clock_skew_ms INTEGER NOT NULL DEFAULT 0,
  agent_version TEXT NOT NULL DEFAULT '',
  boot_time INTEGER NOT NULL DEFAULT 0,
  uptime_seconds INTEGER NOT NULL DEFAULT 0,
  docker_ok INTEGER NOT NULL DEFAULT 0,
  collectors TEXT NOT NULL DEFAULT '[]'
);
CREATE TABLE IF NOT EXISTS ingest_cursors (
  server_id TEXT NOT NULL, epoch TEXT NOT NULL, last_seq INTEGER NOT NULL,
  PRIMARY KEY (server_id, epoch)
);
CREATE TABLE IF NOT EXISTS topology_versions (
  id INTEGER PRIMARY KEY,
  server_id TEXT NOT NULL,
  hash TEXT NOT NULL,
  valid_from INTEGER NOT NULL,
  valid_to INTEGER,
  snapshot TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS topology_time ON topology_versions (server_id, valid_from);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY,
  server_id TEXT NOT NULL,
  occurred_at INTEGER NOT NULL,
  received_at INTEGER NOT NULL,
  category TEXT NOT NULL,
  type TEXT NOT NULL,
  severity TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  target TEXT NOT NULL DEFAULT '',
  service_key TEXT NOT NULL DEFAULT '',
  container_id TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL,
  duration_ms INTEGER,
  duration_limit_ms INTEGER,
  bytes INTEGER,
  result TEXT NOT NULL DEFAULT '',
  evidence TEXT NOT NULL,
  aggregated INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS events_time ON events (server_id, occurred_at);
CREATE INDEX IF NOT EXISTS events_service ON events (server_id, service_key, occurred_at);
CREATE TABLE IF NOT EXISTS samples (
  server_id TEXT NOT NULL, scope TEXT NOT NULL, name TEXT NOT NULL, at INTEGER NOT NULL, value REAL NOT NULL,
  PRIMARY KEY (server_id, scope, name, at)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS samples_time ON samples (server_id, at);
CREATE TABLE IF NOT EXISTS gaps (
  id INTEGER PRIMARY KEY,
  server_id TEXT NOT NULL,
  source TEXT NOT NULL,
  from_at INTEGER NOT NULL,
  to_at INTEGER NOT NULL,
  reason TEXT NOT NULL,
  dropped INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS gaps_time ON gaps (server_id, from_at);
CREATE TABLE IF NOT EXISTS probe_states (
  server_id TEXT NOT NULL,
  service_key TEXT NOT NULL,
  vantage TEXT NOT NULL,
  target TEXT NOT NULL,
  state TEXT NOT NULL,
  consecutive_failures INTEGER NOT NULL,
  consecutive_successes INTEGER NOT NULL,
  last_checked INTEGER NOT NULL,
  last_latency_ms INTEGER,
  last_failure TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (server_id, service_key, vantage)
);
`

const (
	sampleRetention = 7 * 24 * time.Hour
	recordRetention = 90 * 24 * time.Hour
)

// Store는 쓰기를 한 번에 하나만 수행한다. SQLite의 단일 쓰기 제약을 잠금 대기 오류 없이 지키기 위해서다.
type Store struct {
	db      *sql.DB
	mode    protocol.Mode
	writeMu sync.Mutex
	// sameHost는 중앙 서버와 같은 장비에서 도는 서버다. 수신이 끊겨도 장비가 멈춘 것으로 보지 않는다.
	sameHost map[string]bool
}

// OpenStore는 DB를 연다. 다른 모드로 만들어진 DB는 거부해 실제 데이터와 데모 데이터가 섞이지 않게 한다.
func OpenStore(path string, mode protocol.Mode) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	file.Close()

	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema + alertSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("스키마 적용 실패: %w", err)
	}

	var stored string
	err = db.QueryRow(`SELECT value FROM meta WHERE key = 'mode'`).Scan(&stored)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		_, err = db.Exec(`INSERT INTO meta (key, value) VALUES ('mode', ?)`, string(mode))
	case err == nil && stored != string(mode):
		err = fmt.Errorf("이 DB는 %s 모드로 만들어졌습니다. %s 모드로 열 수 없습니다", stored, mode)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, mode: mode}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Mode() protocol.Mode { return s.mode }

// ResetDemo는 데모 DB의 내용만 비운다. 실제 DB에서는 동작하지 않는다.
func (s *Store) ResetDemo(ctx context.Context) error {
	if s.mode != protocol.ModeDemo {
		return errors.New("데모 모드에서만 초기화할 수 있습니다")
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	for _, table := range []string{"servers", "ingest_cursors", "topology_versions", "events", "samples", "gaps", "probe_states", "alerts", "notification_deliveries"} {
		if _, err := s.db.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return err
		}
	}
	return nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Enroll은 서버를 등록하고 토큰 해시만 저장한다. 토큰 원문은 호출자에게 한 번만 전달된다.
func (s *Store) Enroll(ctx context.Context, serverID, token string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO servers (id, token_hash) VALUES (?, ?)
		ON CONFLICT (id) DO UPDATE SET token_hash = excluded.token_hash`, serverID, hashToken(token))
	return err
}

// Authenticate는 서버의 토큰을 상수 시간으로 비교한다.
func (s *Store) Authenticate(ctx context.Context, serverID, token string) bool {
	var stored string
	err := s.db.QueryRowContext(ctx, `SELECT token_hash FROM servers WHERE id = ?`, serverID).Scan(&stored)
	if err != nil || stored == "" {
		// 없는 서버도 같은 비교를 거쳐 응답 시간으로 존재 여부가 드러나지 않게 한다.
		subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(hashToken("")))
		return false
	}
	return subtle.ConstantTimeCompare([]byte(hashToken(token)), []byte(stored)) == 1
}

// Ingest는 배치를 한 트랜잭션으로 저장한다. 이미 받은 순번은 저장하지 않고 ACK만 돌려준다.
func (s *Store) Ingest(ctx context.Context, b protocol.Batch, receivedAt time.Time) (protocol.Ack, error) {
	if b.ServerID == "" || b.Epoch == "" || b.Seq == 0 {
		return protocol.Ack{}, errors.New("server_id, epoch, seq가 필요합니다")
	}
	if b.Mode != s.mode {
		return protocol.Ack{}, fmt.Errorf("%s 배치를 %s 서버에 저장할 수 없습니다", b.Mode, s.mode)
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return protocol.Ack{}, err
	}
	defer tx.Rollback()

	var lastSeq uint64
	err = tx.QueryRowContext(ctx, `SELECT last_seq FROM ingest_cursors WHERE server_id = ? AND epoch = ?`,
		b.ServerID, b.Epoch).Scan(&lastSeq)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return protocol.Ack{}, err
	}
	if b.Seq <= lastSeq {
		return protocol.Ack{Seq: b.Seq, Duplicate: true}, nil
	}

	if err := upsertServer(ctx, tx, b, receivedAt); err != nil {
		return protocol.Ack{}, err
	}
	if b.Topology != nil {
		if err := applyTopology(ctx, tx, b.ServerID, b.Topology); err != nil {
			return protocol.Ack{}, err
		}
	}
	for _, e := range b.Events {
		if err := insertEvent(ctx, tx, b.ServerID, e, receivedAt); err != nil {
			return protocol.Ack{}, err
		}
	}
	for _, sample := range b.Samples {
		if err := insertSample(ctx, tx, b.ServerID, sample); err != nil {
			return protocol.Ack{}, err
		}
	}
	for _, g := range b.Gaps {
		_, err := tx.ExecContext(ctx, `INSERT INTO gaps (server_id, source, from_at, to_at, reason, dropped) VALUES (?, ?, ?, ?, ?, ?)`,
			b.ServerID, g.Source, g.From.UnixMilli(), g.To.UnixMilli(), g.Reason, g.Dropped)
		if err != nil {
			return protocol.Ack{}, err
		}
	}
	for _, p := range b.Probes {
		if err := applyProbe(ctx, tx, b.ServerID, p, receivedAt); err != nil {
			return protocol.Ack{}, err
		}
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO ingest_cursors (server_id, epoch, last_seq) VALUES (?, ?, ?)
		ON CONFLICT (server_id, epoch) DO UPDATE SET last_seq = excluded.last_seq`,
		b.ServerID, b.Epoch, b.Seq)
	if err != nil {
		return protocol.Ack{}, err
	}
	return protocol.Ack{Seq: b.Seq}, tx.Commit()
}

func upsertServer(ctx context.Context, tx *sql.Tx, b protocol.Batch, receivedAt time.Time) error {
	skew := receivedAt.Sub(b.SentAt).Milliseconds()
	_, err := tx.ExecContext(ctx, `
		INSERT INTO servers (id, last_seen, clock_skew_ms) VALUES (?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET last_seen = excluded.last_seen, clock_skew_ms = excluded.clock_skew_ms`,
		b.ServerID, receivedAt.UnixMilli(), skew)
	if err != nil || b.Heartbeat == nil {
		return err
	}
	collectors, err := json.Marshal(b.Heartbeat.Collectors)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE servers SET agent_version = ?, boot_time = ?, uptime_seconds = ?, docker_ok = ?, collectors = ? WHERE id = ?`,
		b.Heartbeat.AgentVersion, b.Heartbeat.BootTime.UnixMilli(), b.Heartbeat.UptimeSeconds,
		b.Heartbeat.DockerOK, string(collectors), b.ServerID)
	return err
}

// applyTopology는 구성이 바뀐 경우에만 새 버전을 만든다.
func applyTopology(ctx context.Context, tx *sql.Tx, serverID string, t *protocol.Topology) error {
	var currentID, currentFrom int64
	var currentHash string
	err := tx.QueryRowContext(ctx, `
		SELECT id, hash, valid_from FROM topology_versions
		WHERE server_id = ? AND valid_to IS NULL ORDER BY valid_from DESC LIMIT 1`, serverID).
		Scan(&currentID, &currentHash, &currentFrom)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	observed := t.ObservedAt.UnixMilli()
	if err == nil {
		// 재전송으로 늦게 도착한 오래된 스냅샷이 현재 구성을 덮어쓰지 않게 한다.
		if currentHash == t.Hash || observed < currentFrom {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE topology_versions SET valid_to = ? WHERE id = ?`, observed, currentID); err != nil {
			return err
		}
	}
	snapshot, err := json.Marshal(t)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO topology_versions (server_id, hash, valid_from, snapshot) VALUES (?, ?, ?, ?)`,
		serverID, t.Hash, observed, string(snapshot))
	return err
}

func insertEvent(ctx context.Context, tx *sql.Tx, serverID string, e protocol.Event, receivedAt time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO events (server_id, occurred_at, received_at, category, type, severity, source, target,
			service_key, container_id, summary, duration_ms, duration_limit_ms, bytes, result, evidence, aggregated)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		serverID, e.OccurredAt.UnixMilli(), receivedAt.UnixMilli(), string(e.Category), e.Type, string(e.Severity),
		e.Source, e.Target, e.ServiceKey, e.ContainerID, e.Summary,
		nullable(e.DurationMS), nullable(e.DurationLimitMS), nullable(e.Bytes), e.Result, e.Evidence, e.Aggregated)
	return err
}

func insertSample(ctx context.Context, tx *sql.Tx, serverID string, sample protocol.Sample) error {
	_, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO samples (server_id, scope, name, at, value) VALUES (?, ?, ?, ?, ?)`,
		serverID, sample.Scope, sample.Name, sample.At.UnixMilli(), sample.Value)
	return err
}

// nullable은 측정하지 않은 값을 0이 아니라 NULL로 저장하게 한다.
func nullable(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

// SetSameHost는 시작할 때 한 번 호출한다.
func (s *Store) SetSameHost(serverIDs []string) {
	s.sameHost = make(map[string]bool, len(serverIDs))
	for _, id := range serverIDs {
		s.sameHost[id] = true
	}
}

// Purge는 보관 기간이 지난 기록을 지운다.
func (s *Store) Purge(ctx context.Context, now time.Time) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	sampleLimit := now.Add(-sampleRetention).UnixMilli()
	recordLimit := now.Add(-recordRetention).UnixMilli()
	statements := []struct {
		query string
		limit int64
	}{
		// server_id를 조건에 넣어야 시간 인덱스를 탄다. 없으면 지울 것이 없어도 표 전체를 훑으며 쓰기를 막는다.
		{`DELETE FROM samples WHERE server_id IN (SELECT id FROM servers) AND at < ?`, sampleLimit},
		{`DELETE FROM events WHERE occurred_at < ?`, recordLimit},
		{`DELETE FROM gaps WHERE to_at < ?`, recordLimit},
		{`DELETE FROM topology_versions WHERE valid_to IS NOT NULL AND valid_to < ?`, recordLimit},
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement.query, statement.limit); err != nil {
			return err
		}
	}
	return nil
}
