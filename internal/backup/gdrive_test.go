package backup

import (
	"path/filepath"
	"testing"

	"github.com/galdlore/resend-server/internal/db"
)

func TestServiceIntervalAndRetention(t *testing.T) {
	tempDB := filepath.Join(t.TempDir(), "test_backup.db")
	database, err := db.Open(tempDB)
	if err != nil {
		t.Fatalf("DBオープン失敗: %v", err)
	}
	defer database.Close()

	svc := NewService(database, "testpass", "", "folder123", 10080, 10)

	// 初期値の確認
	if interval := svc.GetIntervalMinutes(); interval != 10080 {
		t.Errorf("期待インターバル 10080, 実際: %d", interval)
	}
	if retention := svc.GetRetentionCount(); retention != 10 {
		t.Errorf("期待保持世代 10, 実際: %d", retention)
	}

	// DB書き換え後の反映確認
	if err := database.SetSetting("backup_interval_minutes", "1440"); err != nil {
		t.Fatalf("設定保存失敗: %v", err)
	}
	if err := database.SetSetting("backup_retention_count", "5"); err != nil {
		t.Fatalf("設定保存失敗: %v", err)
	}

	if interval := svc.GetIntervalMinutes(); interval != 1440 {
		t.Errorf("DB反映後インターバル期待 1440, 実際: %d", interval)
	}
	if retention := svc.GetRetentionCount(); retention != 5 {
		t.Errorf("DB反映後保持世代期待 5, 実際: %d", retention)
	}

	// UpdateInterval の動作確認
	svc.UpdateInterval(4320)
	select {
	case updated := <-svc.intervalChan:
		if updated != 4320 {
			t.Errorf("チャネル経由のインターバル期待 4320, 実際: %d", updated)
		}
	default:
		t.Errorf("intervalChan に更新値が送信されていません")
	}
}
