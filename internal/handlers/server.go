package handlers

import (
	"context"
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

	// モーダル用単体テンプレート
	modalTmpl, err := template.ParseFiles("web/templates/modal_detail.html")
	if err != nil {
		return fmt.Errorf("modal_detailのパースに失敗しました: %w", err)
	}
	s.templates["modal_detail"] = modalTmpl

	// エイリアステーブル再描画用パーシャル
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

	mux.HandleFunc("GET /aliases", s.handleAliasesPage)
	mux.HandleFunc("POST /api/aliases", s.handleCreateAlias)
	mux.HandleFunc("DELETE /api/aliases/{id}", s.handleDeleteAlias)

	mux.HandleFunc("GET /history", s.handleHistoryPage)
	mux.HandleFunc("GET /api/history/{id}", s.handleHistoryDetail)

	mux.HandleFunc("GET /settings", s.handleSettingsPage)
	mux.HandleFunc("POST /api/backup", s.handleTriggerBackup)
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
		"DefaultDomain": s.cfg.DefaultDomain,
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

	// Resend API送信
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

	// 送信成功ログ記録
	_, _ = s.db.CreateSentEmail(result.ID, fromAddr, fromName, toAddr, subject, body, "", "sent", "")

	// 登録されていないエイリアスの場合は自動登録するかメモ
	existing, _ := s.db.GetAliasByAddress(fromAddr)
	if existing == nil {
		_, _ = s.db.CreateAlias(fromAddr, fromName, "送信時に自動追加")
	}

	s.renderAlert(w, "success", fmt.Sprintf("メールを正常に送信しました（Resend ID: %s）", result.ID))
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
		// 既に存在する場合などはエラー
		http.Error(w, fmt.Sprintf("登録に失敗しました: %v", err), http.StatusInternalServerError)
		return
	}

	// 更新後の行一覧を再描画
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

	// 空レスポンスでouterHTMLスワップにより要素をDOMから除去
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

// 設定・バックアップ画面
func (s *Server) handleSettingsPage(w http.ResponseWriter, r *http.Request) {
	logs, err := s.db.ListBackupLogs(30)
	if err != nil {
		http.Error(w, "バックアップ履歴の取得に失敗しました", http.StatusInternalServerError)
		return
	}

	data := map[string]interface{}{
		"Title":              "バックアップ設定",
		"IsLoggedIn":         true,
		"ActiveNav":          "settings",
		"Logs":               logs,
		"ServiceAccountJSON": s.cfg.GDriveServiceAccountJSON,
		"FolderID":           s.cfg.GDriveFolderID,
		"BackupIntervalMin":  s.cfg.BackupIntervalMinutes,
	}
	_ = s.templates["settings"].ExecuteTemplate(w, "layout", data)
}

// 手動バックアップ実行API (HTMX)
func (s *Server) handleTriggerBackup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

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
