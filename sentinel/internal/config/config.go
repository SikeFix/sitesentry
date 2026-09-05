package config

import (
	"encoding/json"
	"fmt"
	"os"
)

type DBConfig struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	User string `json:"user"`
	Pass string `json:"pass"`
	Name string `json:"name"`
}

type Config struct {
	Listen       string   `json:"listen"`
	DB           DBConfig `json:"db"`
	WebDir       string   `json:"web_dir"`
	AppName      string   `json:"app_name"`
	BaseURL      string   `json:"base_url"`
	CheckTickSec int      `json:"check_tick_sec"`
	LLMAPIKey    string   `json:"llm_api_key"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:33330"
	}
	if cfg.WebDir == "" {
		cfg.WebDir = "web"
	}
	if cfg.AppName == "" {
		cfg.AppName = "SiteSentry 哨兵"
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "http://127.0.0.1:33330"
	}
	if cfg.CheckTickSec < 10 {
		cfg.CheckTickSec = 30
	}
	if cfg.DB.Port == 0 {
		cfg.DB.Port = 3306
	}
	// LLM API Key 优先读环境变量，其次读配置文件（避免密钥写死在代码里）
	if env := os.Getenv("SENTINEL_LLM_API_KEY"); env != "" {
		cfg.LLMAPIKey = env
	}
	return cfg, nil
}

// DSN 返回 MySQL 连接串
func (c *Config) DSN() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=true&loc=Local&timeout=10s&readTimeout=30s&writeTimeout=30s",
		c.DB.User, c.DB.Pass, c.DB.Host, c.DB.Port, c.DB.Name)
}
