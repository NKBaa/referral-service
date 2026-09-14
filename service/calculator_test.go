package service

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

func TestCalculateCreditedQuota(t *testing.T) {
	tests := []struct {
		name        string
		provider    string
		amount      int64
		money       decimal.Decimal
		expected    int64
		expectError bool
		targetErr   error
	}{
		{
			name:        "Epay 10 units",
			provider:    ProviderEpay,
			amount:      10,
			money:       decimal.Zero,
			expected:    5000000, // 10 * 500000
			expectError: false,
		},
		{
			name:        "Stripe 15.5 money",
			provider:    ProviderStripe,
			amount:      0,
			money:       decimal.NewFromFloat(15.5),
			expected:    7750000, // 15.5 * 500000 = 7750000
			expectError: false,
		},
		{
			name:        "Creem amount directly as quota",
			provider:    ProviderCreem,
			amount:      1000000,
			money:       decimal.NewFromFloat(2.0),
			expected:    1000000, // Amount is quota
			expectError: false,
		},
		{
			name:        "Waffo 20 units",
			provider:    ProviderWaffo,
			amount:      20,
			money:       decimal.Zero,
			expected:    10000000, // 20 * 500000
			expectError: false,
		},
		{
			name:        "Waffo Pancake 5 units",
			provider:    ProviderWaffoPancake,
			amount:      5,
			money:       decimal.Zero,
			expected:    2500000, // 5 * 500000
			expectError: false,
		},
		{
			name:        "Unknown provider (原则 11)",
			provider:    "crypto_pay",
			amount:      100,
			money:       decimal.NewFromFloat(10),
			expected:    0,
			expectError: true,
			targetErr:   ErrUnsupportedProvider,
		},
		{
			name:        "Epay invalid amount",
			provider:    ProviderEpay,
			amount:      -1,
			expected:    0,
			expectError: true,
			targetErr:   ErrInvalidAmount,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual, err := CalculateCreditedQuota(tc.provider, tc.amount, tc.money)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tc.targetErr != nil && !errors.Is(err, tc.targetErr) {
					t.Fatalf("expected error %v, got %v", tc.targetErr, err)
				}
			} else {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if actual != tc.expected {
					t.Fatalf("expected %d, got %d", tc.expected, actual)
				}
			}
		})
	}
}

func TestCalculateRewardQuota(t *testing.T) {
	tests := []struct {
		name          string
		creditedQuota int64
		rate          string
		expected      int64
	}{
		{
			name:          "10% commission on 5000000 quota",
			creditedQuota: 5000000,
			rate:          "0.10",
			expected:      500000,
		},
		{
			name:          "15% commission with floor precision test",
			creditedQuota: 1000001,
			rate:          "0.15",
			// 1000001 * 0.15 = 150000.15 -> floor -> 150000
			expected: 150000,
		},
		{
			name:          "Zero rate",
			creditedQuota: 5000000,
			rate:          "0.0",
			expected:      0,
		},
		{
			name:          "Zero credited quota",
			creditedQuota: 0,
			rate:          "0.10",
			expected:      0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rateDec, err := decimal.NewFromString(tc.rate)
			if err != nil {
				t.Fatalf("invalid rate string: %v", err)
			}
			actual := CalculateRewardQuota(tc.creditedQuota, rateDec)
			if actual != tc.expected {
				t.Fatalf("expected %d, got %d", tc.expected, actual)
			}
		})
	}
}
