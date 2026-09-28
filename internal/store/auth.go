package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
)

// DefaultPassword 是首次启动时的 Web 登录密码。
const DefaultPassword = "123456"

// EnsureDefaultPassword 在还没有设置密码时写入默认密码。
func (s *PolicyStore) EnsureDefaultPassword(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_setting WHERE key = 'password_hash'`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	return s.setPasswordLocked(ctx, DefaultPassword)
}

// CheckPassword 判断密码是否正确。
func (s *PolicyStore) CheckPassword(ctx context.Context, password string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	salt, hash, err := s.passwordLocked(ctx)
	if err != nil {
		return false, err
	}
	return hashPassword(salt, password) == hash, nil
}

// SetPassword 修改登录密码。
func (s *PolicyStore) SetPassword(ctx context.Context, password string) error {
	if len([]rune(password)) < 4 || len([]rune(password)) > 64 {
		return errors.New("password length")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setPasswordLocked(ctx, password)
}

func (s *PolicyStore) passwordLocked(ctx context.Context) (salt, hash string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT value FROM app_setting WHERE key = 'password_salt'`).Scan(&salt)
	if err != nil {
		return "", "", err
	}
	err = s.db.QueryRowContext(ctx, `SELECT value FROM app_setting WHERE key = 'password_hash'`).Scan(&hash)
	return salt, hash, err
}

func (s *PolicyStore) setPasswordLocked(ctx context.Context, password string) error {
	var salt [16]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return err
	}
	saltHex := hex.EncodeToString(salt[:])
	hash := hashPassword(saltHex, password)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, kv := range [][2]string{{"password_salt", saltHex}, {"password_hash", hash}} {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO app_setting (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value`, kv[0], kv[1]); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func hashPassword(salt, password string) string {
	sum := sha256.Sum256([]byte(salt + "\n" + password))
	return hex.EncodeToString(sum[:])
}

// TemplateRow 是一份可套用的策略。
type TemplateRow struct {
	ID        string
	Name      string
	Spec      string
	UpdatedAt int64
}

// Templates 按名称返回全部策略模板。
func (s *PolicyStore) Templates(ctx context.Context) ([]TemplateRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, spec, updated_at FROM policy_template ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TemplateRow
	for rows.Next() {
		var r TemplateRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Spec, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveTemplate 新建或覆盖一份策略模板。id 为空时自动生成。
func (s *PolicyStore) SaveTemplate(ctx context.Context, id, name, spec string, now int64) (string, error) {
	if id == "" {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		id = hex.EncodeToString(b[:])
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO policy_template (id, name, spec, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name, spec = excluded.spec, updated_at = excluded.updated_at`,
		id, name, spec, now)
	if err != nil {
		return "", err
	}
	return id, nil
}

// DeleteTemplate 删除一份策略模板。
func (s *PolicyStore) DeleteTemplate(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, err := s.db.ExecContext(ctx, `DELETE FROM policy_template WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
