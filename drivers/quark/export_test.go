package quark

import (
	"time"

	"pan-transfer/internal/httpx"
)

// InitForTest 把驱动指向测试用的 HTTP 服务，并注入一个可用的 Cookie。
//
// 放在 _test.go 而不是 driver.go：生产代码不应该存在"把请求改指向任意地址"
// 的入口，即使是测试专用的。真实请求地址是编译期常量（baseURL），
// 而测试需要本地 httptest 服务器来精确构造 429 / 401 / 业务错误码等上游行为。
func (d *Driver) InitForTest(serverURL string) {
	d.testBase = serverURL
	d.cookie = "test-cookie=1"
	d.client = httpx.NewClient(httpx.ClientOptions{Timeout: 5 * time.Second})
}
