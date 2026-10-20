package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	historyRetentionDays   = 30
	maxEventsPerStation    = 500
	maxSessionsPerStation  = 200
	detailEventLimit       = 50
	detailSessionLimit     = 30
	maxTimelinePoints      = 120
	historySampleInterval  = 10 * time.Second
	historyPruneInterval   = time.Hour
	historyOnlineSampleSec = 10
)

// HistoryEvent là một sự kiện của station (connect, timeout, auth, retry...).
type HistoryEvent struct {
	Time        time.Time `json:"time"`
	Type        string    `json:"type"`
	Message     string    `json:"message"`
	DurationSec float64   `json:"duration_sec"`
}

// HistorySession là một phiên kết nối source -> destination.
type HistorySession struct {
	Start       time.Time  `json:"start"`
	End         *time.Time `json:"end,omitempty"`
	DurationSec float64    `json:"duration_sec"`
	Result      string     `json:"result"`
	Message     string     `json:"message"`
}

// EventCounts là số lượng sự kiện trong cửa sổ thời gian đang xem.
type EventCounts struct {
	Timeout int `json:"timeout"`
	Auth    int `json:"auth"`
	Proxy   int `json:"proxy"`
	NMEA    int `json:"nmea"`
	Retry   int `json:"retry"`
	Errors  int `json:"errors"`
}

// TimelinePoint là một đoạn trên thanh timeline kết nối.
type TimelinePoint struct {
	Start         time.Time `json:"start"`
	OnlinePercent float64   `json:"online_percent"`
	Errors        int       `json:"errors"`
	Sessions      int       `json:"sessions"`
	HasData       bool      `json:"has_data"`
}

// StationHistoryDetail là dữ liệu lịch sử trả về cho popup View.
type StationHistoryDetail struct {
	FirstSeen          *time.Time       `json:"first_seen,omitempty"`
	LastSeen           *time.Time       `json:"last_seen,omitempty"`
	ActiveSessionStart *time.Time       `json:"active_session_start,omitempty"`
	OnlinePercent      float64          `json:"online_percent"`
	TotalSessions      int              `json:"total_sessions"`
	EventCounts        EventCounts      `json:"event_counts"`
	Timeline           []TimelinePoint  `json:"timeline"`
	Events             []HistoryEvent   `json:"events"`
	Sessions           []HistorySession `json:"sessions"`
}

// HistoryStore lưu lịch sử station trong SQLite.
type HistoryStore struct {
	db   *sql.DB
	path string
}

// OpenHistoryStore mở/tạo database và schema.
func OpenHistoryStore(path string) (*HistoryStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	pragmas := []string{
		`PRAGMA journal_mode = WAL`,
		`PRAGMA synchronous = NORMAL`,
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA foreign_keys = ON`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("sqlite pragma %q: %w", p, err)
		}
	}

	if err := createHistorySchema(db); err != nil {
		db.Close()
		return nil, err
	}

	store := &HistoryStore{db: db, path: path}
	if err := store.closeOrphanSessions(); err != nil {
		log.Printf("[History] Close orphan sessions failed: %v", err)
	}
	if err := store.Prune(); err != nil {
		log.Printf("[History] Prune on startup failed: %v", err)
	}
	return store, nil
}

func createHistorySchema(db *sql.DB) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS stations (
			id TEXT PRIMARY KEY,
			first_seen_ts INTEGER NOT NULL,
			last_seen_ts INTEGER NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			station_id TEXT NOT NULL,
			ts INTEGER NOT NULL,
			type TEXT NOT NULL,
			message TEXT NOT NULL DEFAULT '',
			duration_sec REAL NOT NULL DEFAULT 0
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_station_ts
			ON events(station_id, ts DESC)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			station_id TEXT NOT NULL,
			start_ts INTEGER NOT NULL,
			end_ts INTEGER,
			duration_sec REAL NOT NULL DEFAULT 0,
			result TEXT NOT NULL DEFAULT 'active',
			message TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE INDEX IF NOT EXISTS idx_sessions_station_start
			ON sessions(station_id, start_ts DESC)`,
		`CREATE TABLE IF NOT EXISTS activity_buckets (
			station_id TEXT NOT NULL,
			hour_ts INTEGER NOT NULL,
			online_sec INTEGER NOT NULL DEFAULT 0,
			sessions INTEGER NOT NULL DEFAULT 0,
			errors INTEGER NOT NULL DEFAULT 0,
			timeouts INTEGER NOT NULL DEFAULT 0,
			auth_errors INTEGER NOT NULL DEFAULT 0,
			proxy_errors INTEGER NOT NULL DEFAULT 0,
			nmea_errors INTEGER NOT NULL DEFAULT 0,
			retries INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (station_id, hour_ts)
		)`,
	}
	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("create history schema: %w", err)
		}
	}
	return nil
}

