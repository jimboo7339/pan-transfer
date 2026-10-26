package domain

import "time"

// AccountProfile 是驱动返回并由服务端缓存的账号公开资料。
// UserID 仅供服务端做账号关联，不会下发给浏览器。
type AccountProfile struct {
	AccountID   int64     `json:"account_id"`
	UserID      string    `json:"user_id"`
	Nickname    string    `json:"nickname"`
	Membership  string    `json:"membership"`
	UsedBytes   int64     `json:"used_bytes"`
	TotalBytes  int64     `json:"total_bytes"`
	AttemptDate string    `json:"attempt_date"`
	RefreshedAt time.Time `json:"refreshed_at"`
}
