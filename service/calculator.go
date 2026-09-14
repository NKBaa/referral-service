package service

import (
	"errors"
	"fmt"

	"github.com/shopspring/decimal"
)

var (
	ErrUnsupportedProvider = errors.New("unsupported payment provider")
	ErrNonSuccessTopUp     = errors.New("topup status is not success")
	ErrInvalidAmount       = errors.New("invalid topup amount or money")
)

const (
	ProviderEpay         = "epay"
	ProviderStripe       = "stripe"
	ProviderCreem        = "creem"
	ProviderWaffo        = "waffo"
	ProviderWaffoPancake = "waffo_pancake"
)

// QuotaPerUnit 与 New API common.QuotaPerUnit 保持一致：500000 (0.002 / 1K tokens)
var QuotaPerUnitDecimal = decimal.NewFromInt(500000)

// CalculateCreditedQuota 按照 New API 各 Provider 实际入账规则计算用户充值获得的 quota（原则 10）
func CalculateCreditedQuota(provider string, amount int64, money decimal.Decimal) (int64, error) {
	switch provider {
	case ProviderEpay:
		// Epay: decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		if amount <= 0 {
			return 0, ErrInvalidAmount
		}
		credited := decimal.NewFromInt(amount).Mul(QuotaPerUnitDecimal).Floor()
		return credited.IntPart(), nil

	case ProviderStripe:
		// Stripe: decimal.NewFromFloat(topUp.Money).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		if money.LessThanOrEqual(decimal.Zero) {
			return 0, ErrInvalidAmount
		}
		credited := money.Mul(QuotaPerUnitDecimal).Floor()
		return credited.IntPart(), nil

	case ProviderCreem:
		// Creem: 直接使用 Amount 作为充值额度（整数）
		if amount <= 0 {
			return 0, ErrInvalidAmount
		}
		return amount, nil

	case ProviderWaffo:
		// Waffo: decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		if amount <= 0 {
			return 0, ErrInvalidAmount
		}
		credited := decimal.NewFromInt(amount).Mul(QuotaPerUnitDecimal).Floor()
		return credited.IntPart(), nil

	case ProviderWaffoPancake:
		// Waffo Pancake: decimal.NewFromInt(topUp.Amount).Mul(decimal.NewFromFloat(common.QuotaPerUnit))
		if amount <= 0 {
			return 0, ErrInvalidAmount
		}
		credited := decimal.NewFromInt(amount).Mul(QuotaPerUnitDecimal).Floor()
		return credited.IntPart(), nil

	default:
		// 未知支付渠道原则（原则 11）：不允许猜测，跳过订单
		return 0, fmt.Errorf("%w: %s", ErrUnsupportedProvider, provider)
	}
}

// CalculateRewardQuota 根据 creditedQuota 和 commissionRate 计算返佣额度（原则 10）
// 使用 Decimal 进行计算，向下取整 floor
func CalculateRewardQuota(creditedQuota int64, commissionRate decimal.Decimal) int64 {
	if creditedQuota <= 0 || commissionRate.LessThanOrEqual(decimal.Zero) {
		return 0
	}

	creditedDec := decimal.NewFromInt(creditedQuota)
	rewardDec := creditedDec.Mul(commissionRate).Floor()
	return rewardDec.IntPart()
}
