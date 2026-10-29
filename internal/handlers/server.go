package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/galdlore/resend-server/internal/auth"
	"github.com/galdlore/resend-server/internal/backup"
	"github.com/galdlore/resend-server/internal/config"
	"github.com/galdlore/resend-server/internal/db"
	"github.com/galdlore/resend-server/internal/mailer"
	"github.com/galdlore/resend-server/internal/parser"
)

type Server struct {
	cfg       *config.Config
	db        *db.DB
	auth      *auth.Manager
	mailer    *mailer.Client
	backupSvc *backup.Service
	templates map[string]*template.Template
}

func NewServer(cfg *config.Config, database *db.DB, authMgr *auth.Manager, mailerClient *mailer.Client, backupService *backup.Service) (*Server, error) {
	s := &Server{
		cfg:       cfg,
		db:        database,
		auth:      authMgr,
		mailer:    mailerClient,
		backupSvc: backupService,
		templates: make(map[string]*template.Template),
	}

	if err := s.loadTemplates(); err != nil {
		return nil, fmt.Errorf("テンプレートの読み込みに失敗しました: %w", err)
	}

	return s, nil
}

func (s *Server) loadTemplates() error {
	pages := []string{"login", "compose", "aliases", "history", "settings"}
	for _, page := range pages {
		tmpl, err := template.ParseFiles(
			"web/templates/layout.html",
			fmt.Sprintf("web/templates/%s.html", page),
		)
		if err != nil {
			return fmt.Errorf("ページテンプレートのパースに失敗しました (%s): %w", page, err)
		}
		s.templates[page] = tmpl
	}

	modalTmpl, err := template.ParseFiles("web/templates/modal_detail.html")
	if err != nil {
		return fmt.Errorf("modal_detailのパースに失敗しました: %w", err)
	}
	s.templates["modal_detail"] = modalTmpl

	aliasesPartial, err := template.ParseFiles("web/templates/aliases.html")
	if err != nil {
		return fmt.Errorf("aliases partialのパースに失敗しました: %w", err)
	}
	s.templates["aliases_rows"] = aliasesPartial

	return nil
}

func (s *Server) RegisterRoutes(mux *http.ServeMux) {
	// 静的アセット
	fileServer := http.FileServer(http.Dir("web/static"))
	mux.Handle("/static/", http.StripPrefix("/static/", fileServer))

	// 認証なしルート
	mux.HandleFunc("GET /login", s.handleLoginPage)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)

	// 要認証ルート
	mux.HandleFunc("GET /{$}", s.handleComposePage)
	mux.HandleFunc("POST /api/send", s.handleSendEmail)
	mux.HandleFunc("POST /api/parse-email", s.handleParseEmail)

	mux.HandleFunc("GET /aliases", s.handleAliasesPage)
	mux.HandleFunc("POST /api/aliases", s.handleCreateAlias)
	mux.HandleFunc("DELETE /api/aliases/{id}", s.handleDeleteAlias)

	mux.HandleFunc("GET /history", s.handleHistoryPage)
	mux.HandleFunc("GET /api/history/{id}", s.handleHistoryDetail)

	mux.HandleFunc("GET /settings", s.handleSettingsPage)
	mux.HandleFunc("GET /api/settings/backup-logs", s.handleGetBackupLogs)
	mux.HandleFunc("GET /api/settings/db-size", s.handleGetDatabaseSize)
	mux.HandleFunc("POST /api/backup", s.handleTriggerBackup)
	mux.HandleFunc("POST /api/settings/credentials", s.handleUpdateCredentials)
	mux.HandleFunc("POST /api/settings/resend", s.handleUpdateResendSettings)
	mux.HandleFunc("POST /api/settings/gdrive", s.handleUpdateGDriveSettings)
	mux.HandleFunc("POST /api/settings/cleanup", s.handleCleanupDatabase)

	// Google OAuth 連携
	mux.HandleFunc("GET /auth/google/start", s.handleGoogleOAuthStart)
	mux.HandleFunc("GET /auth/google/callback", s.handleGoogleOAuthCallback)
	mux.HandleFunc("POST /api/settings/google-oauth-client", s.handleUpdateGoogleOAuthClient)
	mux.HandleFunc("POST /api/settings/google-oauth-code", s.handleManualGoogleOAuthCode)
	mux.HandleFunc("POST /api/settings/google-oauth-disconnect", s.handleGoogleOAuthDisconnect)
}

