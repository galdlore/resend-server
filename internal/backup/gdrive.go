package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/galdlore/resend-server/internal/crypto"
	"github.com/galdlore/resend-server/internal/db"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

type Service struct {
	db                *db.DB
	defaultPassphrase string
	defaultFileJSON   string
	defaultFolderID   string
	intervalMin       int
	stopChan          chan struct{}
}

func NewService(database *db.DB, passphrase, serviceJSON, folderID string, intervalMin int) *Service {
	return &Service{
		db:                database,
		defaultPassphrase: passphrase,
		defaultFileJSON:   serviceJSON,
		defaultFolderID:   folderID,
		intervalMin:       intervalMin,
		stopChan:          make(chan struct{}),
	}
}

// GetFolderID は現在有効なGoogle Drive保存先フォルダIDを返します。
func (s *Service) GetFolderID() string {
	raw := s.defaultFolderID
	if s.db != nil {
		if id, err := s.db.GetSetting("gdrive_folder_id"); err == nil && id != "" {
			raw = id
		}
	}
	raw = strings.TrimSpace(raw)
	return strings.Trim(raw, "./ \t\r\n")
}

// GetPassphrase は現在有効なバックアップ暗号化パスフレーズを返します。
func (s *Service) GetPassphrase() string {
	if s.db != nil {
		if p, err := s.db.GetSetting("backup_encryption_key"); err == nil && p != "" {
			return p
		}
	}
	return s.defaultPassphrase
}

// GetOAuthConfig は登録されているOAuthクライアントJSONまたはID/Secretからoauth2.Configを生成します。
func (s *Service) GetOAuthConfig(redirectURI string) (*oauth2.Config, error) {
	if s.db == nil {
		return nil, errors.New("データベースが初期化されていません")
	}

	// 1. JSON貼り付けからの生成
	rawJSON, err := s.db.GetSetting("google_oauth_client_json")
	if err == nil && strings.TrimSpace(rawJSON) != "" {
		conf, err := google.ConfigFromJSON([]byte(rawJSON), drive.DriveFileScope)
		if err == nil {
			if redirectURI != "" {
				conf.RedirectURL = redirectURI
			}
			return conf, nil
		}
	}

	// 2. Client ID / Client Secret 直接指定からの生成
	clientID, _ := s.db.GetSetting("google_client_id")
	clientSecret, _ := s.db.GetSetting("google_client_secret")
	if clientID != "" && clientSecret != "" {
		return &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			Endpoint:     google.Endpoint,
			Scopes:       []string{drive.DriveFileScope},
			RedirectURL:  redirectURI,
		}, nil
	}

	return nil, errors.New("Google OAuthクライアント情報が登録されていません")
}

// GetAuthURL はGoogleアカウントへのアクセス許可を求める認可URLを生成します。
func (s *Service) GetAuthURL(redirectURI string) (string, error) {
	conf, err := s.GetOAuthConfig(redirectURI)
	if err != nil {
		return "", err
	}
	// access_type=offline と prompt=consent を指定してリフレッシュトークンを確実に取得
	return conf.AuthCodeURL("resend-state", oauth2.AccessTypeOffline, oauth2.ApprovalForce), nil
}

// ExchangeCode は認可コードをトークン（アクセストークンおよびリフレッシュトークン）と交換し、DBに保存します。
func (s *Service) ExchangeCode(ctx context.Context, code, redirectURI string) error {
	conf, err := s.GetOAuthConfig(redirectURI)
	if err != nil {
		return err
	}

	token, err := conf.Exchange(ctx, strings.TrimSpace(code))
	if err != nil {
		return fmt.Errorf("トークンの交換に失敗しました: %w", err)
	}

	tokenBytes, err := json.Marshal(token)
	if err != nil {
		return fmt.Errorf("トークンデータのシリアライズに失敗しました: %w", err)
	}

	return s.db.SetSetting("google_oauth_token", string(tokenBytes))
}

// IsOAuthConnected は有効なOAuthトークンが保存されているかを判定します。
func (s *Service) IsOAuthConnected() bool {
	if s.db == nil {
		return false
	}
	raw, err := s.db.GetSetting("google_oauth_token")
	return err == nil && strings.TrimSpace(raw) != ""
}

// HasCredentials はGoogle連携（OAuthトークン、サービスアカウントJSON、またはファイル）が存在するかを判定します。
func (s *Service) HasCredentials() bool {
	if s.IsOAuthConnected() {
		return true
	}
	if s.db != nil {
		if raw, err := s.db.GetSetting("gdrive_service_account_json"); err == nil && strings.TrimSpace(raw) != "" {
			return true
		}
	}
	if s.defaultFileJSON != "" {
		if _, err := os.Stat(s.defaultFileJSON); err == nil {
			return true
		}
	}
	return false
}

// DisconnectOAuth は保存されたOAuthトークンを削除します。
func (s *Service) DisconnectOAuth() error {
	if s.db != nil {
		return s.db.SetSetting("google_oauth_token", "")
	}
	return nil
}

