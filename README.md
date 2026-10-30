# PanTransfer · 轻量网盘转存

纯网盘转存工具：粘贴分享链接，自动追剧转存、按正则重命名、忽略后缀去重、定时调度、
多渠道通知；转存完成后还能**按任务分类回调你的接口**（通知媒体库刷新、字幕流水线等下游系统）。
当前支持 **夸克网盘** 与 **光鸭云盘**，单二进制部署（Go 后端 + 内嵌 Vue3 前端 + SQLite）。

> 协议层移植自 [LitePan](https://github.com/jimboo7339/LitePan) 的 Quark/Guangya 驱动（与
> cloud-auto-save-x 1.x 开源版的 adapter 逐行对齐）；业务逻辑（追剧调度、魔法重命名、
> 分享预览）参考 CASX v1.0.9 开源源码用 Go 重新编排。

## 功能

- **追剧任务**：多源链接按序轮换（失效自动跳过）、三档调度（手动 / 跟随全局 / 自定义定时，
  周几 + 时刻 + 1–3 分钟随机延迟）、全局调度开关、截止日期、增量起始 fid
- **转存范围锁定**：按链接记住要转的分享子目录（`#/list/share/<fid>`），多链接任务各转各的文件夹
- **分享预览**：层级浏览分享目录，逐文件显示重命名预览与状态标注
  （✅ 已转存 / ⏭ 起始及之前 / ⚠ 重命名冲突），支持**只看待转存/已转存**筛选与**分页**（大目录不卡）
- **智能去重**：目标目录现有文件 + 转存历史双去重，跨链接互斥；可选**忽略扩展名**
  （mp4 / mkv 有一个即视为已有，不再转存另一个）；可一键清空某任务的转存历史
- **魔法重命名**：内置 `$TV_MAGIC` / `$TV_REGEX` / `$SHOW_MAGIC` 等规则，支持
  `{TASKNAME}` `{SXX}` `{E}` `{II}` `{EXT}` 变量与 PCRE 断言语法（RE2 失败自动降级 regexp2）
- **命名规则维护**：内置规则可覆盖、可自定义，带实时试算测试
- **任务分类与回调**：任务可归入分类，分类配置回调接口（GET/POST、URL/Body 模板、自定义请求头），
  在**单任务完成**或**分类轮次完成**（同批任务跑完聚合一次）时调用下游系统；
  可选「0 转存不回调」；每次调用留日志便于排障
- **通知推送**：Telegram / Bark / ntfy / 企业微信 / 飞书 / 钉钉 / 自定义 Webhook，逐渠道测试；
  0 新文件的空跑静默，不刷屏
- **运行日志**：保留最近运行记录，含**完整执行日志**（解析→token→目录→计划→转存→重命名）；
  可配置保留天数（默认 90 天，每任务保底 50 条）
- **账号管理**：夸克 Cookie / 光鸭 Token 两种凭据，**扫码登录**自动获取，驱动刷新后自动回写，
  一键测试连通性
- **访问控制**：可选访问密码（HMAC 会话）
- **界面**：响应式适配手机/PC（移动端抽屉侧栏 + 全屏弹层）；浅色/深色/自动三档主题
  （自动跟随系统 `prefers-color-scheme`，localStorage 记忆，预置脚本防首帧闪烁）

## 转存流程一览

```
触发（手动 / 定时 cron，1–3 分钟随机延迟，全局并发上限 2）
  └─ 逐条链接（链接间隔 2s）
       解析分享链接（含各链接自己的子目录 fid）
       → 获取分享 token
       → 准备目标目录（解析路径，不存在则逐级创建）
       → 列目标目录（30s 缓存）＋ 转存历史 → 去重依据
       → 列分享目录 → 生成转存计划（pattern / 黑名单 / 增量 / 同名保留最大）
       → 分批转存（每批 100，含权限重试）
       → 重命名（改名 + 二次校验补改）
       → 可选子目录递归同步（≤3 层）
       → 写运行记录与完整日志 → 汇总任务状态
  └─ 收尾：通知推送（0 文件静默）＋ 分类回调（单任务 / 轮次聚合）＋ 记录转存历史
```

网络层保护：每账号请求间隔 300ms；限流（光鸭 127/429/354）指数退避重试 3 次；
目录与分享列表 30s 缓存（自己的写入立即失效）；单次运行总超时 30 分钟；
手动运行脱离 HTTP 请求上下文（刷新页面、断网都不中断）。


## 快速开始

```bash
# 构建（需要 Go 1.26+ 与 Node 20+）
cd web && npm install && npm run build && cd ..
go build -o pan-transfer.exe ./cmd/server

# 运行
./pan-transfer.exe -addr :8935 -data ./data
# 打开 http://127.0.0.1:8935
```

首次使用：**网盘账号** → 添加账号（推荐扫码登录）→ **转存任务** → 新建任务
（粘贴链接 → 预览分享 → 选保存目录 → 可选定时）→ 运行。

## 使用建议

- 不要把定时频率调高：资源不是实时更新的，高频只增加账号风控风险（调度自带 1–3 分钟随机延迟）。
- 夸克无官方个人 API，光鸭官方开放 API 不含分享转存接口——本项目与所有同类工具一样
  使用逆向 Web API，接口可能随官方调整失效，届时需跟进适配。
- 凭据保存在本地 `data/pantransfer.db`，请勿暴露到公网；如需公网访问务必设置访问密码并加反代。

## Docker 部署（NAS / 服务器）

镜像内是单二进制 + ca-certificates + tzdata，前端已 embed，SQLite 落在 `/app/data`。
两步走：**先拿到镜像**（CI 自动构建 或 本地构建导入），**再在 NAS 上跑 compose**。

### 方式一：GitHub Actions 自动构建（推荐）

仓库已带 `.github/workflows/docker.yml`：推送到 `main` 自动构建
`linux/amd64 + linux/arm64` 双架构镜像并发布到 ghcr.io；推 `v*` tag 出语义化版本镜像。

1. 把本项目推到你的 GitHub 仓库（Actions 默认有 `GITHUB_TOKEN` 写 packages 权限，无需额外配置）。
2. 等 Actions 构建完成，到仓库 Packages 页把镜像可见性改为 **public**（否则 NAS 拉取要配登录）。
3. 把 `docker-compose.yml` 里的 `ghcr.io/OWNER/pan-transfer` 改成你的实际地址。

### 方式二：本机构建镜像，离线导入 NAS（无 CI / 不想公开源码）

```bash
# 本机（装了 Docker 的机器）
docker build -t pan-transfer:latest --build-arg VERSION=v0.1.0 .
docker save pan-transfer:latest | gzip > pan-transfer.tar.gz

# 传到 NAS 后导入
docker load < pan-transfer.tar.gz
```

> 本机没装 Docker 也可以交叉编译后导入：`CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o pan-transfer ./cmd/server`
> （NAS 是 ARM 则 `GOARCH=arm64`），配合 `FROM alpine:3.21` + `apk add ca-certificates tzdata` 一样可用；
> 或者干脆在 NAS 上直接跑二进制。

### NAS 上启动

`docker-compose.yml` 已随仓库提供，SSH 到 NAS：

```bash
mkdir -p /vol1/docker/pan-transfer && cd /vol1/docker/pan-transfer
# 放入 docker-compose.yml，修改 image 为你的镜像
docker compose up -d
docker compose logs -f          # 看到 "PanTransfer 已启动 addr=:8935" 即成功
```

图形界面部署：

- **飞牛 fnOS**：Docker → Compose → 新建项目，粘贴 compose 内容，路径选 `/vol1/docker/pan-transfer`
- **群晖 DSM**：Container Manager → 项目 → 新增，粘贴 compose
- **unRAID / 绿联 / 威联通**：按容器参数映射——端口 `8935:8935`，目录挂载 `<宿主机目录>:/app/data`，环境变量 `TZ=Asia/Shanghai`

启动后访问 `http://NAS的IP:8935`。

### 升级与备份

- **升级**：`docker compose pull && docker compose up -d`（方式二则重新 save/load 后 `up -d`）。数据在卷里，升级不丢。
- **备份**：只需备份挂载目录里的 `pantransfer.db`（账号凭据、任务、历史、规则、设置都在里面）。
  定时备份可加一条 cron：`sqlite3 data/pantransfer.db ".backup backup.db"` 或停容器后直接拷贝。

### 注意事项

- **时区**：追剧定时按容器时区调度，compose 里 `TZ=Asia/Shanghai` 不要删。
- **安全**：容器端口不要直接暴露公网。需要外网访问时，用 Nginx 反代 + HTTPS，
  并在 设置 → 访问控制 里开启访问密码。
- **频率**：NAS 上同样不要调高定时频率，默认调度自带随机延迟。

## 目录结构

```
cmd/server/          入口：装配 + 内嵌静态资源 + 优雅关闭
internal/
  drivers → drivers/quark|guangya   网盘驱动（协议层，移植自 LitePan）
  driver/      驱动接口契约与注册表
  domain/      领域模型与错误码
  httpx/       HTTP 客户端工具
  store/       SQLite 仓储（账号/任务/运行/规则/转存历史/设置/分类与回调日志）
  accounts/    账号→驱动实例生命周期（凭据注入/回写/扫码）
  transfer/    转存执行器 + 调度 + 分享预览 + 魔法重命名 + 分类回调
  driver/      驱动接口契约、注册表、限流重试、目录列表缓存
  notify/      多渠道通知
  httpapi/     REST API + 会话鉴权
web/                 Vue3 前端（构建产物 embed 进二进制）
```

## 声明

仅供学习与个人使用；请确保拥有对应账号与资源的合法使用权，遵守网盘服务条款。
使用逆向接口产生的账号与数据风险自行承担。
