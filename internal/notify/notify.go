// Package notify 实现多渠道通知推送：Telegram / Bark / ntfy / 企业微信机器人 /
// 飞书机器人 / 钉钉机器人 / 自定义 Webhook。渠道配置存于 settings 表的
// notify_channels 键（JSON 数组），支持逐渠道测试。
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pan-transfer/internal/store"
)

const settingsKey = "notify_channels"

// Channel 是一个通知渠道配置。
type Channel struct {
	ID      string            `json:"id"`      // 渠道实例 ID（前端生成或递增）
	Type    string            `json:"type"`    // telegram/bark/ntfy/wecom_bot/feishu_bot/dingtalk_bot/webhook
	Name    string            `json:"name"`    // 展示名
	Enabled bool              `json:"enabled"` // 是否启用
	Config  map[string]string `json:"config"`  // 各类型的配置字段
}

// Sender 通知发送器。
type Sender struct {
	st  *store.Store
	log *slog.Logger
	hc  *http.Client
}

// New 构造 Sender。
func New(st *store.Store, log *slog.Logger) *Sender {
	if log == nil {
		log = slog.Default()
	}
	return &Sender{st: st, log: log, hc: &http.Client{Timeout: 15 * time.Second}}
}

// ListChannels 读取全部渠道配置。
func (s *Sender) ListChannels(ctx context.Context) ([]Channel, error) {
	raw, err := s.st.GetSetting(ctx, settingsKey)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(raw) == "" {
		return []Channel{}, nil
	}
	var out []Channel
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return []Channel{}, nil
	}
	return out, nil
}

// SaveChannels 保存渠道配置。
func (s *Sender) SaveChannels(ctx context.Context, channels []Channel) error {
	raw, err := json.Marshal(channels)
	if err != nil {
		return err
	}
	return s.st.SetSetting(ctx, settingsKey, string(raw))
}

// Send 向所有启用渠道推送消息；逐渠道容错，汇总失败渠道名返回。
func (s *Sender) Send(ctx context.Context, title, message string) []string {
	channels, err := s.ListChannels(ctx)
	if err != nil {
		s.log.Warn("读取通知渠道失败", "err", err)
		return nil
	}
	var failed []string
	for _, ch := range channels {
		if !ch.Enabled {
			continue
		}
		if err := s.SendTo(ctx, ch, title, message); err != nil {
			s.log.Warn("通知发送失败", "channel", ch.Name, "type", ch.Type, "err", err)
			failed = append(failed, ch.Name)
		}
	}
	return failed
}

// SendTo 向单个渠道发送；返回错误供测试按钮展示。
func (s *Sender) SendTo(ctx context.Context, ch Channel, title, message string) error {
	cfg := ch.Config
	get := func(k string) string { return strings.TrimSpace(cfg[k]) }
	text := message
	if strings.TrimSpace(title) != "" {
		text = title + "\n" + message
	}

	switch ch.Type {
	case "telegram":
		token, chatID := get("bot_token"), get("chat_id")
		if token == "" || chatID == "" {
			return fmt.Errorf("需要 bot_token 与 chat_id")
		}
		api := "https://api.telegram.org"
		if v := get("api_base"); v != "" {
			api = strings.TrimRight(v, "/")
		}
		body := map[string]any{"chat_id": chatID, "text": title + "\n" + message}
		if !strings.Contains(message, "<") { // 非富文本按纯文本发送
			delete(body, "text")
			body["text"] = text
		}
		return s.postJSON(ctx, fmt.Sprintf("%s/bot%s/sendMessage", api, token), body)

	case "bark":
		key := get("device_key")
		if key == "" {
			return fmt.Errorf("需要 device_key")
		}
		server := strings.TrimRight(get("server"), "/")
		if server == "" {
			server = "https://api.day.app"
		}
		q := url.Values{}
		if t := get("sound"); t != "" {
			q.Set("sound", t)
		}
		endpoint := fmt.Sprintf("%s/%s/%s", server, url.PathEscape(key), url.PathEscape(title))
		if message != "" {
			endpoint += "/" + url.PathEscape(message)
		}
		if q.Encode() != "" {
			endpoint += "?" + q.Encode()
		}
		return s.postRaw(ctx, endpoint, nil)

	case "ntfy":
		topic := get("topic_url")
		if topic == "" {
			return fmt.Errorf("需要 topic_url（如 https://ntfy.sh/mytopic）")
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, topic, strings.NewReader(text))
		if err != nil {
			return err
		}
		req.Header.Set("Title", title)
		if p := get("password"); p != "" {
			req.SetBasicAuth(get("username"), p)
		}
		return s.do(req)

	case "wecom_bot":
		hook := get("webhook")
		if hook == "" {
			return fmt.Errorf("需要 webhook 地址")
		}
		return s.postJSON(ctx, hook, map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": text},
		})

	case "feishu_bot":
		hook := get("webhook")
		if hook == "" {
			return fmt.Errorf("需要 webhook 地址")
		}
		return s.postJSON(ctx, hook, map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": text},
		})

	case "dingtalk_bot":
		hook := get("webhook")
		if hook == "" {
			return fmt.Errorf("需要 webhook 地址")
		}
		return s.postJSON(ctx, hook, map[string]any{
			"msgtype": "text",
			"text":    map[string]string{"content": text},
		})

	case "webhook":
		hook := get("url")
		if hook == "" {
			return fmt.Errorf("需要 webhook URL")
		}
		payload := map[string]string{"title": title, "message": message, "text": text}
		if tmpl := get("body_template"); tmpl != "" {
			payload["text"] = strings.ReplaceAll(tmpl, "{message}", text)
		}
		return s.postJSON(ctx, hook, payload)

	default:
		return fmt.Errorf("未知渠道类型：%s", ch.Type)
	}
}

func (s *Sender) postJSON(ctx context.Context, endpoint string, body any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return s.do(req)
}

func (s *Sender) postRaw(ctx context.Context, endpoint string, _ any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return err
	}
	return s.do(req)
}

func (s *Sender) do(req *http.Request) error {
	resp, err := s.hc.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
