package store

import (
	"database/sql"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// Store 包装 *sql.DB 并提供常用辅助方法
type Store struct{ DB *sql.DB }

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("连接数据库失败: %w", err)
	}
	return &Store{DB: db}, nil
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS users (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		username VARCHAR(64) NOT NULL UNIQUE,
		email VARCHAR(190) NOT NULL DEFAULT '',
		password_hash VARCHAR(255) NOT NULL,
		role ENUM('admin','user') NOT NULL DEFAULT 'user',
		enabled TINYINT NOT NULL DEFAULT 1,
		failed_count INT NOT NULL DEFAULT 0,
		failed_at DATETIME NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_login_at DATETIME NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS sessions (
		token CHAR(64) PRIMARY KEY,
		user_id BIGINT UNSIGNED NOT NULL,
		ip VARCHAR(64) NOT NULL DEFAULT '',
		ua VARCHAR(255) NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NOT NULL,
		INDEX idx_user (user_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS monitor_targets (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		user_id BIGINT UNSIGNED NOT NULL,
		name VARCHAR(128) NOT NULL,
		target_type VARCHAR(16) NOT NULL DEFAULT 'http',
		url VARCHAR(512) NOT NULL,
		expect_status INT NOT NULL DEFAULT 200,
		keyword VARCHAR(255) NOT NULL DEFAULT '',
		interval_sec INT NOT NULL DEFAULT 60,
		timeout_sec INT NOT NULL DEFAULT 10,
		notify_emails TEXT,
		notify_recovery TINYINT NOT NULL DEFAULT 1,
		public TINYINT NOT NULL DEFAULT 1,
		icon VARCHAR(512) NOT NULL DEFAULT '',
		enabled TINYINT NOT NULL DEFAULT 1,
		maintenance_until DATETIME NULL,
		status ENUM('up','down','unknown') NOT NULL DEFAULT 'unknown',
		last_check_at DATETIME NULL,
		last_status_code INT NULL,
		last_ms INT NULL,
		last_cert_days INT NULL,
		fail_streak INT NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_user (user_id),
		INDEX idx_due (enabled, last_check_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS checks (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		target_id BIGINT UNSIGNED NOT NULL,
		user_id BIGINT UNSIGNED NOT NULL,
		ok TINYINT NOT NULL,
		status_code INT NOT NULL DEFAULT 0,
		ms INT NOT NULL DEFAULT 0,
		error VARCHAR(512) NOT NULL DEFAULT '',
		checked_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_target_time (target_id, checked_at),
		INDEX idx_user_time (user_id, checked_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS api_tokens (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		user_id BIGINT UNSIGNED NOT NULL,
		name VARCHAR(128) NOT NULL,
		token CHAR(48) NOT NULL UNIQUE,
		last_used_at DATETIME NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_user (user_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS logs (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		user_id BIGINT UNSIGNED NOT NULL,
		source VARCHAR(128) NOT NULL DEFAULT 'web',
		level ENUM('debug','info','warn','error','fatal') NOT NULL DEFAULT 'info',
		message TEXT NOT NULL,
		ctx JSON NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_user_time (user_id, created_at),
		INDEX idx_user_level (user_id, level, created_at),
		INDEX idx_source (user_id, source)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS anomalies (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		user_id BIGINT UNSIGNED NOT NULL,
		type ENUM('check_down','check_recovery','latency_spike','log_burst','external','cert_expiring') NOT NULL,
		severity ENUM('critical','warning') NOT NULL DEFAULT 'warning',
		target_id BIGINT UNSIGNED NULL,
		source VARCHAR(128) NOT NULL DEFAULT '',
		title VARCHAR(255) NOT NULL,
		detail TEXT NOT NULL,
		llm_analysis TEXT NULL,
		llm_at DATETIME NULL,
		ai_decision VARCHAR(32) NOT NULL DEFAULT '',
		status ENUM('open','resolved') NOT NULL DEFAULT 'open',
		notified TINYINT NOT NULL DEFAULT 0,
		resolved_at DATETIME NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_user_status (user_id, status),
		INDEX idx_user_time (user_id, created_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS llm_conversations (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		user_id BIGINT UNSIGNED NOT NULL,
		title VARCHAR(190) NOT NULL DEFAULT '新对话',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_user (user_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS llm_messages (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		conv_id BIGINT UNSIGNED NOT NULL,
		role ENUM('user','assistant','system') NOT NULL,
		content MEDIUMTEXT NOT NULL,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_conv (conv_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS mail_queue (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		user_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
		to_addr VARCHAR(190) NOT NULL,
		subject VARCHAR(255) NOT NULL,
		body_html MEDIUMTEXT NOT NULL,
		body_text MEDIUMTEXT,
		status ENUM('pending','sent','failed') NOT NULL DEFAULT 'pending',
		attempts INT NOT NULL DEFAULT 0,
		next_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		last_error VARCHAR(512) NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_status (status, next_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS mail_log (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		to_addr VARCHAR(190) NOT NULL,
		subject VARCHAR(255) NOT NULL DEFAULT '',
		ok TINYINT NOT NULL,
		error VARCHAR(512) NOT NULL DEFAULT '',
		sent_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_time (sent_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`,
	`CREATE TABLE IF NOT EXISTS settings (
		skey VARCHAR(64) PRIMARY KEY,
		svalue TEXT NOT NULL
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
	`CREATE TABLE IF NOT EXISTS ai_reports (
		id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
		user_id BIGINT UNSIGNED NOT NULL,
		kind ENUM('daily','weekly') NOT NULL DEFAULT 'daily',
		title VARCHAR(255) NOT NULL,
		content MEDIUMTEXT NOT NULL,
		metrics JSON NULL,
		status ENUM('pending','done','failed') NOT NULL DEFAULT 'pending',
		error VARCHAR(512) NOT NULL DEFAULT '',
		want_email TINYINT NOT NULL DEFAULT 0,
		sent TINYINT NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_user_time (user_id, created_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
}

func (s *Store) Migrate() error {
	for _, ddl := range schema {
		if _, err := s.DB.Exec(ddl); err != nil {
			return fmt.Errorf("执行建表语句失败: %w", err)
		}
	}
	// 存量库升级：monitor_targets 补 public 列（已存在则忽略 1060 错误）
	if _, err := s.DB.Exec(`ALTER TABLE monitor_targets ADD COLUMN public TINYINT NOT NULL DEFAULT 1`); err != nil {
		if !strings.Contains(err.Error(), "Duplicate column") {
			return fmt.Errorf("升级 monitor_targets.public 失败: %w", err)
		}
	}
	// 存量库升级：monitor_targets 补 icon 列（站点图标 URL，用于公开状态页展示）
	if _, err := s.DB.Exec(`ALTER TABLE monitor_targets ADD COLUMN icon VARCHAR(512) NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "Duplicate column") {
			return fmt.Errorf("升级 monitor_targets.icon 失败: %w", err)
		}
	}
	// 存量库升级：mail_queue 补 body_text 列（纯文本版本，用于 multipart/alternative）
	if _, err := s.DB.Exec(`ALTER TABLE mail_queue ADD COLUMN body_text MEDIUMTEXT`); err != nil {
		if !strings.Contains(err.Error(), "Duplicate column") {
			return fmt.Errorf("升级 mail_queue.body_text 失败: %w", err)
		}
	}
	// 存量库升级：anomalies 补 ai_decision 列（AI 自动决策结果：auto_resolve/watch/manual）
	if _, err := s.DB.Exec(`ALTER TABLE anomalies ADD COLUMN ai_decision VARCHAR(32) NOT NULL DEFAULT ''`); err != nil {
		if !strings.Contains(err.Error(), "Duplicate column") {
			return fmt.Errorf("升级 anomalies.ai_decision 失败: %w", err)
		}
	}
	// 存量库升级：monitor_targets 补 target_type 列（http / tcp 端口监测）
	if _, err := s.DB.Exec(`ALTER TABLE monitor_targets ADD COLUMN target_type VARCHAR(16) NOT NULL DEFAULT 'http'`); err != nil {
		if !strings.Contains(err.Error(), "Duplicate column") {
			return fmt.Errorf("升级 monitor_targets.target_type 失败: %w", err)
		}
	}
	// 存量库升级：monitor_targets 补 maintenance_until 列（维护模式，期间冻结状态不告警）
	if _, err := s.DB.Exec(`ALTER TABLE monitor_targets ADD COLUMN maintenance_until DATETIME NULL`); err != nil {
		if !strings.Contains(err.Error(), "Duplicate column") {
			return fmt.Errorf("升级 monitor_targets.maintenance_until 失败: %w", err)
		}
	}
	// 存量库升级：monitor_targets 补 last_cert_days 列（最近一次探测的证书剩余天数）
	if _, err := s.DB.Exec(`ALTER TABLE monitor_targets ADD COLUMN last_cert_days INT NULL`); err != nil {
		if !strings.Contains(err.Error(), "Duplicate column") {
			return fmt.Errorf("升级 monitor_targets.last_cert_days 失败: %w", err)
		}
	}
	// 存量库升级：anomalies.type 枚举补 cert_expiring（SSL 证书到期异常）
	if _, err := s.DB.Exec(`ALTER TABLE anomalies MODIFY COLUMN type ENUM('check_down','check_recovery','latency_spike','log_burst','external','cert_expiring') NOT NULL`); err != nil {
		return fmt.Errorf("升级 anomalies.type 枚举失败: %w", err)
	}
	return nil
}

// Seed 写入默认设置（仅当表为空时）
func (s *Store) Seed(appName, baseURL string, llmKey string) error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		// 存量环境：仅补写后续版本新增的键（INSERT IGNORE 不动已有值）
		for k, v := range newKeys {
			if _, err := s.DB.Exec(`INSERT IGNORE INTO settings (skey, svalue) VALUES (?, ?)`, k, v); err != nil {
				return err
			}
		}
		return nil
	}
	defaults := map[string]string{
		"app_name":            appName,
		"base_url":            baseURL,
		"llm_base_url":        "",
		"llm_api_key":         llmKey,
		"llm_model":           "",
		"llm_enabled":         "1",
		"smtp_host":           "",
		"smtp_port":           "465",
		"smtp_mode":           "ssl",
		"smtp_user":           "",
		"smtp_pass":           "",
		"smtp_from_name":      appName,
		"default_notify_emails": "",
		"log_burst_threshold": "10",
		"latency_multiplier":  "3",
		"ai_auto_resolve":     "1",
		"ssl_warn_days":       "7",
		"webhook_type":        "feishu",
		"webhook_url":         "",
	}
	for k, v := range defaults {
		if _, err := s.DB.Exec(`INSERT IGNORE INTO settings (skey, svalue) VALUES (?, ?)`, k, v); err != nil {
			return err
		}
	}
	log.Printf("[seed] 默认设置已写入 (%d 项)", len(defaults))
	return nil
}

// newKeys 后续版本新增的默认设置（存量环境启动时补写，不覆盖已有值）
var newKeys = map[string]string{
	"webhook_type":    "feishu",
	"webhook_url":     "",
	"ai_auto_resolve": "1",
	"ssl_warn_days":   "7",
	"report_auto":     "0",
	"report_hour":     "8",
}

// EnsureAdmin 若无任何用户则创建初始 admin
func (s *Store) EnsureAdmin(username, passwordHash string) (bool, error) {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false, err
	}
	if n > 0 {
		return false, nil
	}
	_, err := s.DB.Exec(`INSERT INTO users (username, email, password_hash, role) VALUES (?, '', ?, 'admin')`,
		username, passwordHash)
	if err != nil {
		return false, err
	}
	log.Printf("[seed] 初始管理员已创建: %s", username)
	return true, nil
}

func (s *Store) Exec(query string, args ...any) (sql.Result, error) {
	return s.DB.Exec(query, args...)
}

func (s *Store) Query(query string, args ...any) (*sql.Rows, error) {
	return s.DB.Query(query, args...)
}

func (s *Store) QueryRow(query string, args ...any) *sql.Row {
	return s.DB.QueryRow(query, args...)
}

// GetSetting 读取设置，不存在时返回 def
func (s *Store) GetSetting(key, def string) string {
	var v string
	if err := s.DB.QueryRow(`SELECT svalue FROM settings WHERE skey = ?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

// GetIntSetting 读取整型设置
func (s *Store) GetIntSetting(key string, def int) int {
	v := s.GetSetting(key, "")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings (skey, svalue) VALUES (?, ?)
		ON DUPLICATE KEY UPDATE svalue = VALUES(svalue)`, key, value)
	return err
}

// AllSettings 返回全部设置
func (s *Store) AllSettings() map[string]string {
	rows, err := s.DB.Query(`SELECT skey, svalue FROM settings`)
	if err != nil {
		return map[string]string{}
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if rows.Scan(&k, &v) == nil {
			m[k] = v
		}
	}
	return m
}
