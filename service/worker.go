package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"referral-service/config"
	"referral-service/model"
)

type Worker struct {
	cfg         *config.Config
	db          *gorm.DB
	client      *NewApiClient
	currentRun  *model.ServiceRun
	runMu       sync.RWMutex
	isPaused    bool
	pauseMu     sync.RWMutex
	cancelFunc  context.CancelFunc
	workerWg    sync.WaitGroup
	ctx         context.Context
}

func NewWorker(cfg *config.Config, db *gorm.DB, client *NewApiClient) *Worker {
	return &Worker{
		cfg:    cfg,
		db:     db,
		client: client,
	}
}

// Log 记录日志（同时输出到 stdout 与 MySQL system_logs，原则 33）
func (w *Worker) Log(level, message, details string) {
	// 1. stdout 输出
	log.Printf("[%s] %s: %s", level, message, details)

	// 2. MySQL system_logs 存储
	sysLog := &model.SystemLog{
		Level:     level,
		Message:   message,
		Details:   details,
		CreatedAt: time.Now(),
	}
	_ = w.db.Create(sysLog).Error
}

// Audit 记录审计操作（原则 31）
func (w *Worker) Audit(action, details string) {
	audit := &model.AuditLog{
		Action:    action,
		Details:   details,
		CreatedAt: time.Now(),
	}
	_ = w.db.Create(audit).Error
	log.Printf("[AUDIT] %s: %s", action, details)
}

// Start 启动 Worker 服务（原则 6、7）
func (w *Worker) Start() error {
	w.Audit(model.AuditActionServiceStarted, "Referral service is starting up")

	// 1. 废弃所有旧 Run 中未完结的订单（原则 7）
	if err := w.abandonUnfinishedOrders("abandoned upon service start/restart"); err != nil {
		w.Log("error", "failed to abandon unfinished orders", err.Error())
		return err
	}

	// 2. 将之前处于 running 状态的旧 Run 标记为 stopped
	now := time.Now()
	w.db.Model(&model.ServiceRun{}).Where("status = ?", model.RunStatusRunning).Updates(map[string]any{
		"status":     model.RunStatusStopped,
		"stopped_at": now,
	})

	// 3. 建立新的运行周期 Run（原则 7）
	if err := w.initiateNewRun(); err != nil {
		return err
	}

	w.ctx, w.cancelFunc = context.WithCancel(context.Background())

	// 4. 启动轮询协程
	w.workerWg.Add(1)
	go w.pollLoop()

	return nil
}

// Stop 停止 Worker 服务
func (w *Worker) Stop() {
	w.Audit(model.AuditActionServiceStopped, "Referral service is shutting down")
	if w.cancelFunc != nil {
		w.cancelFunc()
	}

	w.runMu.Lock()
	if w.currentRun != nil {
		now := time.Now()
		w.currentRun.Status = model.RunStatusStopped
		w.currentRun.StoppedAt = &now
		w.db.Save(w.currentRun)
	}
	w.runMu.Unlock()

	w.workerWg.Wait()
}

// Pause 暂停服务（原则 29）
func (w *Worker) Pause() error {
	w.pauseMu.Lock()
	defer w.pauseMu.Unlock()

	if w.isPaused {
		return nil
	}

	w.Audit(model.AuditActionMonitorPause, "Referral monitor paused by admin")
	w.isPaused = true

	// 废弃当前 Run 中未完结的订单（原则 7、29）
	_ = w.abandonUnfinishedOrders("abandoned upon monitor pause")

	w.runMu.Lock()
	if w.currentRun != nil {
		now := time.Now()
		w.currentRun.Status = model.RunStatusPaused
		w.currentRun.StoppedAt = &now
		w.db.Save(w.currentRun)
	}
	w.runMu.Unlock()

	w.Log("info", "monitor paused", "Order polling has stopped. Orders created during pause will be ignored.")
	return nil
}

// Resume 恢复服务（原则 30）
func (w *Worker) Resume() error {
	w.pauseMu.Lock()
	defer w.pauseMu.Unlock()

	if !w.isPaused {
		return nil
	}

	w.Audit(model.AuditActionMonitorResume, "Referral monitor resumed, starting new run cycle")

	// 视为新的运行周期：重新获取最大 TopUp ID 作为新 baseline（原则 30）
	if err := w.initiateNewRun(); err != nil {
		w.Log("error", "failed to initiate new run on resume", err.Error())
		return err
	}

	w.isPaused = false
	w.Log("info", "monitor resumed", fmt.Sprintf("New run %s started with baseline %d", w.currentRun.RunID, w.currentRun.BaselineTopUpID))
	return nil
}