func (s *Service) getDriveService(ctx context.Context) (*drive.Service, error) {
	// 1. OAuth 2.0 トークンによるクライアント（個人Googleアカウントの容量で動くため最優先）
	if s.IsOAuthConnected() {
		rawToken, _ := s.db.GetSetting("google_oauth_token")
		var tok oauth2.Token
		if err := json.Unmarshal([]byte(rawToken), &tok); err == nil {
			conf, err := s.GetOAuthConfig("")
			if err == nil {
				// トークンが自動更新された場合にDBへ最新トークンを書き戻すラッパー
				ts := oauth2.ReuseTokenSource(&tok, conf.TokenSource(ctx, &tok))
				client := oauth2.NewClient(ctx, ts)

				srv, err := drive.NewService(ctx, option.WithHTTPClient(client))
				if err == nil {
					// 最新トークンを確認して保存
					if newTok, tokErr := ts.Token(); tokErr == nil && newTok.AccessToken != tok.AccessToken {
						if b, mErr := json.Marshal(newTok); mErr == nil {
							_ = s.db.SetSetting("google_oauth_token", string(b))
						}
					}
					return srv, nil
				}
			}
		}
	}

	// 2. サービスアカウントによるクライアント（フォールバック）
	var clientOpt option.ClientOption
	if s.db != nil {
		if raw, err := s.db.GetSetting("gdrive_service_account_json"); err == nil && strings.TrimSpace(raw) != "" {
			clientOpt = option.WithCredentialsJSON([]byte(raw))
		}
	}
	if clientOpt == nil && s.defaultFileJSON != "" {
		if _, err := os.Stat(s.defaultFileJSON); err == nil {
			clientOpt = option.WithCredentialsFile(s.defaultFileJSON)
		}
	}

	if clientOpt != nil {
		return drive.NewService(ctx, clientOpt)
	}

	return nil, errors.New("Google連携設定がありません。設定画面からGoogle OAuth連携を行ってください。")
}

// PerformBackup はデータベースのスナップショットを作成し、AES-256-GCMで暗号化してGoogle Driveへ保存します。
func (s *Service) PerformBackup(ctx context.Context) error {
	folderID := s.GetFolderID()
	if folderID == "" {
		err := errors.New("Google Driveの保存先フォルダIDが指定されていません。設定画面から登録してください。")
		_ = s.db.CreateBackupLog("none", "", 0, "failed", err.Error())
		return err
	}

	driveService, err := s.getDriveService(ctx)
	if err != nil {
		_ = s.db.CreateBackupLog("none", "", 0, "failed", err.Error())
		return err
	}

	passphrase := s.GetPassphrase()
	timestamp := time.Now().Format("20060102_150405")
	tempSnapshot := filepath.Join(os.TempDir(), fmt.Sprintf("snapshot_%s.db", timestamp))
	defer os.Remove(tempSnapshot)

	// 1. SQLiteのスナップショットを作成
	if err := s.db.CreateSnapshot(tempSnapshot); err != nil {
		wrappedErr := fmt.Errorf("データベーススナップショットの作成に失敗しました: %w", err)
		_ = s.db.CreateBackupLog("snapshot_failed", "", 0, "failed", wrappedErr.Error())
		return wrappedErr
	}

	// 2. スナップショットを読み込み
	rawBytes, err := os.ReadFile(tempSnapshot)
	if err != nil {
		wrappedErr := fmt.Errorf("スナップショットファイルの読み取りに失敗しました: %w", err)
		_ = s.db.CreateBackupLog("read_failed", "", 0, "failed", wrappedErr.Error())
		return wrappedErr
	}

	// 3. AES-256-GCMで暗号化
	encBytes, err := crypto.Encrypt(rawBytes, passphrase)
	if err != nil {
		wrappedErr := fmt.Errorf("バックアップデータの暗号化に失敗しました: %w", err)
		_ = s.db.CreateBackupLog("encrypt_failed", "", 0, "failed", wrappedErr.Error())
		return wrappedErr
	}

	// 4. Google Driveへアップロード
	uploadFileName := fmt.Sprintf("resend_backup_%s.db.enc", timestamp)
	driveFile := &drive.File{
		Name:    uploadFileName,
		Parents: []string{folderID},
	}

	resFile, err := driveService.Files.Create(driveFile).
		Media(bytes.NewReader(encBytes)).
		Fields("id, name, size").
		Context(ctx).
		Do()
	if err != nil {
		wrappedErr := fmt.Errorf("Google Driveへのアップロードに失敗しました: %w", err)
		_ = s.db.CreateBackupLog(uploadFileName, "", int64(len(encBytes)), "failed", wrappedErr.Error())
		return wrappedErr
	}

	// 5. バックアップ履歴を記録
	if err := s.db.CreateBackupLog(uploadFileName, resFile.Id, int64(len(encBytes)), "success", ""); err != nil {
		log.Printf("バックアップ履歴の記録に失敗しました: %v", err)
	}

	log.Printf("Google Driveへの暗号化バックアップが完了しました: %s (ID: %s, サイズ: %d バイト)", uploadFileName, resFile.Id, len(encBytes))
	return nil
}

// StartScheduler は定期バックアップをバックグラウンドで開始します。
func (s *Service) StartScheduler() {
	if s.intervalMin <= 0 {
		return
	}
	ticker := time.NewTicker(time.Duration(s.intervalMin) * time.Minute)
	go func() {
		log.Printf("定期バックアップスケジューラを開始しました（間隔: %d分）", s.intervalMin)
		for {
			select {
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				if err := s.PerformBackup(ctx); err != nil {
					log.Printf("定期バックアップの実行中にエラーが発生しました: %v", err)
				}
				cancel()
			case <-s.stopChan:
				ticker.Stop()
				return
			}
		}
	}()
}

func (s *Service) Stop() {
	close(s.stopChan)
}
