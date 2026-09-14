package service

import (
	"context"
	"log"
	"time"

	"gorm.io/gorm"

	"referral-service/config"
	"referral-service/model"
)

type Cleaner struct {
	cfg    *config.Config
	db     *gorm.DB
	ctx    context.Context
	cancel context.CancelFunc
}

func NewCleaner(cfg *config.Config, db *gorm.DB) *Cleaner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Cleaner{
		cfg:    cfg,
		db:     db,
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start 启动定时清理协程（每天执行一次，原则 34）
func (c *Cleaner) Start() {
	go func() {
		// 启动后首次清理
		c.clean()

		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()

		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				c.clean()
			}
		}
	}()
}

// Stop 停止清理任务
func (c *Cleaner) Stop() {
	c.cancel()
}

func (c *Cleaner) clean() {
	// 1. 清理 system_logs（原则 34）
	if c.cfg.SystemLogRetentionDays > 0 {
		sysCutoff := time.Now().AddDate(0, 0, -c.cfg.SystemLogRetentionDays)
		res := c.db.Where("created_at < ?", sysCutoff).Delete(&model.SystemLog{})
		if res.Error == nil && res.RowsAffected > 0 {
			log.Printf("[CLEANER] Purged %d expired system logs older than %d days", res.RowsAffected, c.cfg.SystemLogRetentionDays)
		}
	}

	// 2. 清理 audit_logs（原则 34）
	if c.cfg.AuditLogRetentionDays > 0 {
		auditCutoff := time.Now().AddDate(0, 0, -c.cfg.AuditLogRetentionDays)
		res := c.db.Where("created_at < ?", auditCutoff).Delete(&model.AuditLog{})
		if res.Error == nil && res.RowsAffected > 0 {
			log.Printf("[CLEANER] Purged %d expired audit logs older than %d days", res.RowsAffected, c.cfg.AuditLogRetentionDays)
		}
	}

	// referral_orders 与 service_runs 永久保留，禁止自动清理（原则 34）
}
