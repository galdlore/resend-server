package mailer

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	apiKey     string
	httpClient *http.Client
}

type SendRequest struct {
	FromAddress string
	FromName    string
	ToAddress   string
	Subject     string
	BodyText    string
	BodyHTML    string
}

type SendResult struct {
	ID    string
	Error error
}

func New(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (c *Client) Send(req SendRequest) (*SendResult, error) {
	if c.apiKey == "" {
		return nil, errors.New("Resend APIキーが設定されていません")
	}

	fromHeader := req.FromAddress
	if req.FromName != "" {
		fromHeader = fmt.Sprintf("%s <%s>", req.FromName, req.FromAddress)
	}

	// 宛先アドレスのトリム
	toAddress := strings.TrimSpace(req.ToAddress)
	if toAddress == "" {
		return nil, errors.New("宛先メールアドレスが空です")
	}

	payload := map[string]interface{}{
		"from":    fromHeader,
		"to":      []string{toAddress},
		"subject": req.Subject,
	}
	if req.BodyText != "" {
		payload["text"] = req.BodyText
	}
	if req.BodyHTML != "" {
		payload["html"] = req.BodyHTML
	} else if req.BodyText != "" {
		// プレーンテキストのみの場合は改行を<br>にして簡易HTMLも設定
		escaped := strings.ReplaceAll(req.BodyText, "\n", "<br>")
		payload["html"] = fmt.Sprintf("<div style=\"font-family: sans-serif; line-height: 1.6;\">%s</div>", escaped)
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("リクエストデータの生成に失敗しました: %w", err)
	}

	httpReq, err := http.NewRequest("POST", "https://api.resend.com/emails", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("httpリクエストの構築に失敗しました: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("resend apiへの送信中に通信エラーが発生しました: %w", err)
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("レスポンスの読み取りに失敗しました: %w", err)
	}

	if resp.StatusCode >= 400 {
		var errResp struct {
			Message    string `json:"message"`
			StatusCode int    `json:"statusCode"`
			Name       string `json:"name"`
		}
		if jsonErr := json.Unmarshal(respBytes, &errResp); jsonErr == nil && errResp.Message != "" {
			return nil, fmt.Errorf("resend送信エラー (%d %s): %s", errResp.StatusCode, errResp.Name, errResp.Message)
		}
		return nil, fmt.Errorf("resend送信エラー (ステータスコード %d): %s", resp.StatusCode, string(respBytes))
	}

	var successResp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(respBytes, &successResp); err != nil {
		return nil, fmt.Errorf("レスポンスjsonの解析に失敗しました: %w", err)
	}

	return &SendResult{
		ID: successResp.ID,
	}, nil
}
