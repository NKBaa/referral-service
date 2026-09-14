package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

var (
	ErrUserNotFound = errors.New("user not found")
	ErrAuthInvalid  = errors.New("new api authentication failed (401/403)")
)

type TopUpDTO struct {
	ID              int64           `json:"id"`
	UserID          int             `json:"user_id"`
	Amount          int64           `json:"amount"`
	Money           decimal.Decimal `json:"money"`
	TradeNo         string          `json:"trade_no"`
	PaymentMethod   string          `json:"payment_method"`
	PaymentProvider string          `json:"payment_provider"`
	CreateTime      int64           `json:"create_time"`
	CompleteTime    int64           `json:"complete_time"`
	Status          string          `json:"status"`
}

type UserDTO struct {
	ID        int    `json:"id"`
	Username  string `json:"username"`
	InviterID int    `json:"inviter_id"`
	Role      int    `json:"role"`
	Status    int    `json:"status"`
}

type ApiResponse struct {
	Success bool            `json:"success"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type PageData struct {
	Page     int               `json:"page"`
	PageSize int               `json:"page_size"`
	Total    int64             `json:"total"`
	Items    []json.RawMessage `json:"items"`
}

type NewApiClient struct {
	baseURL     string
	accessToken string
	httpClient  *http.Client
}

func NewNewApiClient(baseURL, accessToken string, timeoutSeconds int) *NewApiClient {
	return &NewApiClient{
		baseURL:     strings.TrimRight(baseURL, "/"),
		accessToken: accessToken,
		httpClient: &http.Client{
			Timeout: time.Duration(timeoutSeconds) * time.Second,
		},
	}
}

func (c *NewApiClient) newRequest(method, path string, body io.Reader) (*http.Request, error) {
	reqURL := c.baseURL + path
	req, err := http.NewRequest(method, reqURL, body)
	if err != nil {
		return nil, err
	}
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// GetMaxTopUpID 获取 New API 当前最大的 TopUp ID 作为 baseline（原则 6）
func (c *NewApiClient) GetMaxTopUpID() (int64, error) {
	topups, _, err := c.GetTopUps(1, 1)
	if err != nil {
		return 0, err
	}
	if len(topups) == 0 {
		return 0, nil
	}
	return topups[0].ID, nil
}

// GetTopUps 获取充值订单列表（New API 默认以 ID 降序返回）
func (c *NewApiClient) GetTopUps(page, pageSize int) ([]TopUpDTO, int64, error) {
	path := fmt.Sprintf("/api/user/topup?p=%d&page_size=%d", page, pageSize)
	req, err := c.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, 0, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, 0, fmt.Errorf("%w: status %d", ErrAuthInvalid, resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}

	var apiResp ApiResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return nil, 0, fmt.Errorf("failed to parse New API response: %w", err)
	}
	if !apiResp.Success {
		return nil, 0, fmt.Errorf("New API returned error: %s", apiResp.Message)
	}

	var pageData PageData
	if err := json.Unmarshal(apiResp.Data, &pageData); err != nil {
		return nil, 0, fmt.Errorf("failed to parse page data: %w", err)
	}

	topups := make([]TopUpDTO, 0, len(pageData.Items))
	for _, itemRaw := range pageData.Items {
		var item TopUpDTO
		if err := json.Unmarshal(itemRaw, &item); err == nil {
			topups = append(topups, item)
		}
	}

	return topups, pageData.Total, nil
}

// GetUser 查询充值用户或邀请人信息（原则 12、14）
func (c *NewApiClient) GetUser(userID int) (*UserDTO, error) {
	path := fmt.Sprintf("/api/user/%d", userID)
	req, err := c.newRequest(http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("%w: status %d", ErrAuthInvalid, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrUserNotFound
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var apiResp ApiResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return nil, fmt.Errorf("failed to parse user response: %w", err)
	}

	if !apiResp.Success {
		// 检查消息是否指示不存在
		msg := strings.ToLower(apiResp.Message)
		if strings.Contains(msg, "not found") || strings.Contains(msg, "不存在") || strings.Contains(msg, "record not found") {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("failed to get user: %s", apiResp.Message)
	}

	var user UserDTO
	if err := json.Unmarshal(apiResp.Data, &user); err != nil {
		return nil, fmt.Errorf("failed to unmarshal user data: %w", err)
	}

	return &user, nil
}

// AddAffReward 调用 New API 新增的极薄接口发送充值返佣奖励（原则 3、5）
func (c *NewApiClient) AddAffReward(reference string, userID int, quota int64) error {
	if reference == "" {
		reference = fmt.Sprintf("reward-%d-%d", userID, time.Now().UnixNano())
	}
	reqPayload := map[string]any{
		"reference":    reference,
		"user_id":      userID,
		"reward_quota": quota,
		"quota":        quota,
	}
	data, err := json.Marshal(reqPayload)
	if err != nil {
		return err
	}

	req, err := c.newRequest(http.MethodPost, "/api/user/aff/reward", bytes.NewBuffer(data))
	if err != nil {
		return err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%w: status %d", ErrAuthInvalid, resp.StatusCode)
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var apiResp ApiResponse
	if err := json.Unmarshal(bodyBytes, &apiResp); err != nil {
		return fmt.Errorf("failed to parse reward response: %w", err)
	}
	if !apiResp.Success {
		return fmt.Errorf("reward api failed: %s", apiResp.Message)
	}

	return nil
}

// Ping 测试 New API 连接状态
func (c *NewApiClient) Ping() error {
	_, err := c.GetMaxTopUpID()
	return err
}
