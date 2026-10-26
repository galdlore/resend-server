package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/galdlore/resend-server/internal/db"
)

const (
	SessionCookieName = "resend_session"
	SessionDuration   = 7 * 24 * time.Hour
)

type Manager struct {
	db              *db.DB
	defaultUsername string
	defaultPassword string
	secret          []byte
}

func NewManager(database *db.DB, defaultUsername, defaultPassword, secret string) *Manager {
	return &Manager{
		db:              database,
		defaultUsername: defaultUsername,
		defaultPassword: defaultPassword,
		secret:          []byte(secret),
	}
}

// GetCredentials は現在の有効なユーザー名とパスワードを返します（DBのカスタム設定を優先）。
func (m *Manager) GetCredentials() (string, string) {
	username := m.defaultUsername
	password := m.defaultPassword

	if m.db != nil {
		if u, err := m.db.GetSetting("admin_username"); err == nil && u != "" {
			username = u
		}
		if p, err := m.db.GetSetting("admin_password"); err == nil && p != "" {
			password = p
		}
	}
	return username, password
}

// UpdateCredentials はDBに新しい認証情報を保存します。
func (m *Manager) UpdateCredentials(newUsername, newPassword string) error {
	if err := m.db.SetSetting("admin_username", newUsername); err != nil {
		return fmt.Errorf("ユーザー名の保存に失敗しました: %w", err)
	}
	if err := m.db.SetSetting("admin_password", newPassword); err != nil {
		return fmt.Errorf("パスワードの保存に失敗しました: %w", err)
	}
	return nil
}

// Authenticate はユーザー名とパスワードを検証します。
func (m *Manager) Authenticate(username, password string) bool {
	validUser, validPass := m.GetCredentials()
	return username == validUser && password == validPass
}

// CreateToken は改ざん防止のHMAC署名付きセッショントークンを生成します。
func (m *Manager) CreateToken(username string) string {
	expires := time.Now().Add(SessionDuration).Unix()
	payload := fmt.Sprintf("%s:%d", username, expires)

	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	signature := hex.EncodeToString(mac.Sum(nil))

	return fmt.Sprintf("%s:%s", payload, signature)
}

// ValidateToken はトークンの有効期限とHMAC署名を検証します。
func (m *Manager) ValidateToken(token string) bool {
	parts := strings.Split(token, ":")
	if len(parts) != 3 {
		return false
	}

	username, expiresStr, signature := parts[0], parts[1], parts[2]
	currentUsername, _ := m.GetCredentials()
	if username != currentUsername {
		return false
	}

	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil || time.Now().Unix() > expires {
		return false
	}

	payload := fmt.Sprintf("%s:%d", username, expires)
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(payload))
	expectedSig := hex.EncodeToString(mac.Sum(nil))

	return hmac.Equal([]byte(signature), []byte(expectedSig))
}

// SetSessionCookie はブラウザにセッションCookieを発行します。
func (m *Manager) SetSessionCookie(w http.ResponseWriter, username string) {
	token := m.CreateToken(username)
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(SessionDuration.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// ClearSessionCookie はセッションCookieを破棄します。
func (m *Manager) ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
}

// Middleware は認証済みかどうかを検査し、未認証の場合はログインページへ誘導します。
func (m *Manager) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if strings.HasPrefix(path, "/static/") || path == "/login" || strings.HasPrefix(path, "/auth/google/callback") {
			next.ServeHTTP(w, r)
			return
		}

		cookie, err := r.Cookie(SessionCookieName)
		if err != nil || !m.ValidateToken(cookie.Value) {
			if r.Header.Get("HX-Request") == "true" {
				w.Header().Set("HX-Redirect", "/login")
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		next.ServeHTTP(w, r)
	})
}
