package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/galdlore/resend-server/internal/auth"
	"github.com/galdlore/resend-server/internal/backup"
	"github.com/galdlore/resend-server/internal/config"
	"github.com/galdlore/resend-server/internal/db"
	"github.com/galdlore/resend-server/internal/handlers"
	"github.com/galdlore/resend-server/internal/mailer"
)

func main() {
	log.Println("Resend Server を起動しています...")

	// 1. 設定の読み込み
	cfg := config.Load()

	// 2. データベース接続
	database, err := db.Open(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("データベースの初期化に失敗しました: %v", err)
	}
	defer database.Close()
	log.Printf("データベースを初期化しました: %s", cfg.DatabasePath)

	// 3. 各種サービスの初期化
	authMgr := auth.NewManager(cfg.AdminUsername, cfg.AdminPassword, cfg.SessionSecret)
	mailerClient := mailer.New(cfg.ResendAPIKey)
	backupSvc := backup.NewService(
		database,
		cfg.BackupEncryptionKey,
		cfg.GDriveServiceAccountJSON,
		cfg.GDriveFolderID,
		cfg.BackupIntervalMinutes,
	)

	// 定期バックアップの開始
	backupSvc.StartScheduler()
	defer backupSvc.Stop()

	// 4. HTTPハンドラーとルーティングの構築
	srvHandler, err := handlers.NewServer(cfg, database, authMgr, mailerClient, backupSvc)
	if err != nil {
		log.Fatalf("サーバーハンドラーの初期化に失敗しました: %v", err)
	}

	mux := http.NewServeMux()
	srvHandler.RegisterRoutes(mux)

	// 認証ミドルウェアの適用
	handlerWithAuth := authMgr.Middleware(mux)

	// 5. HTTPサーバーの起動設定
	addr := ":" + cfg.Port
	httpServer := &http.Server{
		Addr:         addr,
		Handler:      handlerWithAuth,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// シャットダウン監視用ゴルーチン
	stopChan := make(chan os.Signal, 1)
	signal.Notify(stopChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("サーバーをポート %s で待機開始しました (http://localhost:%s)", cfg.Port, cfg.Port)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("サーバー待機中にエラーが発生しました: %v", err)
		}
	}()

	<-stopChan
	log.Println("シャットダウンシグナルを受信しました。終了処理を開始します...")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("サーバーの安全な終了中にエラーが発生しました: %v", err)
	} else {
		log.Println("サーバーが安全に終了しました")
	}
}
