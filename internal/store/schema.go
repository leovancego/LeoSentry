package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"slices"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

//go:embed device_migrations/*.sql
var deviceMigrationFS embed.FS

// migrate 把库文件的结构升级到最新版本。版本号记录在 PRAGMA user_version 中，
// 每个迁移脚本在各自的事务内执行且只执行一次；已是最新版本时不做任何写入。
func migrate(ctx context.Context, db *sql.DB) error {
	return migrateFS(ctx, db, migrationFS, "migrations/*.sql")
}

func migrateFS(ctx context.Context, db *sql.DB, fsys embed.FS, glob string) error {
	names, err := fs.Glob(fsys, glob)
	if err != nil {
		return err
	}
	slices.Sort(names)

	var version int
	if err := db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	for i, name := range names {
		target := i + 1
		if target <= version {
			continue
		}
		script, err := fsys.ReadFile(name)
		if err != nil {
			return err
		}
		if err := applyMigration(ctx, db, string(script), target); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, script string, version int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, script); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		return err
	}
	return tx.Commit()
}

// schemaVersion 返回迁移脚本对应的最新版本号。
func schemaVersion() int {
	names, _ := fs.Glob(migrationFS, "migrations/*.sql")
	return len(names)
}