func (s *Server) getDefaultDomain() string {
	return s.db.GetSettingWithDefault("default_domain", s.cfg.DefaultDomain)
}

// ログイン・認証ハンドラー
func (s *Server) handleLoginPage(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err == nil && s.auth.ValidateToken(cookie.Value) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	_ = s.templates["login"].ExecuteTemplate(w, "layout", map[string]interface{}{
		"Title":      "ログイン",
		"IsLoggedIn": false,
		"Error":      r.URL.Query().Get("error"),
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/login?error=入力内容に不備があります", http.StatusSeeOther)
		return
	}

	username := r.FormValue("username")
	password := r.FormValue("password")

	if !s.auth.Authenticate(username, password) {
		http.Redirect(w, r, "/login?error=ユーザー名またはパスワードが正しくありません", http.StatusSeeOther)
		return
	}

	s.auth.SetSessionCookie(w, username)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	s.auth.ClearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// メール作成画面ハンドラー
func (s *Server) handleComposePage(w http.ResponseWriter, r *http.Request) {
	aliases, err := s.db.ListAliases(false)
	if err != nil {
		http.Error(w, "エイリアス一覧の取得に失敗しました", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":         "メール作成",
		"IsLoggedIn":    true,
		"ActiveNav":     "compose",
		"Aliases":       aliases,
		"DefaultDomain": s.getDefaultDomain(),
	}
	_ = s.templates["compose"].ExecuteTemplate(w, "layout", data)
}

// メール送信API (HTMX)
func (s *Server) handleSendEmail(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderAlert(w, "error", "送信データの解析に失敗しました")
		return
	}

	fromAddr := strings.TrimSpace(r.FormValue("from_address"))
	fromName := strings.TrimSpace(r.FormValue("from_name"))
	toAddr := strings.TrimSpace(r.FormValue("to_address"))
	subject := strings.TrimSpace(r.FormValue("subject"))
	body := r.FormValue("body")

	if fromAddr == "" || toAddr == "" || subject == "" || body == "" {
		s.renderAlert(w, "error", "必須項目（送信元、宛先、件名、本文）が未入力です")
		return
	}

	req := mailer.SendRequest{
		FromAddress: fromAddr,
		FromName:    fromName,
		ToAddress:   toAddr,
		Subject:     subject,
		BodyText:    body,
	}

	result, err := s.mailer.Send(req)
	if err != nil {
		errMsg := err.Error()
		_, _ = s.db.CreateSentEmail("", fromAddr, fromName, toAddr, subject, body, "", "failed", errMsg)
		s.renderAlert(w, "error", fmt.Sprintf("メール送信に失敗しました: %s", errMsg))
		return
	}

	_, _ = s.db.CreateSentEmail(result.ID, fromAddr, fromName, toAddr, subject, body, "", "sent", "")

	existing, _ := s.db.GetAliasByAddress(fromAddr)
	if existing == nil {
		_, _ = s.db.CreateAlias(fromAddr, fromName, "送信時に自動追加")
	}

	s.renderAlert(w, "success", fmt.Sprintf("メールを正常に送信しました（Resend ID: %s）", result.ID))
}

// メールソース解析API
func (s *Server) handleParseEmail(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "入力形式が不正です", http.StatusBadRequest)
		return
	}

	rawEmail := r.FormValue("raw_email")
	if strings.TrimSpace(rawEmail) == "" {
		http.Error(w, "メールソースが空です", http.StatusBadRequest)
		return
	}

	parsed, err := parser.ParseEmailSource(rawEmail, s.getDefaultDomain())
	if err != nil {
		http.Error(w, fmt.Sprintf("メールの解析に失敗しました: %v", err), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(parsed)
}

// エイリアス一覧画面
func (s *Server) handleAliasesPage(w http.ResponseWriter, r *http.Request) {
	aliases, err := s.db.ListAliases(true)
	if err != nil {
		http.Error(w, "エイリアス一覧の取得に失敗しました", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":      "エイリアス管理",
		"IsLoggedIn": true,
		"ActiveNav":  "aliases",
		"Aliases":    aliases,
	}
	_ = s.templates["aliases"].ExecuteTemplate(w, "layout", data)
}

// エイリアス登録API (HTMX)
func (s *Server) handleCreateAlias(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "入力形式が不正です", http.StatusBadRequest)
		return
	}

	addr := strings.TrimSpace(r.FormValue("address"))
	name := strings.TrimSpace(r.FormValue("name"))
	note := strings.TrimSpace(r.FormValue("note"))

	if addr == "" {
		http.Error(w, "メールアドレスは必須です", http.StatusBadRequest)
		return
	}

	if _, err := s.db.CreateAlias(addr, name, note); err != nil {
		http.Error(w, fmt.Sprintf("登録に失敗しました: %v", err), http.StatusInternalServerError)
		return
	}

	aliases, _ := s.db.ListAliases(true)
	_ = s.templates["aliases_rows"].ExecuteTemplate(w, "aliases_rows", map[string]interface{}{
		"Aliases": aliases,
	})
}

// エイリアス削除API (HTMX)
func (s *Server) handleDeleteAlias(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "不正なIDです", http.StatusBadRequest)
		return
	}

	if err := s.db.DeleteAlias(id); err != nil {
		http.Error(w, "削除に失敗しました", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
}

// 送信履歴画面
func (s *Server) handleHistoryPage(w http.ResponseWriter, r *http.Request) {
	history, err := s.db.ListSentEmails(100, 0)
	if err != nil {
		http.Error(w, "送信履歴の取得に失敗しました", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":      "送信履歴",
		"IsLoggedIn": true,
		"ActiveNav":  "history",
		"History":    history,
	}
	_ = s.templates["history"].ExecuteTemplate(w, "layout", data)
}

// 送信履歴詳細モーダル (HTMX)
func (s *Server) handleHistoryDetail(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "不正なIDです", http.StatusBadRequest)
		return
	}

	item, err := s.db.GetSentEmail(id)
	if err != nil {
		http.Error(w, "該当データが見つかりません", http.StatusNotFound)
		return
	}

	_ = s.templates["modal_detail"].ExecuteTemplate(w, "modal_detail", map[string]interface{}{
		"Email": item,
	})
}

