package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type DB struct {
	*sql.DB
	path string
}

type Alias struct {
	ID        int64     `json:"id"`
	Address   string    `json:"address"`
	Name      string    `json:"name"`
	Note      string    `json:"note"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SentEmail struct {
	ID           int64     `json:"id"`
	ResendID     string    `json:"resend_id"`
	FromAddress  string    `json:"from_address"`
	FromName     string    `json:"from_name"`
	ToAddress    string    `json:"to_address"`
	Subject      string    `json:"subject"`
	BodyText     string    `json:"body_text"`
	BodyHTML     string    `json:"body_html"`
	Status       string    `json:"status"`
	ErrorMessage string    `json:"error_message"`
	SentAt       time.Time `json:"sent_at"`
}

type BackupLog struct {
	ID            int64     `json:"id"`
	FileName      string    `json:"file_name"`
	GDriveFileID  string    `json:"gdrive_file_id"`
	FileSizeBytes int64     `json:"file_size_bytes"`
	Status        string    `json:"status"`
	ErrorMessage  string    `json:"error_message"`
	BackedUpAt    time.Time `json:"backed_up_at"`
}

func Open(dbPath string) (*DB, error) {
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("データディレクトリの作成に失敗しました: %w", err)
	}

	conn, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("sqlite接続の開始に失敗しました: %w", err)
	}

	// WALモードと外部キーの有効化
	pragmas := []string{
		"PRAGMA journal_mode = WAL;",
		"PRAGMA busy_timeout = 5000;",
		"PRAGMA foreign_keys = ON;",
		"PRAGMA synchronous = NORMAL;",
	}
	for _, p := range pragmas {
		if _, err := conn.Exec(p); err != nil {
			conn.Close()
			return nil, fmt.Errorf("pragma設定の適用に失敗しました (%s): %w", p, err)
		}
	}

	d := &DB{DB: conn, path: dbPath}
	if err := d.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("テーブルの初期化に失敗しました: %w", err)
	}

	return d, nil
}

func (d *DB) Path() string {
	return d.path
}

func (d *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS aliases (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		address TEXT UNIQUE NOT NULL,
		name TEXT NOT NULL DEFAULT '',
		note TEXT NOT NULL DEFAULT '',
		is_active INTEGER NOT NULL DEFAULT 1,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_aliases_address ON aliases(address);

	CREATE TABLE IF NOT EXISTS sent_emails (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		resend_id TEXT NOT NULL DEFAULT '',
		from_address TEXT NOT NULL,
		from_name TEXT NOT NULL DEFAULT '',
		to_address TEXT NOT NULL,
		subject TEXT NOT NULL DEFAULT '',
		body_text TEXT NOT NULL DEFAULT '',
		body_html TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'sent',
		error_message TEXT NOT NULL DEFAULT '',
		sent_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_sent_emails_sent_at ON sent_emails(sent_at DESC);

	CREATE TABLE IF NOT EXISTS backup_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_name TEXT NOT NULL,
		gdrive_file_id TEXT NOT NULL DEFAULT '',
		file_size_bytes INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL,
		error_message TEXT NOT NULL DEFAULT '',
		backed_up_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_backup_logs_backed_up_at ON backup_logs(backed_up_at DESC);

	CREATE TABLE IF NOT EXISTS app_settings (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL,
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);
	`
	_, err := d.Exec(schema)
	return err
}