// Close đóng database.
func (s *HistoryStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *HistoryStore) ensureStationTx(tx *sql.Tx, stationID string, now time.Time) error {
	ms := now.UnixMilli()
	_, err := tx.Exec(
		`INSERT INTO stations(id, first_seen_ts, last_seen_ts)
		 VALUES(?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET last_seen_ts = excluded.last_seen_ts`,
		stationID, ms, ms,
	)
	return err
}

func eventBucketCounts(eventType string) (errors, timeouts, auth, proxy, nmea, retries int) {
	switch eventType {
	case "timeout":
		return 1, 1, 0, 0, 0, 0
	case "auth_error":
		return 1, 0, 1, 0, 0, 0
	case "proxy_error":
		return 1, 0, 0, 1, 0, 0
	case "nmea_error":
		return 1, 0, 0, 0, 1, 0
	case "error":
		return 1, 0, 0, 0, 0, 0
	case "retry":
		return 0, 0, 0, 0, 0, 1
	default:
		return 0, 0, 0, 0, 0, 0
	}
}

func (s *HistoryStore) bumpEventBucketTx(tx *sql.Tx, stationID string, now time.Time, eventType string) error {
	errors, timeouts, auth, proxy, nmea, retries := eventBucketCounts(eventType)
	if errors == 0 && timeouts == 0 && auth == 0 && proxy == 0 && nmea == 0 && retries == 0 {
		return nil
	}
	_, err := tx.Exec(
		`INSERT INTO activity_buckets(
			station_id, hour_ts, errors, timeouts, auth_errors, proxy_errors, nmea_errors, retries
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(station_id, hour_ts) DO UPDATE SET
			errors = errors + excluded.errors,
			timeouts = timeouts + excluded.timeouts,
			auth_errors = auth_errors + excluded.auth_errors,
			proxy_errors = proxy_errors + excluded.proxy_errors,
			nmea_errors = nmea_errors + excluded.nmea_errors,
			retries = retries + excluded.retries`,
		stationID, now.Truncate(time.Hour).Unix(),
		errors, timeouts, auth, proxy, nmea, retries,
	)
	return err
}

func (s *HistoryStore) bumpSessionBucketTx(tx *sql.Tx, stationID string, now time.Time) error {
	_, err := tx.Exec(
		`INSERT INTO activity_buckets(station_id, hour_ts, sessions)
		 VALUES(?, ?, 1)
		 ON CONFLICT(station_id, hour_ts) DO UPDATE SET sessions = sessions + 1`,
		stationID, now.Truncate(time.Hour).Unix(),
	)
	return err
}

// AddEvent thêm một sự kiện và cập nhật counter theo giờ.
func (s *HistoryStore) AddEvent(stationID, eventType, message string, durationSec float64) error {
	if s == nil || stationID == "" {
		return nil
	}
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := s.ensureStationTx(tx, stationID, now); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO events(station_id, ts, type, message, duration_sec) VALUES(?, ?, ?, ?, ?)`,
		stationID, now.UnixMilli(), eventType, message, durationSec,
	); err != nil {
		return err
	}
	if err := s.bumpEventBucketTx(tx, stationID, now, eventType); err != nil {
		return err
	}
	return tx.Commit()
}

// BeginSession tạo một session mới ở trạng thái active và ghi event connected.
func (s *HistoryStore) BeginSession(stationID string) (int64, error) {
	if s == nil || stationID == "" {
		return 0, nil
	}
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if err := s.ensureStationTx(tx, stationID, now); err != nil {
		return 0, err
	}
	res, err := tx.Exec(
		`INSERT INTO sessions(station_id, start_ts, result) VALUES(?, ?, 'active')`,
		stationID, now.UnixMilli(),
	)
	if err != nil {
		return 0, err
	}
	sessionID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(
		`INSERT INTO events(station_id, ts, type, message, duration_sec) VALUES(?, ?, 'connected', ?, 0)`,
		stationID, now.UnixMilli(), "Connected to source and destination",
	); err != nil {
		return 0, err
	}
	if err := s.bumpSessionBucketTx(tx, stationID, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return sessionID, nil
}

// EndSession đóng session và tính thời lượng kết nối.
func (s *HistoryStore) EndSession(sessionID int64, stationID, result, message string) error {
	if s == nil || sessionID == 0 {
		return nil
	}
	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var startMs int64
	err = tx.QueryRow(
		`SELECT start_ts FROM sessions WHERE id = ? AND end_ts IS NULL`,
		sessionID,
	).Scan(&startMs)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}

	durationSec := float64(now.UnixMilli()-startMs) / 1000.0
	if durationSec < 0 {
		durationSec = 0
	}
	if _, err := tx.Exec(
		`UPDATE sessions SET end_ts = ?, duration_sec = ?, result = ?, message = ?
		 WHERE id = ? AND end_ts IS NULL`,
		now.UnixMilli(), durationSec, result, message, sessionID,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE stations SET last_seen_ts = ? WHERE id = ?`, now.UnixMilli(), stationID); err != nil {
		return err
	}
	return tx.Commit()
}