// IsPaused 查询当前是否处于暂停状态
func (w *Worker) IsPaused() bool {
	w.pauseMu.RLock()
	defer w.pauseMu.RUnlock()
	return w.isPaused
}

// GetCurrentRun 获取当前运行周期信息
func (w *Worker) GetCurrentRun() *model.ServiceRun {
	w.runMu.RLock()
	defer w.runMu.RUnlock()
	return w.currentRun
}

// abandonUnfinishedOrders 将所有未完成的旧订单标记为 abandoned_restart（原则 7）
func (w *Worker) abandonUnfinishedOrders(reason string) error {
	result := w.db.Model(&model.ReferralOrder{}).
		Where("status IN ?", []string{model.OrderStatusPending, model.OrderStatusProcessing, model.OrderStatusFailed}).
		Updates(map[string]any{
			"status": model.OrderStatusAbandonedRestart,
			"reason": reason,
		})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected > 0 {
		w.Log("warn", "abandoned unfinished orders", fmt.Sprintf("Marked %d orders as %s", result.RowsAffected, model.OrderStatusAbandonedRestart))
	}
	return nil
}

// initiateNewRun 创建新的运行 Run 并获取最新 baseline（原则 6、7、30）
func (w *Worker) initiateNewRun() error {
	w.runMu.Lock()
	defer w.runMu.Unlock()

	maxID, err := w.client.GetMaxTopUpID()
	if err != nil {
		w.Log("error", "failed to fetch baseline topup id from New API", err.Error())
		return fmt.Errorf("failed to fetch baseline topup id: %w", err)
	}

	runID := fmt.Sprintf("run-%s-%s", time.Now().Format("20060102150405"), uuid.New().String()[:8])
	newRun := &model.ServiceRun{
		RunID:           runID,
		BaselineTopUpID: maxID,
		MaxSeenTopUpID:  maxID,
		Status:          model.RunStatusRunning,
		StartedAt:       time.Now(),
	}

	if err := w.db.Create(newRun).Error; err != nil {
		return fmt.Errorf("failed to create new service run: %w", err)
	}

	w.currentRun = newRun

	w.Audit(model.AuditActionServiceNewRun, fmt.Sprintf("Created run %s", runID))
	w.Audit(model.AuditActionServiceNewBaseline, fmt.Sprintf("Run %s established baseline_topup_id = %d", runID, maxID))
	w.Log("info", "new run initiated", fmt.Sprintf("RunID: %s, BaselineTopUpID: %d", runID, maxID))
	return nil
}

// pollLoop 轮询主循环
func (w *Worker) pollLoop() {
	defer w.workerWg.Done()

	ticker := time.NewTicker(time.Duration(w.cfg.PollIntervalSeconds) * time.Second)
	defer ticker.Stop()

	// 启动后立即执行一次轮询检查
	w.executePoll()

	for {
		select {
		case <-w.ctx.Done():
			return
		case <-ticker.C:
			w.executePoll()
		}
	}
}

// executePoll 执行单次轮询
func (w *Worker) executePoll() {
	if w.IsPaused() {
		return
	}

	w.runMu.RLock()
	currentRun := w.currentRun
	w.runMu.RUnlock()

	if currentRun == nil {
		return
	}

	// 拉取最近的订单（New API 默认以 ID desc 返回）
	topups, _, err := w.client.GetTopUps(1, w.cfg.TopUpPageSize)
	if err != nil {
		if errors.Is(err, ErrAuthInvalid) {
			w.Log("error", "authentication error with New API", "Please check NEWAPI_ACCESS_TOKEN configuration (401/403)")
		} else {
			w.Log("warn", "poll failed", err.Error())
		}
		return
	}

	if len(topups) == 0 {
		return
	}

	// 过滤出大于 baseline_topup_id 的新订单（原则 6）
	newTopUps := make([]TopUpDTO, 0)
	for _, t := range topups {
		if t.ID > currentRun.BaselineTopUpID {
			newTopUps = append(newTopUps, t)
		}
	}

	if len(newTopUps) == 0 {
		return
	}

	// 按 ID 升序排序处理，保证按订单产生时序进行
	sort.Slice(newTopUps, func(i, j int) bool {
		return newTopUps[i].ID < newTopUps[j].ID
	})

	for _, topUp := range newTopUps {
		if w.IsPaused() {
			break
		}
		w.processTopUp(currentRun, topUp)

		// 更新 max_seen_topup_id
		w.runMu.Lock()
		if topUp.ID > currentRun.MaxSeenTopUpID {
			currentRun.MaxSeenTopUpID = topUp.ID
			w.db.Model(currentRun).Update("max_seen_topup_id", topUp.ID)
		}
		w.runMu.Unlock()
	}
}

