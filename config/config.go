package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

type Config struct {
	NewApiBaseURL           string          `json:"newapi_base_url"`
	NewApiAccessToken       string          `json:"-"`
	MySQLDSN                string          `json:"-"`
	CommissionRate          decimal.Decimal `json:"commission_rate"`
	PollIntervalSeconds     int             `json:"poll_interval_seconds"`
	TopUpPageSize           int             `json:"topup_page_size"`
	HttpTimeoutSeconds      int             `json:"http_timeout_seconds"`
	RetryMaxAttempts        int             `json:"retry_max_attempts"`
	HttpHost                string          `json:"http_host"`
	HttpPort                int             `json:"http_port"`
	LogLevel                string          `json:"log_level"`
	SystemLogRetentionDays  int             `json:"system_log_retention_days"`
	AuditLogRetentionDays   int             `json:"audit_log_retention_days"`
}

// LoadFromEnv 从环境变量加载所有配置（唯一配置源）
func LoadFromEnv() (*Config, error) {
	cfg := &Config{
		NewApiBaseURL:          strings.TrimRight(strings.TrimSpace(getEnv("NEWAPI_BASE_URL", "http://localhost:3000")), "/"),
		NewApiAccessToken:      strings.TrimSpace(os.Getenv("NEWAPI_ACCESS_TOKEN")),
		MySQLDSN:               strings.TrimSpace(os.Getenv("MYSQL_DSN")),
		PollIntervalSeconds:    getEnvInt("POLL_INTERVAL_SECONDS", 15),
		TopUpPageSize:          getEnvInt("TOPUP_PAGE_SIZE", 50),
		HttpTimeoutSeconds:     getEnvInt("HTTP_TIMEOUT_SECONDS", 10),
		RetryMaxAttempts:       getEnvInt("RETRY_MAX_ATTEMPTS", 3),
		HttpHost:               getEnv("HTTP_HOST", "0.0.0.0"),
		HttpPort:               getEnvInt("HTTP_PORT", 8080),
		LogLevel:               strings.ToLower(getEnv("LOG_LEVEL", "info")),
		SystemLogRetentionDays: getEnvInt("SYSTEM_LOG_RETENTION_DAYS", 30),
		AuditLogRetentionDays:  getEnvInt("AUDIT_LOG_RETENTION_DAYS", 180),
	}

	rateStr := getEnv("COMMISSION_RATE", "0.10")
	rate, err := decimal.NewFromString(rateStr)
	if err != nil || rate.IsNegative() || rate.GreaterThan(decimal.NewFromInt(1)) {
		return nil, fmt.Errorf("invalid COMMISSION_RATE: must be between 0 and 1, got %q", rateStr)
	}
	cfg.CommissionRate = rate

	if cfg.MySQLDSN == "" {
		return nil, errors.New("MYSQL_DSN environment variable is required")
	}

	return cfg, nil
}

// MaskedSummary 生成脱敏的只读配置信息（原则 26/43）
func (c *Config) MaskedSummary() map[string]any {
	tokenStatus := "Not Configured"
	if c.NewApiAccessToken != "" {
		tokenStatus = "Configured"
	}

	return map[string]any{
		"newapi_base_url":           c.NewApiBaseURL,
		"newapi_access_token":       tokenStatus,
		"mysql_dsn":                 MaskDSN(c.MySQLDSN),
		"commission_rate":           c.CommissionRate.String(),
		"poll_interval_seconds":     c.PollIntervalSeconds,
		"topup_page_size":           c.TopUpPageSize,
		"http_timeout_seconds":      c.HttpTimeoutSeconds,
		"retry_max_attempts":        c.RetryMaxAttempts,
		"http_host":                 c.HttpHost,
		"http_port":                 c.HttpPort,
		"log_level":                 c.LogLevel,
		"system_log_retention_days": c.SystemLogRetentionDays,
		"audit_log_retention_days":  c.AuditLogRetentionDays,
	}
}

// MaskDSN 脱敏数据库连接串
func MaskDSN(dsn string) string {
	if dsn == "" {
		return ""
	}
	// 常见格式: user:password@tcp(host:port)/dbname?params
	if atIdx := strings.Index(dsn, "@"); atIdx != -1 {
		rest := dsn[atIdx+1:]
		if qIdx := strings.Index(rest, "?"); qIdx != -1 {
			return rest[:qIdx]
		}
		return rest
	}
	// URL 格式: mysql://user:password@host:port/dbname
	if parsed, err := url.Parse(dsn); err == nil && parsed.Host != "" {
		return parsed.Host + parsed.Path
	}
	return "mysql_configured"
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return strings.TrimSpace(val)
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if intVal, err := strconv.Atoi(strings.TrimSpace(val)); err == nil {
			return intVal
		}
	}
	return defaultVal
}