// SampleOnline cộng thời gian online cho các session đang mở.
func (s *HistoryStore) SampleOnline() error {
	if s == nil {
		return nil
	}
	rows, err := s.db.Query(`SELECT DISTINCT station_id FROM sessions WHERE end_ts IS NULL`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}

	now := time.Now()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	hour := now.Truncate(time.Hour).Unix()
	nowMs := now.UnixMilli()
	for _, id := range ids {
		if _, err := tx.Exec(
			`INSERT INTO activity_buckets(station_id, hour_ts, online_sec)
			 VALUES(?, ?, ?)
			 ON CONFLICT(station_id, hour_ts) DO UPDATE SET
				online_sec = online_sec + excluded.online_sec`,
			id, hour, historyOnlineSampleSec,
		); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE stations SET last_seen_ts = ? WHERE id = ?`, nowMs, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *HistoryStore) closeOrphanSessions() error {
	if s == nil {
		return nil
	}
	_, err := s.db.Exec(
		`UPDATE sessions SET
			end_ts = COALESCE((SELECT last_seen_ts FROM stations WHERE stations.id = sessions.station_id), start_ts),
			duration_sec = (
				COALESCE((SELECT last_seen_ts FROM stations WHERE stations.id = sessions.station_id), start_ts) - start_ts
			) / 1000.0,
			result = 'unknown',
			message = 'relay restarted'
		 WHERE end_ts IS NULL`,
	)
	return err
}

// Prune xóa dữ liệu cũ và giới hạn số event/session mỗi station.
func (s *HistoryStore) Prune() error {
	if s == nil {
		return nil
	}
	now := time.Now()
	cutoffMs := now.Add(-historyRetentionDays * 24 * time.Hour).UnixMilli()
	cutoffHour := now.Add(-historyRetentionDays * 24 * time.Hour).Truncate(time.Hour).Unix()

	eventStations, err := s.distinctStations(`SELECT DISTINCT station_id FROM events`)
	if err != nil {
		return err
	}
	sessionStations, err := s.distinctStations(`SELECT DISTINCT station_id FROM sessions`)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM events WHERE ts < ?`, cutoffMs); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE start_ts < ? AND end_ts IS NOT NULL`, cutoffMs); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM activity_buckets WHERE hour_ts < ?`, cutoffHour); err != nil {
		return err
	}

	for _, id := range eventStations {
		if _, err := tx.Exec(
			`DELETE FROM events
			 WHERE station_id = ? AND id NOT IN (
				SELECT id FROM events WHERE station_id = ? ORDER BY ts DESC, id DESC LIMIT ?
			 )`,
			id, id, maxEventsPerStation,
		); err != nil {
			return err
		}
	}
	for _, id := range sessionStations {
		if _, err := tx.Exec(
			`DELETE FROM sessions
			 WHERE station_id = ? AND end_ts IS NOT NULL AND id NOT IN (
				SELECT id FROM sessions WHERE station_id = ? ORDER BY start_ts DESC, id DESC LIMIT ?
			 )`,
			id, id, maxSessionsPerStation,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *HistoryStore) distinctStations(query string) ([]string, error) {
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DeleteStation xóa toàn bộ lịch sử của một station.
func (s *HistoryStore) DeleteStation(stationID string) error {
	if s == nil || stationID == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, query := range []string{
		`DELETE FROM events WHERE station_id = ?`,
		`DELETE FROM sessions WHERE station_id = ?`,
		`DELETE FROM activity_buckets WHERE station_id = ?`,
		`DELETE FROM stations WHERE id = ?`,
	} {
		if _, err := tx.Exec(query, stationID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// StationDetail lấy lịch sử station trong cửa sổ thời gian.
func (s *HistoryStore) StationDetail(stationID string, window time.Duration) (*StationHistoryDetail, error) {
	detail := &StationHistoryDetail{
		Timeline: []TimelinePoint{},
		Events:   []HistoryEvent{},
		Sessions: []HistorySession{},
	}
	if s == nil || stationID == "" {
		return detail, nil
	}

	now := time.Now()
	windowStart := now.Add(-window)

	var firstSeen, lastSeen sql.NullInt64
	err := s.db.QueryRow(
		`SELECT first_seen_ts, last_seen_ts FROM stations WHERE id = ?`,
		stationID,
	).Scan(&firstSeen, &lastSeen)
	if err == nil {
		if firstSeen.Valid {
			t := time.UnixMilli(firstSeen.Int64)
			detail.FirstSeen = &t
		}
		if lastSeen.Valid {
			t := time.UnixMilli(lastSeen.Int64)
			detail.LastSeen = &t
		}
	} else if err != sql.ErrNoRows {
		return nil, err
	}

	var activeStart sql.NullInt64
	err = s.db.QueryRow(
		`SELECT start_ts FROM sessions WHERE station_id = ? AND end_ts IS NULL ORDER BY start_ts DESC LIMIT 1`,
		stationID,
	).Scan(&activeStart)
	if err == nil && activeStart.Valid {
		t := time.UnixMilli(activeStart.Int64)
		detail.ActiveSessionStart = &t
	} else if err != nil && err != sql.ErrNoRows {
		return nil, err
	}

	buckets, err := s.loadBuckets(stationID, windowStart)
	if err != nil {
		return nil, err
	}

	sumOnline := 0
	for _, b := range buckets {
		sumOnline += b.onlineSec
		detail.EventCounts.Errors += b.errors
		detail.EventCounts.Timeout += b.timeouts
		detail.EventCounts.Auth += b.auth
		detail.EventCounts.Proxy += b.proxy
		detail.EventCounts.NMEA += b.nmea
		detail.EventCounts.Retry += b.retries
		detail.TotalSessions += b.sessions
	}

	denominator := window.Seconds()
	if detail.FirstSeen != nil {
		age := now.Sub(*detail.FirstSeen).Seconds()
		if age > 0 && age < denominator {
			denominator = age
		}
	}
	if denominator > 0 {
		detail.OnlinePercent = float64(sumOnline) / denominator * 100.0
		if detail.OnlinePercent > 100 {
			detail.OnlinePercent = 100
		}
	}

	detail.Timeline = buildTimeline(buckets, windowStart, now, detail.FirstSeen)
	detail.Events, err = s.loadEvents(stationID, detailEventLimit)
	if err != nil {
		return nil, err
	}
	detail.Sessions, err = s.loadSessions(stationID, detailSessionLimit)
	if err != nil {
		return nil, err
	}
	return detail, nil
}

type historyBucketRow struct {
	hourTS    int64
	onlineSec int
	sessions  int
	errors    int
	timeouts  int
	auth      int
	proxy     int
	nmea      int
	retries   int
}

func (s *HistoryStore) loadBuckets(stationID string, windowStart time.Time) ([]historyBucketRow, error) {
	rows, err := s.db.Query(
		`SELECT hour_ts, online_sec, sessions, errors, timeouts, auth_errors,
			proxy_errors, nmea_errors, retries
		 FROM activity_buckets
		 WHERE station_id = ? AND hour_ts >= ?
		 ORDER BY hour_ts`,
		stationID, windowStart.Truncate(time.Hour).Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []historyBucketRow
	for rows.Next() {
		var b historyBucketRow
		if err := rows.Scan(
			&b.hourTS, &b.onlineSec, &b.sessions, &b.errors, &b.timeouts,
			&b.auth, &b.proxy, &b.nmea, &b.retries,
		); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}

func buildTimeline(buckets []historyBucketRow, windowStart, now time.Time, firstSeen *time.Time) []TimelinePoint {
	startHour := windowStart.Truncate(time.Hour).Unix()
	endHour := now.Truncate(time.Hour).Unix()
	currentHour := now.Truncate(time.Hour).Unix()
	totalHours := int((endHour-startHour)/3600) + 1
	if totalHours <= 0 {
		return []TimelinePoint{}
	}

	byHour := make(map[int64]historyBucketRow, len(buckets))
	for _, b := range buckets {
		byHour[b.hourTS] = b
	}

	step := 1
	if totalHours > maxTimelinePoints {
		step = int(math.Ceil(float64(totalHours) / float64(maxTimelinePoints)))
	}

	points := make([]TimelinePoint, 0, (totalHours+step-1)/step)
	for i := 0; i < totalHours; i += step {
		point := TimelinePoint{Start: time.Unix(startHour+int64(i)*3600, 0).UTC()}
		var onlineSec, errors, sessions int
		denominatorSec := 0.0
		hasData := false
		for j := 0; j < step && i+j < totalHours; j++ {
			hour := startHour + int64(i+j)*3600
			segmentStart := time.Unix(hour, 0)
			segmentEnd := segmentStart.Add(time.Hour)
			if hour == currentHour {
				segmentEnd = now
			}
			if firstSeen != nil && segmentEnd.After(*firstSeen) {
				hasData = true
			}
			if firstSeen != nil && firstSeen.After(segmentStart) && firstSeen.Before(segmentEnd) {
				segmentStart = *firstSeen
			}
			hourDenominator := segmentEnd.Sub(segmentStart).Seconds()
			if hourDenominator < 1 {
				hourDenominator = 1
			}
			denominatorSec += hourDenominator
			if b, ok := byHour[hour]; ok {
				onlineSec += b.onlineSec
				errors += b.errors
				sessions += b.sessions
			}
		}
		if denominatorSec > 0 {
			point.OnlinePercent = float64(onlineSec) / denominatorSec * 100.0
			if point.OnlinePercent > 100 {
				point.OnlinePercent = 100
			}
		}
		point.Errors = errors
		point.Sessions = sessions
		point.HasData = hasData
		points = append(points, point)
	}
	return points
}

func (s *HistoryStore) loadEvents(stationID string, limit int) ([]HistoryEvent, error) {
	rows, err := s.db.Query(
		`SELECT ts, type, message, duration_sec
		 FROM events WHERE station_id = ? ORDER BY ts DESC, id DESC LIMIT ?`,
		stationID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := []HistoryEvent{}
	for rows.Next() {
		var ts int64
		var typ, message string
		var duration float64
		if err := rows.Scan(&ts, &typ, &message, &duration); err != nil {
			return nil, err
		}
		events = append(events, HistoryEvent{
			Time:        time.UnixMilli(ts).UTC(),
			Type:        typ,
			Message:     message,
			DurationSec: duration,
		})
	}
	return events, rows.Err()
}

func (s *HistoryStore) loadSessions(stationID string, limit int) ([]HistorySession, error) {
	rows, err := s.db.Query(
		`SELECT start_ts, end_ts, duration_sec, result, message
		 FROM sessions WHERE station_id = ? ORDER BY start_ts DESC, id DESC LIMIT ?`,
		stationID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sessions := []HistorySession{}
	for rows.Next() {
		var startMs int64
		var endMs sql.NullInt64
		var duration float64
		var result, message string
		if err := rows.Scan(&startMs, &endMs, &duration, &result, &message); err != nil {
			return nil, err
		}
		session := HistorySession{
			Start:       time.UnixMilli(startMs).UTC(),
			DurationSec: duration,
			Result:      result,
			Message:     message,
		}
		if endMs.Valid {
			t := time.UnixMilli(endMs.Int64).UTC()
			session.End = &t
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

// classifyHistoryEvent phân loại lỗi để lưu event.
func classifyHistoryEvent(err error) string {
	if err == nil {
		return "error"
	}
	s := strings.ToLower(err.Error())
	switch {
	case strings.Contains(s, "timeout"), strings.Contains(s, "deadline exceeded"):
		return "timeout"
	case strings.Contains(s, "auth"), strings.Contains(s, "rejected"),
		strings.Contains(s, "401"), strings.Contains(s, "403"), strings.Contains(s, "404"),
		strings.Contains(s, "unauthorized"), strings.Contains(s, "forbidden"):
		return "auth_error"
	case strings.Contains(s, "proxy"):
		return "proxy_error"
	case strings.Contains(s, "nmea"):
		return "nmea_error"
	default:
		return "error"
	}
}
