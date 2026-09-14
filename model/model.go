package model

import (
	"fmt"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

var DB *gorm.DB

// 订单处理状态常量
const (
	OrderStatusSuccess                  = "success"
	OrderStatusPending                  = "pending"
	OrderStatusProcessing               = "processing"
	OrderStatusFailed                   = "failed"
	OrderStatusSkippedNoInviter         = "skipped_no_inviter"
	OrderStatusSkippedInviterNotFound   = "skipped_inviter_not_found"
	OrderStatusSkippedUnsupportedProvider = "skipped_unsupported_provider"
	OrderStatusSkippedZeroReward        = "skipped_zero_reward"
	OrderStatusAbandonedRestart         = "abandoned_restart"
)

// 运行状态常量
const (
	RunStatusRunning = "running"
	RunStatusPaused  = "paused"
	RunStatusStopped = "stopped"
)

// 审计操作常量
const (
	AuditActionServiceStarted     = "service.started"
	AuditActionServiceStopped     = "service.stopped"
	AuditActionServiceNewRun      = "service.new_run"
	AuditActionServiceNewBaseline = "service.new_baseline"
	AuditActionMonitorPause       = "monitor.pause"
	AuditActionMonitorResume      = "monitor.resume"
	AuditActionRewardSuccess      = "reward.success"
	AuditActionRewardFailed       = "reward.failed"
	AuditActionRewardSkipped      = "reward.skipped"
)

// ServiceRun 运行周期记录（原则 7、19）
type ServiceRun struct {
	ID              uint       `json:"id" gorm:"primaryKey;autoIncrement"`
	RunID           string     `json:"run_id" gorm:"type:varchar(64);uniqueIndex;not null"`
	BaselineTopUpID int64      `json:"baseline_topup_id" gorm:"column:baseline_top_up_id;not null;default:0"`
	MaxSeenTopUpID  int64      `json:"max_seen_topup_id" gorm:"column:max_seen_top_up_id;not null;default:0"`
	Status          string     `json:"status" gorm:"type:varchar(32);not null;default:'running'"`
	StartedAt       time.Time  `json:"started_at" gorm:"not null"`
	StoppedAt       *time.Time `json:"stopped_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// ReferralOrder 充值返佣订单事实记录（原则 19、32）
type ReferralOrder struct {
	ID              uint            `json:"id" gorm:"primaryKey;autoIncrement"`
	RunID           string          `json:"run_id" gorm:"type:varchar(64);index;not null"`
	TopUpID         int64           `json:"topup_id" gorm:"column:top_up_id;uniqueIndex;not null"`
	TradeNo         string          `json:"trade_no" gorm:"type:varchar(255);index"`
	InviteeID       int             `json:"invitee_id" gorm:"index;not null"`
	InviterID       int             `json:"inviter_id" gorm:"index;not null"`
	PaymentProvider string          `json:"payment_provider" gorm:"type:varchar(64);not null"`
	PaymentMethod   string          `json:"payment_method" gorm:"type:varchar(64)"`
	Amount          int64           `json:"amount"`
	Money           decimal.Decimal `json:"money" gorm:"type:decimal(16,4)"`
	TopUpStatus     string          `json:"topup_status" gorm:"column:top_up_status;type:varchar(32);not null"`
	CreditedQuota   int64           `json:"credited_quota" gorm:"not null"`
	CommissionRate  decimal.Decimal `json:"commission_rate" gorm:"type:decimal(8,4);not null"`
	RewardQuota     int64           `json:"reward_quota" gorm:"not null"`
	Status          string          `json:"status" gorm:"type:varchar(64);index;not null"`
	Reason          string          `json:"reason" gorm:"type:text"`
	RetryCount      int             `json:"retry_count" gorm:"not null;default:0"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
}

// AuditLog 审计日志（原则 31、32）
type AuditLog struct {
	ID        uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	Action    string    `json:"action" gorm:"type:varchar(64);index;not null"`
	Details   string    `json:"details" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at" gorm:"index;not null"`
}

// SystemLog 系统运行日志（原则 32、33）
type SystemLog struct {
	ID        uint      `json:"id" gorm:"primaryKey;autoIncrement"`
	Level     string    `json:"level" gorm:"type:varchar(16);index;not null"`
	Message   string    `json:"message" gorm:"type:varchar(512);not null"`
	Details   string    `json:"details" gorm:"type:text"`
	CreatedAt time.Time `json:"created_at" gorm:"index;not null"`
}

// InitDB 初始化 MySQL 数据库连接并自动迁移表结构（原则 17）
func InitDB(dsn string) (*gorm.DB, error) {
	if dsn == "" {
		return nil, fmt.Errorf("MySQL DSN is empty")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MySQL: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get underlying sql.DB: %w", err)
	}

	// 连接池设置
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(50)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// 测试连通性
	if err := sqlDB.Ping(); err != nil {
		return nil, fmt.Errorf("MySQL ping failed: %w", err)
	}

	// 自动建表与表结构迁移
	err = db.AutoMigrate(
		&ServiceRun{},
		&ReferralOrder{},
		&AuditLog{},
		&SystemLog{},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to auto migrate tables: %w", err)
	}

	DB = db
	return db, nil
}
