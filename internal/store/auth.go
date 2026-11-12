package store

import (
	"context"
	"database/sql"
)

// migrateAuthState initializes and upgrades only authentication tables in db.
// The unified database keeps its historical copies during the rollback window.
func migrateAuthState(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS auth_sessions(token_hash TEXT PRIMARY KEY,expires_ts INTEGER NOT NULL,username TEXT NOT NULL DEFAULT '');
CREATE INDEX IF NOT EXISTS auth_sessions_retention_expiry ON auth_sessions(expires_ts);
CREATE TABLE IF NOT EXISTS auth_users(username TEXT PRIMARY KEY,password_hash TEXT NOT NULL,created_ts INTEGER NOT NULL,updated_ts INTEGER NOT NULL,role TEXT NOT NULL DEFAULT 'user',disabled INTEGER NOT NULL DEFAULT 0,last_login_ts INTEGER);
CREATE TABLE IF NOT EXISTS auth_audit(id INTEGER PRIMARY KEY AUTOINCREMENT,actor TEXT NOT NULL,action TEXT NOT NULL,target TEXT NOT NULL,created_ts INTEGER NOT NULL);
CREATE INDEX IF NOT EXISTS auth_audit_created ON auth_audit(created_ts DESC);`); err != nil {
		return err
	}
	for column, definition := range map[string]string{
		"role":          "TEXT NOT NULL DEFAULT 'user'",
		"disabled":      "INTEGER NOT NULL DEFAULT 0",
		"last_login_ts": "INTEGER",
	} {
		if !hasColumnOn(ctx, db, "auth_users", column) {
			if _, err := db.ExecContext(ctx, `ALTER TABLE auth_users ADD COLUMN `+column+` `+definition); err != nil {
				return err
			}
		}
	}
	if !hasColumnOn(ctx, db, "auth_sessions", "username") {
		if _, err := db.ExecContext(ctx, `ALTER TABLE auth_sessions ADD COLUMN username TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}