// 設定画面用データ取得ヘルパー
func (s *Server) getSettingsData(r *http.Request) map[string]interface{} {
	logs, _ := s.db.ListBackupLogs(30)

	currentUsername, _ := s.auth.GetCredentials()
	currentAPIKey := s.mailer.GetAPIKey()
	maskedAPIKey := ""
	if currentAPIKey != "" {
		if len(currentAPIKey) > 8 {
			maskedAPIKey = currentAPIKey[:4] + "••••••••" + currentAPIKey[len(currentAPIKey)-4:]
		} else {
			maskedAPIKey = "••••••••"
		}
	}

	dbSizeBytes, _ := s.db.GetDatabaseSize()
	dbSizeKB := float64(dbSizeBytes) / 1024.0

	redirectURI := fmt.Sprintf("http://%s/auth/google/callback", r.Host)
	authURL, _ := s.backupSvc.GetAuthURL(redirectURI)

	return map[string]interface{}{
		"Title":             "環境設定",
		"IsLoggedIn":        true,
		"ActiveNav":         "settings",
		"Logs":              logs,
		"CurrentUsername":   currentUsername,
		"MaskedAPIKey":      maskedAPIKey,
		"DefaultDomain":     s.getDefaultDomain(),
		"HasGDriveCreds":    s.backupSvc.HasCredentials(),
		"IsOAuthConnected":  s.backupSvc.IsOAuthConnected(),
		"AuthURL":           authURL,
		"RedirectURI":       redirectURI,
		"FolderID":             s.backupSvc.GetFolderID(),
		"BackupIntervalMin":    s.backupSvc.GetIntervalMinutes(),
		"BackupRetentionCount": s.backupSvc.GetRetentionCount(),
		"DatabaseSizeKB":       fmt.Sprintf("%.1f", dbSizeKB),
		"SuccessMsg":           r.URL.Query().Get("success"),
		"ErrorMsg":             r.URL.Query().Get("error"),
	}
}

