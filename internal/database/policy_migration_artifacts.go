package database

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type PolicyMigrationArtifacts struct {
	Directory     string
	DatabasePath  string
	CandidatePath string
}

type LegacyPolicyCandidate struct {
	Found           bool            `json:"found"`
	ProfileID       int64           `json:"profile_id,omitempty"`
	ProfileSlug     string          `json:"profile_slug,omitempty"`
	ProfileConfig   json.RawMessage `json:"profile_config,omitempty"`
	StrategyID      int64           `json:"strategy_id,omitempty"`
	StrategyName    string          `json:"strategy_name,omitempty"`
	StrategyConfig  json.RawMessage `json:"strategy_config,omitempty"`
	PointerRevision int64           `json:"source_runtime_revision,omitempty"`
}

func PreparePolicyMigrationArtifacts(
	db *sql.DB,
	dataDir string,
) (PolicyMigrationArtifacts, error) {
	root := filepath.Join(dataDir, "migration-backups")
	if existing, ok := existingV25Artifacts(root); ok {
		return existing, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return PolicyMigrationArtifacts{}, fmt.Errorf("create Policy migration backup root: %w", err)
	}
	directory := filepath.Join(root, "v25-"+time.Now().UTC().Format("20060102T150405.000000000Z"))
	if err := os.Mkdir(directory, 0o700); err != nil {
		return PolicyMigrationArtifacts{}, fmt.Errorf("create Policy migration backup directory: %w", err)
	}
	artifacts := PolicyMigrationArtifacts{
		Directory:     directory,
		DatabasePath:  filepath.Join(directory, "llm-proxy-v24.db"),
		CandidatePath: filepath.Join(directory, "crs2-strategy-16.json"),
	}
	backupTemp := artifacts.DatabasePath + ".tmp"
	if _, err := db.Exec(`VACUUM INTO ` + sqliteString(backupTemp)); err != nil {
		return PolicyMigrationArtifacts{}, fmt.Errorf("create v24 SQLite backup: %w", err)
	}
	if err := os.Chmod(backupTemp, 0o600); err != nil {
		return PolicyMigrationArtifacts{}, fmt.Errorf("secure v24 SQLite backup: %w", err)
	}
	if err := os.Rename(backupTemp, artifacts.DatabasePath); err != nil {
		return PolicyMigrationArtifacts{}, fmt.Errorf("publish v24 SQLite backup: %w", err)
	}
	candidate, err := readLegacyPolicyCandidate(db)
	if err != nil {
		return PolicyMigrationArtifacts{}, err
	}
	payload, err := json.MarshalIndent(candidate, "", "  ")
	if err != nil {
		return PolicyMigrationArtifacts{}, fmt.Errorf("encode legacy Policy candidate: %w", err)
	}
	payload = append(payload, '\n')
	candidateTemp := artifacts.CandidatePath + ".tmp"
	if err := os.WriteFile(candidateTemp, payload, 0o600); err != nil {
		return PolicyMigrationArtifacts{}, fmt.Errorf("write legacy Policy candidate: %w", err)
	}
	if err := os.Rename(candidateTemp, artifacts.CandidatePath); err != nil {
		return PolicyMigrationArtifacts{}, fmt.Errorf("publish legacy Policy candidate: %w", err)
	}
	return artifacts, nil
}

func existingV25Artifacts(root string) (PolicyMigrationArtifacts, bool) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return PolicyMigrationArtifacts{}, false
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "v25-") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		directory := filepath.Join(root, name)
		artifacts := PolicyMigrationArtifacts{
			Directory:     directory,
			DatabasePath:  filepath.Join(directory, "llm-proxy-v24.db"),
			CandidatePath: filepath.Join(directory, "crs2-strategy-16.json"),
		}
		if fileExists(artifacts.DatabasePath) && fileExists(artifacts.CandidatePath) {
			return artifacts, true
		}
	}
	return PolicyMigrationArtifacts{}, false
}

func readLegacyPolicyCandidate(db *sql.DB) (LegacyPolicyCandidate, error) {
	var candidate LegacyPolicyCandidate
	var profileConfig, strategyConfig string
	err := db.QueryRow(`
		SELECT profile.id, profile.slug, profile.config_json,
		       strategy.id, strategy.name, strategy.config_json,
		       COALESCE(pointer.revision, 0)
		FROM profiles AS profile
		JOIN routing_strategies AS strategy
		  ON strategy.profile_id = profile.id AND strategy.id = 16
		LEFT JOIN routing_strategy_pointers AS pointer ON pointer.profile_id = profile.id
		WHERE profile.slug = 'crs2'
	`).Scan(
		&candidate.ProfileID, &candidate.ProfileSlug, &profileConfig,
		&candidate.StrategyID, &candidate.StrategyName, &strategyConfig,
		&candidate.PointerRevision,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return LegacyPolicyCandidate{Found: false}, nil
	}
	if err != nil {
		return LegacyPolicyCandidate{}, fmt.Errorf("read legacy strategy 16: %w", err)
	}
	if !json.Valid([]byte(profileConfig)) || !json.Valid([]byte(strategyConfig)) {
		return LegacyPolicyCandidate{}, errors.New("legacy strategy 16 contains invalid JSON")
	}
	candidate.Found = true
	candidate.ProfileConfig = json.RawMessage(profileConfig)
	candidate.StrategyConfig = json.RawMessage(strategyConfig)
	return candidate, nil
}

func PendingPolicyMigrationCandidate(
	dataDir string,
) (LegacyPolicyCandidate, string, bool, error) {
	artifacts, ok := existingV25Artifacts(filepath.Join(dataDir, "migration-backups"))
	if !ok {
		return LegacyPolicyCandidate{}, "", false, nil
	}
	resultPath := filepath.Join(artifacts.Directory, "crs2-strategy-16.result.json")
	if fileExists(resultPath) {
		return LegacyPolicyCandidate{}, resultPath, false, nil
	}
	payload, err := os.ReadFile(artifacts.CandidatePath)
	if err != nil {
		return LegacyPolicyCandidate{}, resultPath, false, fmt.Errorf("read pending legacy Policy candidate: %w", err)
	}
	var candidate LegacyPolicyCandidate
	if err := json.Unmarshal(payload, &candidate); err != nil {
		return LegacyPolicyCandidate{}, resultPath, false, fmt.Errorf("decode pending legacy Policy candidate: %w", err)
	}
	return candidate, resultPath, candidate.Found, nil
}

func CompletePolicyMigrationCandidate(resultPath, status, detail string) error {
	payload, err := json.MarshalIndent(struct {
		Status    string `json:"status"`
		Detail    string `json:"detail"`
		CreatedAt string `json:"created_at"`
	}{Status: status, Detail: detail, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode Policy migration result: %w", err)
	}
	payload = append(payload, '\n')
	temp := resultPath + ".tmp"
	if err := os.WriteFile(temp, payload, 0o600); err != nil {
		return fmt.Errorf("write Policy migration result: %w", err)
	}
	if err := os.Rename(temp, resultPath); err != nil {
		return fmt.Errorf("publish Policy migration result: %w", err)
	}
	return nil
}

func sqliteString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
