// Package stats provides async token usage recording backed by SQLite,
// and an HTTP handler to query aggregated statistics.
package stats

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed ui.html
var uiHTML []byte

// DB wraps a SQLite database for async usage recording.
type DB struct {
	db *sql.DB
}

// Open creates (or opens) the SQLite database at path, creating parent directories as needed.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("stats: create db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("stats: open db: %w", err)
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &DB{db: db}, nil
}

// Close closes the underlying database connection.
func (s *DB) Close() error { return s.db.Close() }

const schema = `
CREATE TABLE IF NOT EXISTS usage (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at    TEXT NOT NULL,
    provider      TEXT NOT NULL,
    model         TEXT NOT NULL DEFAULT '',
    path          TEXT NOT NULL DEFAULT '',
    input_tokens  INTEGER NOT NULL DEFAULT 0,
    output_tokens INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_creation_tokens INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_usage_date  ON usage(date(created_at));
CREATE INDEX IF NOT EXISTS idx_usage_model ON usage(model);
`

func migrate(db *sql.DB) error {
	_, err := db.Exec(schema)
	if err != nil {
		return fmt.Errorf("stats: migrate: %w", err)
	}
	// Try to add new columns if they don't exist (for existing databases)
	db.Exec(`ALTER TABLE usage ADD COLUMN cache_read_tokens INTEGER NOT NULL DEFAULT 0`)
	db.Exec(`ALTER TABLE usage ADD COLUMN cache_creation_tokens INTEGER NOT NULL DEFAULT 0`)
	return nil
}

// RecordAsync uses p to parse token usage from the captured response body,
// then writes the record to the database asynchronously.
// Errors are logged but never returned to the caller.
func (s *DB) RecordAsync(provider, reqPath string, data []byte, p Parser) {
	go func() {
		u, ok := p.Parse(data)
		if !ok {
			return
		}
		_, err := s.db.Exec(
			`INSERT INTO usage (created_at, provider, model, path, input_tokens, output_tokens, cache_read_tokens, cache_creation_tokens)
			 VALUES (?,?,?,?,?,?,?,?)`,
			time.Now().UTC().Format(time.RFC3339),
			provider, strings.ToLower(u.Model), reqPath, u.InputTokens, u.OutputTokens, u.CacheReadTokens, u.CacheCreationTokens,
		)
		if err != nil {
			slog.Warn("stats: write failed", "err", err)
		}
	}()
}

// ---- HTTP handler ----

type statRow struct {
	Key                 string `json:"key"`
	Requests            int    `json:"requests"`
	InputTokens         int    `json:"input_tokens"`
	OutputTokens        int    `json:"output_tokens"`
	CacheReadTokens     int    `json:"cache_read_tokens"`
	CacheCreationTokens int    `json:"cache_creation_tokens"`
	TotalTokens         int    `json:"total_tokens"`
}

type statsResponse struct {
	Summary statRow   `json:"summary"`
	ByDay   []statRow `json:"by_day"`
	ByModel []statRow `json:"by_model"`
}

// UIHandler returns an http.HandlerFunc that serves the HTML dashboard.
func (s *DB) UIHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(uiHTML)
	}
}

// Handler returns an http.HandlerFunc that serves JSON usage statistics.
func (s *DB) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		resp, err := s.query()
		if err != nil {
			slog.Error("stats: query failed", "err", err)
			http.Error(w, "stats query error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(resp)
	}
}

func (s *DB) query() (*statsResponse, error) {
	resp := &statsResponse{}

	if err := s.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(input_tokens),0),
		       COALESCE(SUM(output_tokens),0),
		       COALESCE(SUM(cache_read_tokens),0),
		       COALESCE(SUM(cache_creation_tokens),0)
		FROM usage`).Scan(
		&resp.Summary.Requests,
		&resp.Summary.InputTokens,
		&resp.Summary.OutputTokens,
		&resp.Summary.CacheReadTokens,
		&resp.Summary.CacheCreationTokens,
	); err != nil {
		return nil, fmt.Errorf("stats: summary query: %w", err)
	}
	resp.Summary.Key = "total"
	resp.Summary.TotalTokens = resp.Summary.InputTokens + resp.Summary.OutputTokens

	rows, err := s.db.Query(`
		SELECT date(created_at)           AS d,
		       COUNT(*)                   AS requests,
		       COALESCE(SUM(input_tokens),0),
		       COALESCE(SUM(output_tokens),0),
		       COALESCE(SUM(cache_read_tokens),0),
		       COALESCE(SUM(cache_creation_tokens),0)
		FROM usage
		WHERE created_at >= date('now', '-30 days')
		GROUP BY d
		ORDER BY d DESC`)
	if err != nil {
		return nil, fmt.Errorf("stats: by_day query: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r statRow
		if err := rows.Scan(&r.Key, &r.Requests, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens); err != nil {
			return nil, err
		}
		r.TotalTokens = r.InputTokens + r.OutputTokens
		resp.ByDay = append(resp.ByDay, r)
	}

	rows2, err := s.db.Query(`
		SELECT COALESCE(NULLIF(LOWER(model),''), '(unknown)') AS m,
		       COUNT(*)                                        AS requests,
		       COALESCE(SUM(input_tokens),0),
		       COALESCE(SUM(output_tokens),0),
		       COALESCE(SUM(cache_read_tokens),0),
		       COALESCE(SUM(cache_creation_tokens),0)
		FROM usage
		GROUP BY m
		ORDER BY SUM(input_tokens + output_tokens) DESC`)
	if err != nil {
		return nil, fmt.Errorf("stats: by_model query: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var r statRow
		if err := rows2.Scan(&r.Key, &r.Requests, &r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheCreationTokens); err != nil {
			return nil, err
		}
		r.TotalTokens = r.InputTokens + r.OutputTokens
		resp.ByModel = append(resp.ByModel, r)
	}

	return resp, nil
}