// Google Drive カード描画ヘルパー (HTMX)
func (s *Server) renderGDriveCard(w http.ResponseWriter, r *http.Request, alertType, alertMsg string) {
	data := s.getSettingsData(r)
	if alertMsg != "" {
		data["CardAlert"] = alertMsg
		data["CardAlertType"] = alertType
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates["settings"].ExecuteTemplate(w, "gdrive_card", data)
}

// Resend カード描画ヘルパー (HTMX)
func (s *Server) renderResendCard(w http.ResponseWriter, r *http.Request, alertType, alertMsg string) {
	data := s.getSettingsData(r)
	if alertMsg != "" {
		data["ResendAlert"] = alertMsg
		data["ResendAlertType"] = alertType
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates["settings"].ExecuteTemplate(w, "resend_card", data)
}

// 認証情報カード描画ヘルパー (HTMX)
func (s *Server) renderCredentialsCard(w http.ResponseWriter, r *http.Request, alertType, alertMsg string) {
	data := s.getSettingsData(r)
	if alertMsg != "" {
		data["CredAlert"] = alertMsg
		data["CredAlertType"] = alertType
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates["settings"].ExecuteTemplate(w, "credentials_card", data)
}

// 設定・バックアップ画面ハンドラー
func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	data := s.getSettingsData(r)
	_ = s.templates["settings"].ExecuteTemplate(w, "layout", data)
}

// バックアップ実行履歴パーシャル取得API (HTMX)
func (s *Server) handleGetBackupLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := s.db.ListBackupLogs(30)
	if err != nil {
		http.Error(w, "バックアップ履歴の取得に失敗しました", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates["settings"].ExecuteTemplate(w, "backup_logs_rows", map[string]interface{}{
		"Logs": logs,
	})
}

// データベースサイズバッジパーシャル取得API (HTMX)
func (s *Server) handleGetDatabaseSize(w http.ResponseWriter, r *http.Request) {
	dbSizeBytes, _ := s.db.GetDatabaseSize()
	dbSizeKB := float64(dbSizeBytes) / 1024.0
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = s.templates["settings"].ExecuteTemplate(w, "db_size_badge", map[string]interface{}{
		"DatabaseSizeKB": fmt.Sprintf("%.1f", dbSizeKB),
	})
}

// 認証情報変更API (HTMX)
func (s *Server) handleUpdateCredentials(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderCredentialsCard(w, r, "error", "入力内容の解析に失敗しました")
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	confirmPass := r.FormValue("confirm_password")

	if username == "" {
		s.renderCredentialsCard(w, r, "error", "ユーザー名は必須です")
		return
	}
	if password == "" {
		s.renderCredentialsCard(w, r, "error", "パスワードを入力してください")
		return
	}
	if password != confirmPass {
		s.renderCredentialsCard(w, r, "error", "確認用パスワードが一致しません")
		return
	}

	if err := s.auth.UpdateCredentials(username, password); err != nil {
		s.renderCredentialsCard(w, r, "error", fmt.Sprintf("認証情報の更新に失敗しました: %v", err))
		return
	}

	// 新しいユーザー名でセッションCookieを再発行
	s.auth.SetSessionCookie(w, username)
	s.renderCredentialsCard(w, r, "success", "ユーザー名とパスワードを正常に更新しました")
}

// Resend設定更新API (HTMX)
func (s *Server) handleUpdateResendSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderResendCard(w, r, "error", "入力内容の解析に失敗しました")
		return
	}

	apiKey := strings.TrimSpace(r.FormValue("resend_api_key"))
	domain := strings.TrimSpace(r.FormValue("default_domain"))

	if apiKey != "" {
		if err := s.db.SetSetting("resend_api_key", apiKey); err != nil {
			s.renderResendCard(w, r, "error", fmt.Sprintf("APIキーの保存に失敗しました: %v", err))
			return
		}
	}

	if domain != "" {
		if err := s.db.SetSetting("default_domain", domain); err != nil {
			s.renderResendCard(w, r, "error", fmt.Sprintf("ドメインの保存に失敗しました: %v", err))
			return
		}
	}

	s.renderResendCard(w, r, "success", "Resend APIキーおよびドメイン設定を保存しました（即時反映）")
}

// Google Drive設定更新API (HTMX)
func (s *Server) handleUpdateGDriveSettings(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderGDriveCard(w, r, "error", "入力内容の解析に失敗しました")
		return
	}

	folderID := strings.TrimSpace(r.FormValue("gdrive_folder_id"))
	serviceJSON := strings.TrimSpace(r.FormValue("gdrive_service_account_json"))
	encKey := strings.TrimSpace(r.FormValue("backup_encryption_key"))

	if folderID != "" {
		if err := s.db.SetSetting("gdrive_folder_id", folderID); err != nil {
			s.renderGDriveCard(w, r, "error", fmt.Sprintf("フォルダIDの保存に失敗しました: %v", err))
			return
		}
	}

	if serviceJSON != "" {
		// JSONとしての最低限の妥当性確認
		var testMap map[string]interface{}
		if err := json.Unmarshal([]byte(serviceJSON), &testMap); err != nil {
			s.renderGDriveCard(w, r, "error", "サービスアカウント鍵のJSON形式が不正です")
			return
		}
		if err := s.db.SetSetting("gdrive_service_account_json", serviceJSON); err != nil {
			s.renderGDriveCard(w, r, "error", fmt.Sprintf("サービスアカウント鍵の保存に失敗しました: %v", err))
			return
		}
	}

	if encKey != "" {
		if err := s.db.SetSetting("backup_encryption_key", encKey); err != nil {
			s.renderGDriveCard(w, r, "error", fmt.Sprintf("暗号化パスフレーズの保存に失敗しました: %v", err))
			return
		}
	}

	intervalStr := strings.TrimSpace(r.FormValue("backup_interval_minutes"))
	if intervalStr != "" {
		if intervalMin, err := strconv.Atoi(intervalStr); err == nil && intervalMin >= 0 {
			if err := s.db.SetSetting("backup_interval_minutes", intervalStr); err != nil {
				s.renderGDriveCard(w, r, "error", fmt.Sprintf("バックアップ頻度の保存に失敗しました: %v", err))
				return
			}
			s.backupSvc.UpdateInterval(intervalMin)
		}
	}

	retentionStr := strings.TrimSpace(r.FormValue("backup_retention_count"))
	if retentionStr != "" {
		if retention, err := strconv.Atoi(retentionStr); err == nil && retention >= 0 {
			if err := s.db.SetSetting("backup_retention_count", retentionStr); err != nil {
				s.renderGDriveCard(w, r, "error", fmt.Sprintf("保持世代数の保存に失敗しました: %v", err))
				return
			}
		}
	}

	s.renderGDriveCard(w, r, "success", "Google Drive連携設定を保存しました（世代管理・頻度を即時反映）")
}

