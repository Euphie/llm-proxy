package admin

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

const systemDatabaseFilename = "llm-proxy.db"

type systemResponse struct {
	Version            string `json:"version"`
	DataDir            string `json:"data_dir"`
	DatabaseFile       string `json:"database_file"`
	DatabaseBytes      int64  `json:"database_bytes"`
	SchemaVersion      int    `json:"schema_version"`
	DefaultProfileID   int64  `json:"default_profile_id"`
	PasswordMustChange bool   `json:"password_must_change"`
}

func (a *API) getSystem(w http.ResponseWriter, r *http.Request) {
	principal, err := principalForRequest(r)
	if err != nil {
		a.writeDomainError(w, r, err)
		return
	}

	var schemaVersion int
	if err := a.db.QueryRowContext(r.Context(), `PRAGMA user_version`).Scan(&schemaVersion); err != nil {
		a.writeInternalError(w, r, fmt.Errorf("read database schema version: %w", err))
		return
	}
	databaseInfo, err := os.Stat(filepath.Join(a.dataDir, systemDatabaseFilename))
	if err != nil {
		a.writeInternalError(w, r, fmt.Errorf("stat database file: %w", err))
		return
	}
	_, defaultProfileID, err := a.profiles.List(r.Context())
	if err != nil {
		a.writeInternalError(w, r, fmt.Errorf("load default Profile: %w", err))
		return
	}

	writeJSON(w, http.StatusOK, systemResponse{
		Version:            a.version,
		DataDir:            a.dataDir,
		DatabaseFile:       systemDatabaseFilename,
		DatabaseBytes:      databaseInfo.Size(),
		SchemaVersion:      schemaVersion,
		DefaultProfileID:   defaultProfileID,
		PasswordMustChange: principal.MustChangePassword,
	})
}

func resolveSystemVersion(injected string) string {
	if version := strings.TrimSpace(injected); version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		version := strings.TrimSpace(info.Main.Version)
		if version != "" && version != "(devel)" {
			return version
		}
	}
	return "development"
}
