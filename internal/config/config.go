package config

import (
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Port                     string
	DatabasePath             string
	ResendAPIKey             string
	DefaultDomain            string
	AdminUsername            string
	AdminPassword            string
	SessionSecret            string
	BackupEncryptionKey      string
	GDriveServiceAccountJSON string
	GDriveFolderID           string
	BackupIntervalMinutes    int
	BackupRetentionCount     int
}

func Load() *Config {
	// .envファイルが存在すれば読み込む
	_ = godotenv.Load()

	cfg := &Config{
		Port:                     getEnv("PORT", "8080"),
		DatabasePath:             getEnv("DATABASE_PATH", "./data/mailer.db"),
		ResendAPIKey:             getEnv("RESEND_API_KEY", ""),
		DefaultDomain:            getEnv("DEFAULT_DOMAIN", ""),
		AdminUsername:            getEnv("ADMIN_USERNAME", "admin"),
		AdminPassword:            getEnv("ADMIN_PASSWORD", "admin123"),
		SessionSecret:            getEnv("SESSION_SECRET", "resend-server-secret-key-change-me"),
		BackupEncryptionKey:      getEnv("BACKUP_ENCRYPTION_KEY", "resend-backup-passphrase-change-me"),
		GDriveServiceAccountJSON: getEnv("GDRIVE_SERVICE_ACCOUNT_JSON", "./data/gdrive-service-account.json"),
		GDriveFolderID:           getEnv("GDRIVE_FOLDER_ID", ""),
		BackupIntervalMinutes:    getEnvInt("BACKUP_INTERVAL_MINUTES", 10080),
		BackupRetentionCount:     getEnvInt("BACKUP_RETENTION_COUNT", 10),
	}

	return cfg
}

func getEnv(key, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		return val
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok && val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}