// データベースクリーンアップ＆Vacuum実行API (HTMX)
func (s *Server) handleCleanupDatabase(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderAlert(w, "error", "入力内容の解析に失敗しました")
		return
	}

	daysStr := r.FormValue("days")
	days, err := strconv.Atoi(daysStr)
	if err != nil || days < 0 {
		s.renderAlert(w, "error", "日数の指定が不正です")
		return
	}

	cleanEmails := r.FormValue("clean_sent_emails") == "1"
	cleanLogs := r.FormValue("clean_backup_logs") == "1"

	if !cleanEmails && !cleanLogs {
		s.renderAlert(w, "error", "削除対象（送信履歴またはバックアップ履歴）を少なくとも1つ選択してください")
		return
	}

	before := time.Now().AddDate(0, 0, -days)
	deletedEmails, deletedLogs, newSizeBytes, err := s.db.CleanupAndVacuum(before, cleanEmails, cleanLogs)
	if err != nil {
		s.renderAlert(w, "error", fmt.Sprintf("最適化処理中にエラーが発生しました: %v", err))
		return
	}

	// クリーンアップ成功時にバックアップ履歴およびDBサイズバッジをリフレッシュ
	w.Header().Set("HX-Trigger", "backupHistoryChanged")

	newSizeKB := float64(newSizeBytes) / 1024.0
	msg := fmt.Sprintf("%d日以前のデータを削除しました（送信履歴: %d件、バックアップ履歴: %d件）。Vacuumを実行し、データベースを最適化しました（現在のサイズ: %.1f KB）。",
		days, deletedEmails, deletedLogs, newSizeKB)
	s.renderAlert(w, "success", msg)
}

