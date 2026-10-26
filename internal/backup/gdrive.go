package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/galdlore/resend-server/internal/crypto"
	"github.com/galdlore/resend-server/internal/db"
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

// HasCredentials はGoogleサービスアカウント鍵（DB登録またはファイル）が存在するかを判定します。
func (s *Service) HasCredentials() bool {
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

func (s *Service) getClientOption() (option.ClientOption, error) {
	if s.db != nil {
		if raw, err := s.db.GetSetting("gdrive_service_account_json"); err == nil && strings.TrimSpace(raw) != "" {
			return option.WithCredentialsJSON([]byte(raw)), nil
		}
	}

	if s.defaultFileJSON != "" {
		if _, err := os.Stat(s.defaultFileJSON); err == nil {
			return option.WithCredentialsFile(s.defaultFileJSON), nil
		}
	}

	return nil, errors.New("Googleサービスアカウント鍵が登録されていません。設定画面からJSON鍵を登録してください。")
}

// PerformBackup はデータベースのスナップショットを作成し、AES-256-GCMで暗号化してGoogle Driveへ保存します。
func (s *Service) PerformBackup(ctx context.Context) error {
	folderID := s.GetFolderID()
	if folderID == "" {
		err := errors.New("Google Driveの保存先フォルダIDが指定されていません。設定画面から登録してください。")
		_ = s.db.CreateBackupLog("none", "", 0, "failed", err.Error())
		return err
	}

	clientOpt, err := s.getClientOption()
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

	// 4. Google Driveクライアントの生成
	driveService, err := drive.NewService(ctx, clientOpt)
	if err != nil {
		wrappedErr := fmt.Errorf("Google Driveサービスの初期化に失敗しました: %w", err)
		_ = s.db.CreateBackupLog("drive_init_failed", "", 0, "failed", wrappedErr.Error())
		return wrappedErr
	}

	// 5. Google Driveへアップロード
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

	// 6. バックアップ履歴を記録
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
