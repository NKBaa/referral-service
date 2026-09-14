package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"referral-service/config"
	"referral-service/model"
	"referral-service/service"
)

type Server struct {
	cfg    *config.Config
	db     *gorm.DB
	worker *service.Worker
	router *gin.Engine
	srv    *http.Server
}

func NewServer(cfg *config.Config, db *gorm.DB, worker *service.Worker) *Server {
	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.Use(gin.Recovery())

	s := &Server{
		cfg:    cfg,
		db:     db,
		worker: worker,
		router: router,
	}
	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// 1. 健康检查端点（Docker Health Check 探针）
	s.router.GET("/health", func(c *gin.Context) {
		sqlDB, err := s.db.DB()
		dbHealthy := true
		if err != nil || sqlDB.Ping() != nil {
			dbHealthy = false
		}

		currentRun := s.worker.GetCurrentRun()
		runID := ""
		if currentRun != nil {
			runID = currentRun.RunID
		}

		status := "ok"
		statusCode := http.StatusOK
		if !dbHealthy {
			status = "degraded"
			statusCode = http.StatusServiceUnavailable
		}

		c.JSON(statusCode, gin.H{
			"status":     status,
			"database":   dbHealthy,
			"is_paused":  s.worker.IsPaused(),
			"current_run": runID,
			"time":       time.Now().Format(time.RFC3339),
		})
	})

	apiGroup := s.router.Group("/api")
	{
		// 2. 只读系统状态监控（原则 25、26、27）
		apiGroup.GET("/status", func(c *gin.Context) {
			currentRun := s.worker.GetCurrentRun()
			c.JSON(http.StatusOK, gin.H{
				"success":   true,
				"config":    s.cfg.MaskedSummary(),
				"is_paused": s.worker.IsPaused(),
				"run":       currentRun,
			})
		})

		// 3. 随时暂停监控（原则 29）
		apiGroup.POST("/pause", func(c *gin.Context) {
			if err := s.worker.Pause(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": err.Error(),
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"message": "Referral monitor paused successfully. Orders created during pause will be ignored.",
			})
		})

		// 4. 恢复监控（原则 30）
		apiGroup.POST("/resume", func(c *gin.Context) {
			if err := s.worker.Resume(); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{
					"success": false,
					"message": err.Error(),
				})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"success": true,
				"message": "Referral monitor resumed successfully. New run cycle and baseline initiated.",
			})
		})

		// 5. 只读查看返佣订单记录（原则 27、32）
		apiGroup.GET("/orders", func(c *gin.Context) {
			page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
			pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
			if page < 1 {
				page = 1
			}
			if pageSize < 1 || pageSize > 100 {
				pageSize = 20
			}

			var orders []model.ReferralOrder
			var total int64

			query := s.db.Model(&model.ReferralOrder{})
			if status := c.Query("status"); status != "" {
				query = query.Where("status = ?", status)
			}
			if runID := c.Query("run_id"); runID != "" {
				query = query.Where("run_id = ?", runID)
			}

			_ = query.Count(&total).Error
			_ = query.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&orders).Error

			c.JSON(http.StatusOK, gin.H{
				"success":   true,
				"total":     total,
				"page":      page,
				"page_size": pageSize,
				"items":     orders,
			})
		})

		// 6. 只读查看审计日志（原则 31、32）
		apiGroup.GET("/audit", func(c *gin.Context) {
			page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
			pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
			if page < 1 {
				page = 1
			}
			if pageSize < 1 || pageSize > 100 {
				pageSize = 20
			}

			var logs []model.AuditLog
			var total int64

			query := s.db.Model(&model.AuditLog{})
			if action := c.Query("action"); action != "" {
				query = query.Where("action = ?", action)
			}

			_ = query.Count(&total).Error
			_ = query.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs).Error

			c.JSON(http.StatusOK, gin.H{
				"success":   true,
				"total":     total,
				"page":      page,
				"page_size": pageSize,
				"items":     logs,
			})
		})

		// 7. 只读查看系统运行日志（原则 33）
		apiGroup.GET("/logs", func(c *gin.Context) {
			page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
			pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
			if page < 1 {
				page = 1
			}
			if pageSize < 1 || pageSize > 100 {
				pageSize = 20
			}

			var logs []model.SystemLog
			var total int64

			query := s.db.Model(&model.SystemLog{})
			if level := c.Query("level"); level != "" {
				query = query.Where("level = ?", level)
			}

			_ = query.Count(&total).Error
			_ = query.Order("id desc").Offset((page - 1) * pageSize).Limit(pageSize).Find(&logs).Error

			c.JSON(http.StatusOK, gin.H{
				"success":   true,
				"total":     total,
				"page":      page,
				"page_size": pageSize,
				"items":     logs,
			})
		})
	}
}

// Start 启动 HTTP 服务
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.HttpHost, s.cfg.HttpPort)
	s.srv = &http.Server{
		Addr:    addr,
		Handler: s.router,
	}

	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.worker.Log("error", "http server listen failed", err.Error())
		}
	}()
	return nil
}

// Stop 优雅关闭 HTTP 服务
func (s *Server) Stop(ctx context.Context) error {
	if s.srv != nil {
		return s.srv.Shutdown(ctx)
	}
	return nil
}
