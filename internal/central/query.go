package central

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ckdqja135/MSP/internal/protocol"
)

func itoa(n int) string { return strconv.Itoa(n) }

func fromMillis(ms int64) time.Time { return time.UnixMilli(ms).UTC() }

// ServerView는 서버 요약이다. 호스트 지표는 최신값과 측정 시각을 함께 준다.
type ServerView struct {
	ID            string                     `json:"id"`
	Mode          protocol.Mode              `json:"mode"`
	LastSeen      *time.Time                 `json:"last_seen"`
	ClockSkewMS   int64                      `json:"clock_skew_ms"`
	AgentVersion  string                     `json:"agent_version"`
	BootTime      *time.Time                 `json:"boot_time"`
	UptimeSeconds int64                      `json:"uptime_seconds"`
	DockerOK      bool                       `json:"docker_ok"`
	Collectors    []protocol.CollectorStatus `json:"collectors"`
	Reachability  Reachability               `json:"reachability"`
	SameHost      bool                       `json:"same_host"`
	Metrics       map[string]LatestValue     `json:"metrics"`
}

func (s *Store) Servers(ctx context.Context, now time.Time) ([]ServerView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, last_seen, clock_skew_ms, agent_version, boot_time, uptime_seconds, docker_ok, collectors
		FROM servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	servers := []ServerView{}
	for rows.Next() {
		var v ServerView
		var lastSeen, bootTime int64
		var collectors string
		if err := rows.Scan(&v.ID, &lastSeen, &v.ClockSkewMS, &v.AgentVersion, &bootTime, &v.UptimeSeconds, &v.DockerOK, &collectors); err != nil {
			return nil, err
		}
		v.Mode = s.mode
		var seen time.Time
		if lastSeen > 0 {
			seen = fromMillis(lastSeen)
			v.LastSeen = &seen
		}
		if bootTime > 0 {
			boot := fromMillis(bootTime)
			v.BootTime = &boot
		}
		if err := json.Unmarshal([]byte(collectors), &v.Collectors); err != nil {
			return nil, err
		}
		if v.Collectors == nil {
			v.Collectors = []protocol.CollectorStatus{}
		}
		servers = append(servers, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range servers {
		var seen time.Time
		if servers[i].LastSeen != nil {
			seen = *servers[i].LastSeen
		}
		externalOK, err := s.externalResponding(ctx, servers[i].ID, now)
		if err != nil {
			return nil, err
		}
		servers[i].SameHost = s.sameHost[servers[i].ID]
		servers[i].Reachability = reachabilityOf(seen, now, servers[i].DockerOK, externalOK, servers[i].SameHost)
		latest, err := s.latestSamples(ctx, servers[i].ID, now)
		if err != nil {
			return nil, err
		}
		servers[i].Metrics = latest[protocol.HostScope]
		if servers[i].Metrics == nil {
			servers[i].Metrics = map[string]LatestValue{}
		}
	}
	return servers, nil
}

// externalResponding은 최근 외부 응답 확인에 성공한 서비스가 있는지 돌려준다.
func (s *Store) externalResponding(ctx context.Context, serverID string, now time.Time) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM probe_states
		WHERE server_id = ? AND vantage = 'external' AND state = 'ok' AND last_checked > ?`,
		serverID, now.Add(-latestWindow).UnixMilli()).Scan(&count)
	return count > 0, err
}

// latestWindow보다 오래된 값은 최신값으로 내주지 않는다. 멈춘 값을 현재 값처럼 보이지 않게 하기 위해서다.
const latestWindow = 2 * time.Minute

func (s *Store) latestSamples(ctx context.Context, serverID string, at time.Time) (map[string]map[string]LatestValue, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT scope, name, value, MAX(at) FROM samples INDEXED BY samples_time
		WHERE server_id = ? AND at > ? AND at <= ?
		GROUP BY scope, name`, serverID, at.Add(-latestWindow).UnixMilli(), at.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	latest := map[string]map[string]LatestValue{}
	for rows.Next() {
		var scope, name string
		var value float64
		var sampledAt int64
		if err := rows.Scan(&scope, &name, &value, &sampledAt); err != nil {
			return nil, err
		}
		if latest[scope] == nil {
			latest[scope] = map[string]LatestValue{}
		}
		latest[scope][name] = LatestValue{Value: value, At: fromMillis(sampledAt)}
	}
	return latest, rows.Err()
}

func (s *Store) probeViews(ctx context.Context, serverID string) (map[string][]ProbeView, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT service_key, vantage, target, state, consecutive_failures, last_checked, last_latency_ms, last_failure
		FROM probe_states WHERE server_id = ? ORDER BY service_key, vantage`, serverID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	probes := map[string][]ProbeView{}
	for rows.Next() {
		var key, vantage string
		var p ProbeView
		var checked int64
		var latency sql.NullInt64
		if err := rows.Scan(&key, &vantage, &p.Target, &p.State, &p.Failures, &checked, &latency, &p.LastFailure); err != nil {
			return nil, err
		}
		p.Vantage = protocol.Vantage(vantage)
		p.LastChecked = fromMillis(checked)
		if latency.Valid {
			value := latency.Int64
			p.LatencyMS = &value
		}
		probes[key] = append(probes[key], p)
	}
	return probes, rows.Err()
}

var ErrNotFound = errors.New("찾을 수 없습니다")

// Topology는 시점 at의 구조를 돌려준다. at이 nil이면 현재 구조다.
func (s *Store) Topology(ctx context.Context, serverID string, at *time.Time, now time.Time) (TopologyView, error) {
	reference := now
	if at != nil {
		reference = *at
	}

	var (
		versionID, validFrom int64
		validTo              sql.NullInt64
		snapshot             string
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, valid_from, valid_to, snapshot FROM topology_versions
		WHERE server_id = ? AND valid_from <= ? ORDER BY valid_from DESC LIMIT 1`,
		serverID, reference.UnixMilli()).Scan(&versionID, &validFrom, &validTo, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return TopologyView{}, ErrNotFound
	}
	if err != nil {
		return TopologyView{}, err
	}
	var topology protocol.Topology
	if err := json.Unmarshal([]byte(snapshot), &topology); err != nil {
		return TopologyView{}, err
	}

	latest, err := s.latestSamples(ctx, serverID, reference)
	if err != nil {
		return TopologyView{}, err
	}

	historical := at != nil
	probes := map[string][]ProbeView{}
	fresh := true
	if !historical {
		if probes, err = s.probeViews(ctx, serverID); err != nil {
			return TopologyView{}, err
		}
		var lastSeen int64
		err := s.db.QueryRowContext(ctx, `SELECT last_seen FROM servers WHERE id = ?`, serverID).Scan(&lastSeen)
		if err != nil {
			return TopologyView{}, err
		}
		fresh = now.Sub(fromMillis(lastSeen)) < heartbeatLostAfter
	}

	view := buildTopologyView(&topology, probes, latest, fresh, historical)
	view.ServerID = serverID
	view.VersionID = versionID
	view.ValidFrom = fromMillis(validFrom)
	if validTo.Valid {
		to := fromMillis(validTo.Int64)
		view.ValidTo = &to
	}
	return view, nil
}

