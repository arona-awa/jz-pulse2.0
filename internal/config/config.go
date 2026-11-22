// Package config 负责全局配置的加载与持久化。
// 安装状态通过 data/config.json 标记，首次启动（无该文件）自动进入安装向导。
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Version 当前应用版本号，用于检查更新
const Version = "2.1.62"

// Cfg 全局配置单例
var Cfg Config

// Config 持久化到 data/config.json 的配置项
type Config struct {
	Installed  bool   `json:"installed"`   // 是否已完成安装
	DBType     string `json:"db_type"`     // sqlite | mysql
	SQLitePath string `json:"sqlite_path"` // SQLite 文件路径
	MySQLDsn   string `json:"mysql_dsn"`   // MySQL DSN
	Secret     string `json:"secret"`      // 会话签名密钥
	DataDir    string `json:"data_dir"`    // 数据目录
	UploadDir  string `json:"upload_dir"`  // 上传目录
	Version    string `json:"version"`     // 安装时的程序版本
}

// dir 返回数据目录（默认 ./data）
func (c *Config) dir() string {
	if c.DataDir == "" {
		return "data"
	}
	return c.DataDir
}

// File 配置文件完整路径
func (c *Config) File() string {
	return filepath.Join(c.dir(), "config.json")
}

// Load 启动时加载配置；文件不存在则保持未安装状态。
// 目录可用环境变量覆盖，方便单二进制部署到任意位置：
//
//	CLEARLOVE_DATA_DIR   data 目录（默认 ./data）
//	CLEARLOVE_UPLOAD_DIR uploads 目录（默认 ./uploads）
func Load() {
	Cfg = Config{DataDir: "data", UploadDir: "uploads", Version: Version}
	// 先用环境变量确定目录，才能在正确的路径读取 config.json
	applyEnvDirs()
	if b, err := os.ReadFile(Cfg.File()); err == nil {
		_ = json.Unmarshal(b, &Cfg)
	}
	if Cfg.DataDir == "" {
		Cfg.DataDir = "data"
	}
	if Cfg.UploadDir == "" {
		Cfg.UploadDir = "uploads"
	}
	applyEnvDirs() // 环境变量优先级最高
}

// applyEnvDirs 应用目录类环境变量
func applyEnvDirs() {
	if v := os.Getenv("CLEARLOVE_DATA_DIR"); v != "" {
		Cfg.DataDir = v
	}
	if v := os.Getenv("CLEARLOVE_UPLOAD_DIR"); v != "" {
		Cfg.UploadDir = v
	}
}

// Save 将配置写回磁盘（安装完成、升级等场景）
func Save() error {
	if err := os.MkdirAll(Cfg.dir(), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(&Cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(Cfg.File(), b, 0o600)
}