// processTopUp 处理单笔 TopUp 订单的返佣流水线
func (w *Worker) processTopUp(currentRun *model.ServiceRun, topUp TopUpDTO) {
	// 1. 查询该 TopUp 是否已有处理记录
	var existing model.ReferralOrder
	err := w.db.Where("topup_id = ?", topUp.ID).First(&existing).Error
	if err == nil {
		// 已存在且处于终态，则无需处理
		if existing.Status == model.OrderStatusSuccess ||
			existing.Status == model.OrderStatusSkippedNoInviter ||
			existing.Status == model.OrderStatusSkippedInviterNotFound ||
			existing.Status == model.OrderStatusSkippedUnsupportedProvider ||
			existing.Status == model.OrderStatusSkippedZeroReward ||
			existing.Status == model.OrderStatusAbandonedRestart {
			return
		}
		// 若为 failed 且未达到最大重试次数，则继续重试
		if existing.Status == model.OrderStatusFailed && existing.RetryCount >= w.cfg.RetryMaxAttempts {
			return
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		w.Log("error", "database query error", err.Error())
		return
	}

	// 2. 判定订单状态：必须在 New API 中为 success（原则 5、8）
	if topUp.Status != "success" {
		// 若为 pending 等中间态，若之前未记录则先记为 pending
		if existing.ID == 0 {
			pendingOrder := &model.ReferralOrder{
				RunID:           currentRun.RunID,
				TopUpID:         topUp.ID,
				TradeNo:         topUp.TradeNo,
				InviteeID:       topUp.UserID,
				PaymentProvider: topUp.PaymentProvider,
				PaymentMethod:   topUp.PaymentMethod,
				Amount:          topUp.Amount,
				Money:           topUp.Money,
				TopUpStatus:     topUp.Status,
				CommissionRate:  w.cfg.CommissionRate,
				Status:          model.OrderStatusPending,
				Reason:          "Waiting for TopUp.status to become success",
			}
			_ = w.db.Create(pendingOrder)
		}
		return
	}

	// 3. 校验 PaymentProvider 并按照对应入账规则计算 creditedQuota（原则 9、10、11）
	provider := topUp.PaymentProvider
	if provider == "" {
		provider = topUp.PaymentMethod
	}
	creditedQuota, err := CalculateCreditedQuota(provider, topUp.Amount, topUp.Money)
	if err != nil {
		if errors.Is(err, ErrUnsupportedProvider) {
			w.recordOrderDecision(existing, currentRun.RunID, topUp, 0, 0, 0, model.OrderStatusSkippedUnsupportedProvider, fmt.Sprintf("Unsupported provider: %s", provider))
			w.Audit(model.AuditActionRewardSkipped, fmt.Sprintf("TopUp %d skipped: unsupported provider %s", topUp.ID, provider))
			return
		}
		w.recordOrderDecision(existing, currentRun.RunID, topUp, 0, 0, 0, model.OrderStatusFailed, err.Error())
		return
	}

	// 4. 查询充值用户以获取真实 inviter_id（原则 12）
	invitee, err := w.client.GetUser(topUp.UserID)
	if err != nil {
		w.handleRetryableError(existing, currentRun.RunID, topUp, 0, creditedQuota, 0, fmt.Sprintf("Failed to get invitee user info: %s", err.Error()))
		return
	}

	// 5. 无邀请人判定（原则 13）
	if invitee.InviterID <= 0 {
		w.recordOrderDecision(existing, currentRun.RunID, topUp, invitee.InviterID, creditedQuota, 0, model.OrderStatusSkippedNoInviter, "User has no inviter (inviter_id <= 0)")
		w.Audit(model.AuditActionRewardSkipped, fmt.Sprintf("TopUp %d skipped: invitee %d has no inviter", topUp.ID, topUp.UserID))
		return
	}

	// 6. 验证邀请人是否存在（原则 14、15）
	_, err = w.client.GetUser(invitee.InviterID)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			// 明确确认邀请人不存在/已删除/404（原则 14）：永久作废，不补发
			w.recordOrderDecision(existing, currentRun.RunID, topUp, invitee.InviterID, creditedQuota, 0, model.OrderStatusSkippedInviterNotFound, fmt.Sprintf("Inviter user %d not found (404/deleted)", invitee.InviterID))
			w.Audit(model.AuditActionRewardSkipped, fmt.Sprintf("TopUp %d skipped: inviter %d not found", topUp.ID, invitee.InviterID))
			return
		}
		// 临时网络故障（500/timeout等）（原则 15）：进入重试队列
		w.handleRetryableError(existing, currentRun.RunID, topUp, invitee.InviterID, creditedQuota, 0, fmt.Sprintf("Failed to query inviter %d: %s", invitee.InviterID, err.Error()))
		return
	}

	// 7. 计算返佣额度（原则 10）
	rewardQuota := CalculateRewardQuota(creditedQuota, w.cfg.CommissionRate)
	if rewardQuota <= 0 {
		w.recordOrderDecision(existing, currentRun.RunID, topUp, invitee.InviterID, creditedQuota, 0, model.OrderStatusSkippedZeroReward, "Calculated reward quota is 0")
		return
	}

	// 8. 调用 New API 极薄接口发放奖励（原则 3、5）
	ref := fmt.Sprintf("topup-%d", topUp.ID)
	err = w.client.AddAffReward(ref, invitee.InviterID, rewardQuota)
	if err != nil {
		w.handleRetryableError(existing, currentRun.RunID, topUp, invitee.InviterID, creditedQuota, rewardQuota, fmt.Sprintf("Failed to call aff reward API: %s", err.Error()))
		w.Audit(model.AuditActionRewardFailed, fmt.Sprintf("TopUp %d failed to reward inviter %d: %s", topUp.ID, invitee.InviterID, err.Error()))
		return
	}

	// 9. 返佣发放成功（原则 31）
	w.recordOrderDecision(existing, currentRun.RunID, topUp, invitee.InviterID, creditedQuota, rewardQuota, model.OrderStatusSuccess, "Reward successfully credited to inviter")
	w.Audit(model.AuditActionRewardSuccess, fmt.Sprintf("TopUp %d rewarded inviter %d with %d quota (creditedQuota: %d)", topUp.ID, invitee.InviterID, rewardQuota, creditedQuota))
	w.Log("info", "reward granted", fmt.Sprintf("TopUp %d -> Inviter %d: %d quota", topUp.ID, invitee.InviterID, rewardQuota))
}

