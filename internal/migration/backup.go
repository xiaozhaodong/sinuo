package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	timezonepolicy "sinuo/internal/timezone"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type BackupManifest struct {
	MigrationID     string `json:"migration_id"`
	LegacyTimezone  string `json:"legacy_timezone,omitempty"`
	CreatedAt       string `json:"created_at"`
	SourceMode      string `json:"source_mode"`
	DatabasePath    string `json:"database_path"`
	ConfigPath      string `json:"config_path"`
	DatabaseExisted bool   `json:"database_existed"`
	DatabaseSize    int64  `json:"database_size,omitempty"`
	DatabaseSHA256  string `json:"database_sha256,omitempty"`
	ConfigSize      int64  `json:"config_size"`
	ConfigSHA256    string `json:"config_sha256"`
	IntegrityCheck  string `json:"integrity_check,omitempty"`
}

type BackupResult struct {
	Directory      string
	DatabasePath   string
	ConfigPath     string
	ManifestPath   string
	ManifestSHA256 string
	IntegrityCheck string
}

type BackupOptions struct {
	DB              *sql.DB
	MigrationID     string
	DirectorySlug   string
	LegacyTimezone  string
	DatabasePath    string
	ConfigPath      string
	DataDir         string
	SourceMode      SourceMode
	DatabaseExisted bool
	Now             func() time.Time
}

func CreateMigrationBackup(ctx context.Context, options BackupOptions) (*BackupResult, error) {
	if options.DB == nil {
		return nil, fmt.Errorf("backup database connection is nil")
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MigrationID == "" {
		options.MigrationID = EndpointFlattenMigrationID
	}
	if options.DirectorySlug == "" {
		options.DirectorySlug = "20260803-claude-endpoint-flatten"
	}
	if strings.ContainsAny(options.DirectorySlug, `/\\`) || options.DirectorySlug == "." || options.DirectorySlug == ".." {
		return nil, fmt.Errorf("invalid migration backup directory slug %q", options.DirectorySlug)
	}
	stamp := options.Now().Format("20060102-150405.000000000")
	root := filepath.Join(options.DataDir, "migration-backups")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create migration backup root: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("chmod migration backup root: %w", err)
	}
	directory := filepath.Join(root, options.DirectorySlug+"-"+stamp)
	if err := os.Mkdir(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create migration backup directory: %w", err)
	}

	manifest := BackupManifest{
		MigrationID:     options.MigrationID,
		LegacyTimezone:  options.LegacyTimezone,
		CreatedAt:       timezonepolicy.FormatStorage(options.Now()),
		SourceMode:      string(options.SourceMode),
		DatabasePath:    options.DatabasePath,
		ConfigPath:      options.ConfigPath,
		DatabaseExisted: options.DatabaseExisted,
	}
	result := &BackupResult{Directory: directory}

	if options.DatabaseExisted {
		tempDB := filepath.Join(directory, "usage.db.tmp")
		finalDB := filepath.Join(directory, "usage.db")
		if _, err := options.DB.ExecContext(ctx, "VACUUM INTO "+sqliteString(tempDB)); err != nil {
			return nil, fmt.Errorf("create SQLite migration snapshot: %w", err)
		}
		if err := os.Chmod(tempDB, 0o600); err != nil {
			return nil, fmt.Errorf("chmod database backup: %w", err)
		}
		if err := os.Rename(tempDB, finalDB); err != nil {
			return nil, fmt.Errorf("finalize database backup: %w", err)
		}
		integrity, err := checkSQLiteIntegrity(finalDB)
		if err != nil {
			return nil, err
		}
		if integrity != "ok" {
			return nil, fmt.Errorf("database backup integrity_check returned %q", integrity)
		}
		size, hash, err := fileInfoAndSHA256(finalDB)
		if err != nil {
			return nil, err
		}
		manifest.DatabaseSize = size
		manifest.DatabaseSHA256 = hash
		manifest.IntegrityCheck = integrity
		result.DatabasePath = finalDB
		result.IntegrityCheck = integrity
	}

	backupConfig := filepath.Join(directory, "config.yaml")
	if err := copyFile0600(options.ConfigPath, backupConfig); err != nil {
		return nil, fmt.Errorf("backup active config: %w", err)
	}
	configSize, configHash, err := fileInfoAndSHA256(backupConfig)
	if err != nil {
		return nil, err
	}
	manifest.ConfigSize = configSize
	manifest.ConfigSHA256 = configHash
	result.ConfigPath = backupConfig

	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode backup manifest: %w", err)
	}
	manifestBytes = append(manifestBytes, '\n')
	manifestPath := filepath.Join(directory, "manifest.json")
	if err := os.WriteFile(manifestPath, manifestBytes, 0o600); err != nil {
		return nil, fmt.Errorf("write backup manifest: %w", err)
	}
	if err := os.Chmod(manifestPath, 0o600); err != nil {
		return nil, fmt.Errorf("chmod backup manifest: %w", err)
	}
	_, manifestHash, err := fileInfoAndSHA256(manifestPath)
	if err != nil {
		return nil, err
	}
	result.ManifestPath = manifestPath
	result.ManifestSHA256 = manifestHash
	return result, nil
}

func sqliteString(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func checkSQLiteIntegrity(path string) (string, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return "", fmt.Errorf("open database backup read-only: %w", err)
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return "", fmt.Errorf("check database backup integrity: %w", err)
	}
	return result, nil
}

func copyFile0600(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileInfoAndSHA256(path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return 0, "", err
	}
	info, err := file.Stat()
	if err != nil {
		return 0, "", err
	}
	return info.Size(), hex.EncodeToString(hash.Sum(nil)), nil
}