// エイリアス関連
func (d *DB) ListAliases(includeInactive bool) ([]Alias, error) {
	query := "SELECT id, address, name, note, is_active, created_at, updated_at FROM aliases"
	if !includeInactive {
		query += " WHERE is_active = 1"
	}
	query += " ORDER BY updated_at DESC"

	rows, err := d.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Alias
	for rows.Next() {
		var a Alias
		var activeInt int
		if err := rows.Scan(&a.ID, &a.Address, &a.Name, &a.Note, &activeInt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		a.IsActive = (activeInt == 1)
		list = append(list, a)
	}
	return list, rows.Err()
}

func (d *DB) GetAlias(id int64) (*Alias, error) {
	row := d.QueryRow("SELECT id, address, name, note, is_active, created_at, updated_at FROM aliases WHERE id = ?", id)
	var a Alias
	var activeInt int
	if err := row.Scan(&a.ID, &a.Address, &a.Name, &a.Note, &activeInt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.IsActive = (activeInt == 1)
	return &a, nil
}

func (d *DB) GetAliasByAddress(address string) (*Alias, error) {
	row := d.QueryRow("SELECT id, address, name, note, is_active, created_at, updated_at FROM aliases WHERE address = ?", address)
	var a Alias
	var activeInt int
	if err := row.Scan(&a.ID, &a.Address, &a.Name, &a.Note, &activeInt, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return nil, err
	}
	a.IsActive = (activeInt == 1)
	return &a, nil
}

func (d *DB) CreateAlias(address, name, note string) (*Alias, error) {
	now := time.Now()
	res, err := d.Exec(
		"INSERT INTO aliases (address, name, note, is_active, created_at, updated_at) VALUES (?, ?, ?, 1, ?, ?)",
		address, name, note, now, now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &Alias{
		ID:        id,
		Address:   address,
		Name:      name,
		Note:      note,
		IsActive:  true,
		CreatedAt: now,
		UpdatedAt: now,
	}, nil
}

func (d *DB) UpdateAlias(id int64, address, name, note string, isActive bool) error {
	activeInt := 0
	if isActive {
		activeInt = 1
	}
	now := time.Now()
	_, err := d.Exec(
		"UPDATE aliases SET address = ?, name = ?, note = ?, is_active = ?, updated_at = ? WHERE id = ?",
		address, name, note, activeInt, now, id,
	)
	return err
}

func (d *DB) DeleteAlias(id int64) error {
	_, err := d.Exec("DELETE FROM aliases WHERE id = ?", id)
	return err
}

// 送信履歴関連
func (d *DB) ListSentEmails(limit, offset int) ([]SentEmail, error) {
	rows, err := d.Query(
		"SELECT id, resend_id, from_address, from_name, to_address, subject, body_text, body_html, status, error_message, sent_at FROM sent_emails ORDER BY sent_at DESC LIMIT ? OFFSET ?",
		limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []SentEmail
	for rows.Next() {
		var s SentEmail
		if err := rows.Scan(&s.ID, &s.ResendID, &s.FromAddress, &s.FromName, &s.ToAddress, &s.Subject, &s.BodyText, &s.BodyHTML, &s.Status, &s.ErrorMessage, &s.SentAt); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

func (d *DB) GetSentEmail(id int64) (*SentEmail, error) {
	row := d.QueryRow(
		"SELECT id, resend_id, from_address, from_name, to_address, subject, body_text, body_html, status, error_message, sent_at FROM sent_emails WHERE id = ?",
		id,
	)
	var s SentEmail
	if err := row.Scan(&s.ID, &s.ResendID, &s.FromAddress, &s.FromName, &s.ToAddress, &s.Subject, &s.BodyText, &s.BodyHTML, &s.Status, &s.ErrorMessage, &s.SentAt); err != nil {
		return nil, err
	}
	return &s, nil
}

func (d *DB) CreateSentEmail(resendID, fromAddress, fromName, toAddress, subject, bodyText, bodyHTML, status, errMsg string) (*SentEmail, error) {
	now := time.Now()
	res, err := d.Exec(
		"INSERT INTO sent_emails (resend_id, from_address, from_name, to_address, subject, body_text, body_html, status, error_message, sent_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
		resendID, fromAddress, fromName, toAddress, subject, bodyText, bodyHTML, status, errMsg, now,
	)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &SentEmail{
		ID:           id,
		ResendID:     resendID,
		FromAddress:  fromAddress,
		FromName:     fromName,
		ToAddress:    toAddress,
		Subject:      subject,
		BodyText:     bodyText,
		BodyHTML:     bodyHTML,
		Status:       status,
		ErrorMessage: errMsg,
		SentAt:       now,
	}, nil
}

// バックアップログ関連
func (d *DB) ListBackupLogs(limit int) ([]BackupLog, error) {
	rows, err := d.Query(
		"SELECT id, file_name, gdrive_file_id, file_size_bytes, status, error_message, backed_up_at FROM backup_logs ORDER BY backed_up_at DESC LIMIT ?",
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []BackupLog
	for rows.Next() {
		var b BackupLog
		if err := rows.Scan(&b.ID, &b.FileName, &b.GDriveFileID, &b.FileSizeBytes, &b.Status, &b.ErrorMessage, &b.BackedUpAt); err != nil {
			return nil, err
		}
		list = append(list, b)
	}
	return list, rows.Err()
}

func (d *DB) CreateBackupLog(fileName, gdriveFileID string, fileSizeBytes int64, status, errMsg string) error {
	now := time.Now()
	_, err := d.Exec(
		"INSERT INTO backup_logs (file_name, gdrive_file_id, file_size_bytes, status, error_message, backed_up_at) VALUES (?, ?, ?, ?, ?, ?)",
		fileName, gdriveFileID, fileSizeBytes, status, errMsg, now,
	)
	return err
}

// CreateSnapshot は稼働中のデータベースの安全な複製を作成します。
func (d *DB) CreateSnapshot(destPath string) error {
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	_ = os.Remove(destPath)

	// SQLite 3.27+ の VACUUM INTO を使用して整合性のあるスナップショットを出力
	_, err := d.Exec(fmt.Sprintf("VACUUM INTO '%s'", filepath.ToSlash(destPath)))
	return err
}

// 設定値関連
func (d *DB) GetSetting(key string) (string, error) {
	row := d.QueryRow("SELECT value FROM app_settings WHERE key = ?", key)
	var val string
	if err := row.Scan(&val); err != nil {
		return "", err
	}
	return val, nil
}

func (d *DB) GetSettingWithDefault(key, defaultVal string) string {
	val, err := d.GetSetting(key)
	if err != nil || val == "" {
		return defaultVal
	}
	return val
}

func (d *DB) SetSetting(key, value string) error {
	now := time.Now()
	_, err := d.Exec(
		"INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at",
		key, value, now,
	)
	return err
}

