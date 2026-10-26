package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/galdlore/resend-server/internal/auth"
	"github.com/galdlore/resend-server/internal/backup"
	"github.com/galdlore/resend-server/internal/config"
	"github.com/galdlore/resend-server/internal/db"
	"github.com/galdlore/resend-server/internal/handlers"
	"github.com/galdlore/resend-server/internal/mailer"
)

func setupTestServer(t *testing.T) (*handlers.Server, *db.DB, func()) {
	t.Helper()

	tempDir, err := os.MkdirTemp("", "resend-test-*")
	if err != nil {
		t.Fatalf("一時ディレクトリ作成失敗: %v", err)
	}

	dbPath := filepath.Join(tempDir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		_ = os.RemoveAll(tempDir)
		t.Fatalf("DB初期化失敗: %v", err)
	}

	cfg := &config.Config{
		DefaultDomain:          "example.com",
		SessionSecret:          "test-secret-key-32-bytes-long!!",
		AdminUsername:          "admin",
		AdminPassword:          "password123",
		BackupIntervalMinutes: 1440,
	}

	authMgr := auth.NewManager(database, cfg.AdminUsername, cfg.AdminPassword, cfg.SessionSecret)
	mailerClient := mailer.New(database, cfg.ResendAPIKey)
	backupSvc := backup.NewService(
		database,
		cfg.BackupEncryptionKey,
		cfg.GDriveServiceAccountJSON,
		cfg.GDriveFolderID,
		cfg.BackupIntervalMinutes,
	)

	// templatesはプロジェクトルート相対のためカレントディレクトリを合わせる
	cwd, _ := os.Getwd()
	if filepath.Base(cwd) == "handlers" {
		_ = os.Chdir("../..")
	}

	server, err := handlers.NewServer(cfg, database, authMgr, mailerClient, backupSvc)
	if err != nil {
		_ = database.Close()
		_ = os.RemoveAll(tempDir)
		t.Fatalf("Server初期化失敗: %v", err)
	}

	cleanup := func() {
		_ = database.Close()
		_ = os.RemoveAll(tempDir)
	}

	return server, database, cleanup
}

func TestSettingsPage(t *testing.T) {
	server, _, cleanup := setupTestServer(t)
	defer cleanup()

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/settings", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings 期待ステータス 200, 実際: %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "id=\"gdrive-settings-card\"") {
		t.Errorf("Google Drive設定カードが含まれていません")
	}
	if !strings.Contains(body, "id=\"backup-logs-table\"") {
		t.Errorf("バックアップ履歴テーブルが含まれていません")
	}
	if !strings.Contains(body, "hx-trigger=\"backupHistoryChanged from:body\"") {
		t.Errorf("HTMXトリガーが含まれていません")
	}
}

func TestBackupLogsPartial(t *testing.T) {
	server, database, cleanup := setupTestServer(t)
	defer cleanup()

	// 履歴を投入
	_ = database.CreateBackupLog("backup_test.enc", "drive_file_123", 1024, "success", "")

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/settings/backup-logs", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings/backup-logs 期待ステータス 200, 実際: %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "backup_test.enc") {
		t.Errorf("パーシャルにファイル名が含まれていません: %s", body)
	}
	if !strings.Contains(body, "1024 バイト") {
		t.Errorf("パーシャルにファイルサイズが含まれていません: %s", body)
	}
}

func TestTriggerBackupHXTrigger(t *testing.T) {
	server, _, cleanup := setupTestServer(t)
	defer cleanup()

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	req := httptest.NewRequest("POST", "/api/backup", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	// 認証情報がないのでバックアップ自体は失敗するが、HX-Triggerヘッダーは返されるはず
	hxTrigger := rec.Header().Get("HX-Trigger")
	if hxTrigger != "backupHistoryChanged" {
		t.Errorf("HX-Trigger ヘッダー期待値 backupHistoryChanged, 実際: %q", hxTrigger)
	}
}

func TestUpdateGDriveSettingsPartial(t *testing.T) {
	server, _, cleanup := setupTestServer(t)
	defer cleanup()

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	formData := url.Values{
		"gdrive_folder_id": {"test_folder_id_999"},
	}
	req := httptest.NewRequest("POST", "/api/settings/gdrive", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/settings/gdrive 期待ステータス 200, 実際: %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "test_folder_id_999") {
		t.Errorf("返却されたカードに新しいフォルダIDが含まれていません")
	}
	if !strings.Contains(body, "id=\"gdrive-settings-card\"") {
		t.Errorf("返却されたHTMLがGoogle Driveカードではありません: %s", body)
	}
	if !strings.Contains(body, "Google Drive連携設定を保存しました") {
		t.Errorf("保存完了アラートが含まれていません")
	}
}

func TestUpdateGoogleOAuthClientShowsAuthButton(t *testing.T) {
	server, _, cleanup := setupTestServer(t)
	defer cleanup()

	mux := http.NewServeMux()
	server.RegisterRoutes(mux)

	validClientJSON := `{"installed":{"client_id":"test-client-id.apps.googleusercontent.com","client_secret":"test-secret","redirect_uris":["http://localhost"]}}`
	formData := url.Values{
		"oauth_client_json": {validClientJSON},
	}
	req := httptest.NewRequest("POST", "/api/settings/google-oauth-client", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/settings/google-oauth-client 期待ステータス 200, 実際: %d", rec.Code)
	}

	body := rec.Body.String()
	// 保存後、認証ボタンが表示されているか確認
	if !strings.Contains(body, "Google認証画面を開く") {
		t.Errorf("OAuthクライアント保存後にGoogle認証ボタンが表示されていません: %s", body)
	}
	if !strings.Contains(body, "id=\"gdrive-settings-card\"") {
		t.Errorf("返却されたHTMLがGoogle Driveカードではありません: %s", body)
	}
}