// recordOrderDecision 记录或更新订单最终处理事实
func (w *Worker) recordOrderDecision(existing model.ReferralOrder, runID string, topUp TopUpDTO, inviterID int, creditedQuota, rewardQuota int64, status, reason string) {
	if existing.ID != 0 {
		existing.Status = status
		existing.Reason = reason
		existing.InviterID = inviterID
		existing.CreditedQuota = creditedQuota
		existing.RewardQuota = rewardQuota
		existing.TopUpStatus = topUp.Status
		_ = w.db.Save(&existing)
	} else {
		newOrder := &model.ReferralOrder{
			RunID:           runID,
			TopUpID:         topUp.ID,
			TradeNo:         topUp.TradeNo,
			InviteeID:       topUp.UserID,
			InviterID:       inviterID,
			PaymentProvider: topUp.PaymentProvider,
			PaymentMethod:   topUp.PaymentMethod,
			Amount:          topUp.Amount,
			Money:           topUp.Money,
			TopUpStatus:     topUp.Status,
			CreditedQuota:   creditedQuota,
			CommissionRate:  w.cfg.CommissionRate,
			RewardQuota:     rewardQuota,
			Status:          status,
			Reason:          reason,
			RetryCount:      0,
		}
		_ = w.db.Create(newOrder)
	}
}

// handleRetryableError 处理可重试的临时错误
func (w *Worker) handleRetryableError(existing model.ReferralOrder, runID string, topUp TopUpDTO, inviterID int, creditedQuota, rewardQuota int64, reason string) {
	if existing.ID != 0 {
		existing.RetryCount++
		existing.Status = model.OrderStatusFailed
		existing.Reason = reason
		_ = w.db.Save(&existing)
	} else {
		newOrder := &model.ReferralOrder{
			RunID:           runID,
			TopUpID:         topUp.ID,
			TradeNo:         topUp.TradeNo,
			InviteeID:       topUp.UserID,
			InviterID:       inviterID,
			PaymentProvider: topUp.PaymentProvider,
			PaymentMethod:   topUp.PaymentMethod,
			Amount:          topUp.Amount,
			Money:           topUp.Money,
			TopUpStatus:     topUp.Status,
			CreditedQuota:   creditedQuota,
			CommissionRate:  w.cfg.CommissionRate,
			RewardQuota:     rewardQuota,
			Status:          model.OrderStatusFailed,
			Reason:          reason,
			RetryCount:      1,
		}
		_ = w.db.Create(newOrder)
	}
}