// EventRow는 흐름표의 한 행이다. 값이 없는 필드는 null로 내려가 0과 구분된다.
type EventRow struct {
	ID              int64                  `json:"id"`
	OccurredAt      time.Time              `json:"occurred_at"`
	ReceivedAt      time.Time              `json:"received_at"`
	Category        protocol.EventCategory `json:"category"`
	Type            string                 `json:"type"`
	Severity        protocol.Severity      `json:"severity"`
	Source          string                 `json:"source"`
	Target          string                 `json:"target"`
	ServiceKey      string                 `json:"service_key"`
	ContainerID     string                 `json:"container_id"`
	Summary         string                 `json:"summary"`
	DurationMS      *int64                 `json:"duration_ms"`
	DurationLimitMS *int64                 `json:"duration_limit_ms"`
	Bytes           *int64                 `json:"bytes"`
	Result          string                 `json:"result"`
	Evidence        string                 `json:"evidence"`
	Aggregated      int                    `json:"aggregated"`
}

type EventFilter struct {
	From, To   time.Time
	Services   []string
	Categories []string
	ErrorsOnly bool
	BeforeID   int64
	Limit      int
}

func (s *Store) Events(ctx context.Context, serverID string, f EventFilter) ([]EventRow, error) {
	query := strings.Builder{}
	query.WriteString(`
		SELECT id, occurred_at, received_at, category, type, severity, source, target, service_key, container_id,
			summary, duration_ms, duration_limit_ms, bytes, result, evidence, aggregated
		FROM events WHERE server_id = ? AND occurred_at >= ? AND occurred_at <= ?`)
	args := []any{serverID, f.From.UnixMilli(), f.To.UnixMilli()}
	if len(f.Services) > 0 {
		query.WriteString(` AND service_key IN (` + placeholders(len(f.Services)) + `)`)
		for _, service := range f.Services {
			args = append(args, service)
		}
	}
	if len(f.Categories) > 0 {
		query.WriteString(` AND category IN (` + placeholders(len(f.Categories)) + `)`)
		for _, category := range f.Categories {
			args = append(args, category)
		}
	}
	if f.ErrorsOnly {
		query.WriteString(` AND severity IN ('warning', 'critical')`)
	}
	if f.BeforeID > 0 {
		query.WriteString(` AND (occurred_at, id) < (SELECT occurred_at, id FROM events WHERE id = ?)`)
		args = append(args, f.BeforeID)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query.WriteString(` ORDER BY occurred_at DESC, id DESC LIMIT ?`)
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []EventRow{}
	for rows.Next() {
		var e EventRow
		var occurred, received int64
		var duration, limitMS, size sql.NullInt64
		if err := rows.Scan(&e.ID, &occurred, &received, &e.Category, &e.Type, &e.Severity, &e.Source, &e.Target,
			&e.ServiceKey, &e.ContainerID, &e.Summary, &duration, &limitMS, &size, &e.Result, &e.Evidence, &e.Aggregated); err != nil {
			return nil, err
		}
		e.OccurredAt = fromMillis(occurred)
		e.ReceivedAt = fromMillis(received)
		e.DurationMS = optional(duration)
		e.DurationLimitMS = optional(limitMS)
		e.Bytes = optional(size)
		events = append(events, e)
	}
	return events, rows.Err()
}

func optional(v sql.NullInt64) *int64 {
	if !v.Valid {
		return nil
	}
	value := v.Int64
	return &value
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

type GapRow struct {
	Source  string    `json:"source"`
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Reason  string    `json:"reason"`
	Dropped int       `json:"dropped"`
}

func (s *Store) Gaps(ctx context.Context, serverID string, from, to time.Time) ([]GapRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source, from_at, to_at, reason, dropped FROM gaps
		WHERE server_id = ? AND to_at >= ? AND from_at <= ? ORDER BY from_at DESC`,
		serverID, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	gaps := []GapRow{}
	for rows.Next() {
		var g GapRow
		var fromAt, toAt int64
		if err := rows.Scan(&g.Source, &fromAt, &toAt, &g.Reason, &g.Dropped); err != nil {
			return nil, err
		}
		g.From, g.To = fromMillis(fromAt), fromMillis(toAt)
		gaps = append(gaps, g)
	}
	return gaps, rows.Err()
}

type Point struct {
	At    int64   `json:"t"`
	Value float64 `json:"v"`
}

type Series struct {
	Name   string  `json:"name"`
	Points []Point `json:"points"`
}

const maxPointsPerSeries = 1000

// Samples는 구간 평균으로 묶어 돌려준다. 샘플이 없는 구간은 점을 만들지 않아 차트에서 끊겨 보인다.
func (s *Store) Samples(ctx context.Context, serverID, scope string, names []string, from, to time.Time, points int) ([]Series, int64, error) {
	if points <= 0 || points > maxPointsPerSeries {
		points = maxPointsPerSeries
	}
	step := to.Sub(from).Milliseconds() / int64(points)
	if step < 1000 {
		step = 1000
	}

	result := []Series{}
	for _, name := range names {
		rows, err := s.db.QueryContext(ctx, `
			SELECT (at / ?) * ? AS bucket, AVG(value) FROM samples
			WHERE server_id = ? AND scope = ? AND name = ? AND at >= ? AND at <= ?
			GROUP BY bucket ORDER BY bucket`,
			step, step, serverID, scope, name, from.UnixMilli(), to.UnixMilli())
		if err != nil {
			return nil, 0, err
		}
		series := Series{Name: name, Points: []Point{}}
		for rows.Next() {
			var p Point
			if err := rows.Scan(&p.At, &p.Value); err != nil {
				rows.Close()
				return nil, 0, err
			}
			series.Points = append(series.Points, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, 0, err
		}
		result = append(result, series)
	}
	return result, step, nil
}