// 手動バックアップ実行API (HTMX)
func (s *Server) handleTriggerBackup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// バックアップ成功・失敗に関わらず履歴をリフレッシュするためHX-Triggerヘッダーを送信
	w.Header().Set("HX-Trigger", "backupHistoryChanged")

	if err := s.backupSvc.PerformBackup(ctx); err != nil {
		s.renderAlert(w, "error", fmt.Sprintf("バックアップに失敗しました: %s", err))
		return
	}

	s.renderAlert(w, "success", "暗号化データベースをGoogle Driveへ正常に退避しました")
}

func (s *Server) renderAlert(w http.ResponseWriter, alertType, message string) {
	badgeClass := "md-alert-success"
	icon := "check_circle"
	if alertType == "error" {
		badgeClass = "md-alert-error"
		icon = "error"
	}

	html := fmt.Sprintf(`
	<div class="md-alert %s">
		<span class="material-symbols-outlined">%s</span>
		<span>%s</span>
	</div>
	`, badgeClass, icon, template.HTMLEscapeString(message))

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(html))
}

// Google OAuth 認証開始
func (s *Server) handleGoogleOAuthStart(w http.ResponseWriter, r *http.Request) {
	redirectURI := fmt.Sprintf("http://%s/auth/google/callback", r.Host)
	authURL, err := s.backupSvc.GetAuthURL(redirectURI)
	if err != nil {
		http.Redirect(w, r, fmt.Sprintf("/settings?error=%s", err.Error()), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, authURL, http.StatusTemporaryRedirect)
}

// Google OAuth コールバック
func (s *Server) handleGoogleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	errParam := r.URL.Query().Get("error")
	if errParam != "" {
		http.Redirect(w, r, fmt.Sprintf("/settings?error=Google認証が拒否されました: %s", errParam), http.StatusSeeOther)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		http.Redirect(w, r, "/settings?error=認可コードが受信できませんでした", http.StatusSeeOther)
		return
	}

	redirectURI := fmt.Sprintf("http://%s/auth/google/callback", r.Host)
	if err := s.backupSvc.ExchangeCode(r.Context(), code, redirectURI); err != nil {
		http.Redirect(w, r, fmt.Sprintf("/settings?error=トークンの交換に失敗しました: %s", err.Error()), http.StatusSeeOther)
		return
	}

	http.Redirect(w, r, "/settings?success=Google DriveとのOAuth連携が完了しました", http.StatusSeeOther)
}

