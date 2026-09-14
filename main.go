package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"referral-service/api"
	"referral-service/config"
	"referral-service/model"
	"referral-service/service"
)

func main() {
	log.Println("Starting Referral Service...")

	// 1. 从环境变量加载配置（原则 18、21、23）
	cfg, err := config.LoadFromEnv()
	if err != nil {
		log.Fatalf("Fatal configuration error: %v", err)
	}

	// 2. 初始化 MySQL 数据库（原则 17：“只使用 MySQL，不可用则不允许进入 Running 状态”）
	db, err := model.InitDB(cfg.MySQLDSN)
	if err != nil {
		log.Fatalf("Fatal database error: %v", err)
	}
	log.Printf("MySQL database initialized successfully (%s)", config.MaskDSN(cfg.MySQLDSN))

	// 3. 初始化 New API 客户端
	client := service.NewNewApiClient(cfg.NewApiBaseURL, cfg.NewApiAccessToken, cfg.HttpTimeoutSeconds)

	// 4. 初始化核心 Worker 引擎
	worker := service.NewWorker(cfg, db, client)
	if err := worker.Start(); err != nil {
		log.Fatalf("Fatal: failed to start referral worker: %v", err)
	}
	log.Println("Referral Worker started successfully")

	// 5. 初始化并启动日志清理器（原则 34）
	cleaner := service.NewCleaner(cfg, db)
	cleaner.Start()

	// 6. 初始化并启动 HTTP 服务（含 /health 探针及控制端点）
	server := api.NewServer(cfg, db, worker)
	if err := server.Start(); err != nil {
		log.Fatalf("Fatal: failed to start HTTP server: %v", err)
	}
	log.Printf("HTTP Server listening on %s:%d", cfg.HttpHost, cfg.HttpPort)

	// 7. 监听系统信号实现优雅关机
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down Referral Service...")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	_ = server.Stop(shutdownCtx)
	cleaner.Stop()
	worker.Stop()

	log.Println("Referral Service stopped successfully.")
}