// OAuthクライアント設定更新API (HTMX)
func (s *Server) handleUpdateGoogleOAuthClient(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderGDriveCard(w, r, "error", "入力内容の解析に失敗しました")
		return
	}

	rawJSON := strings.TrimSpace(r.FormValue("oauth_client_json"))
	clientID := strings.TrimSpace(r.FormValue("client_id"))
	clientSecret := strings.TrimSpace(r.FormValue("client_secret"))

	if rawJSON != "" {
		// JSON妥当性確認
		var testMap map[string]interface{}
		if err := json.Unmarshal([]byte(rawJSON), &testMap); err != nil {
			s.renderGDriveCard(w, r, "error", "OAuthクライアント情報のJSON形式が不正です")
			return
		}
		if err := s.db.SetSetting("google_oauth_client_json", rawJSON); err != nil {
			s.renderGDriveCard(w, r, "error", fmt.Sprintf("設定の保存に失敗しました: %v", err))
			return
		}
	} else if clientID != "" && clientSecret != "" {
		_ = s.db.SetSetting("google_client_id", clientID)
		_ = s.db.SetSetting("google_client_secret", clientSecret)
	} else {
		s.renderGDriveCard(w, r, "error", "OAuthクライアントJSONまたはClient ID/Secretを入力してください")
		return
	}

	s.renderGDriveCard(w, r, "success", "Google OAuthクライアント情報を保存しました。下の「Google認証画面を開く」ボタンを押して認証を行ってください。")
}

// 認可コード手動入力API (HTMX)
func (s *Server) handleManualGoogleOAuthCode(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		s.renderGDriveCard(w, r, "error", "入力内容の解析に失敗しました")
		return
	}

	rawInput := strings.TrimSpace(r.FormValue("auth_code"))
	if rawInput == "" {
		s.renderGDriveCard(w, r, "error", "認可コードまたはリダイレクトURLを入力してください")
		return
	}

	// URL全体が貼り付けられた場合、code=クエリパラメータを抽出
	code := rawInput
	if strings.Contains(rawInput, "code=") {
		parts := strings.Split(rawInput, "code=")
		if len(parts) > 1 {
			code = parts[1]
			if andIdx := strings.Index(code, "&"); andIdx != -1 {
				code = code[:andIdx]
			}
		}
	}

	redirectURI := fmt.Sprintf("http://%s/auth/google/callback", r.Host)
	// installedアプリ用リダイレクトURI（http://localhost）での交換も試行
	err := s.backupSvc.ExchangeCode(r.Context(), code, redirectURI)
	if err != nil {
		// フォールバック: クライアント定義のURI（http://localhost）で試行
		err = s.backupSvc.ExchangeCode(r.Context(), code, "http://localhost")
	}

	if err != nil {
		s.renderGDriveCard(w, r, "error", fmt.Sprintf("認可コードの検証に失敗しました: %v", err))
		return
	}

	s.renderGDriveCard(w, r, "success", "Google DriveとのOAuth連携が完了しました。常時自動バックアップが有効です。")
}

// OAuth連携解除API (HTMX)
func (s *Server) handleGoogleOAuthDisconnect(w http.ResponseWriter, r *http.Request) {
	if err := s.backupSvc.DisconnectOAuth(); err != nil {
		s.renderGDriveCard(w, r, "error", fmt.Sprintf("連携解除に失敗しました: %v", err))
		return
	}
	s.renderGDriveCard(w, r, "success", "Google DriveとのOAuth連携を解除しました")
}
