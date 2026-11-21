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
- **仪表盘**：打开即见系统健康总览——今日转存数 + 7 天趋势、账号启用状态、
  失败任务告警（点击直接跳到该任务）、下次调度时间
- **全局搜索**：`⌘K` / `Ctrl+K` / `/` 打开命令面板，一次搜任务、账号、规则、
  分类、运行记录、已转存文件；键盘上下选择、回车跳转
- **批量操作**：勾选任务后浮动条出现，支持批量运行 / 暂停 / 启用 / 删除 /
  **移动分类** / **改调度模式**；执行期间显示进度，完成后展示逐条结果（失败项可展开）
- **两种视图**：卡片（舒适，默认）与**表格**（紧凑，宽屏一屏 15+ 行）双视图切换，
  窄屏自动禁用表格视图而不是让用户看横向滚动条
- **任务详情页**：`/tasks/:id` 独立路由，可分享、可深链；仪表盘告警与搜索结果直接落到该任务
- **键盘快捷键**：`⌘K` 搜索、`/ ` 聚焦搜索、`n` 新建任务、`g d/t/l/a/r/s` 跳页、
  `?` 查看快捷键清单；输入框内不触发任何快捷键
- **配置导入导出**：任务 / 命名规则 / 分类回调可打包成 JSON 跨实例迁移，
  **不含任何凭据**（只有「该账号是否配过凭据」的布尔标记），导入前先预检并展示
  「将发生什么 / 哪些会因账号缺失被跳过」
- **实时进度**：「运行」按钮立即返回，卡片内显示阶段（解析分享链接 / 转存中 / 重命名）、
  进度条与滚动日志，**随时可取消**；关掉页面再回来也能继续跟（服务端保留句柄）
- **可观测性**：`GET /metrics` 以 Prometheus 文本格式暴露请求量、错误码分布、耗时直方图、
  调度扫描次数、上游限流次数，可直接接入 Grafana；`GET /api/health/ready` 逐项探测
  数据库与调度器（不只是「进程活着」）
- **账号管理**：夸克 Cookie / 光鸭 Token 两种凭据，**扫码登录**自动获取，驱动刷新后自动回写，
  一键测试连通性；账号可启用/停用（停用后不被调度）
- **凭据安全**：账号 cookie / token 以 **AES-256-GCM 加密落库**（主密钥来自
  `PAN_TRANSFER_MASTER_KEY` 或 `data/master.key`）；管理接口**不再下发凭据明文**，
  前端只显示「🔒 已保存」；访问密码使用 **{{SECRET_9b6f2cd4}}** 哈希；会话支持登出与强制作废；
  跨站请求被拒绝（CSRF 防护）；登录失败限流
- **请求节流**：按账号的最小请求间隔（默认 300ms，可在设置里调整）是降低风控概率的第一道闸；
  夸克 / 光鸭均识别 HTTP 429 与业务限流码并按指数退避重试
- **访问控制**：可选访问密码（HMAC 会话）
- **数据备份**：内置备份与还原（`VACUUM INTO` 一致性快照，含 WAL 中未落盘的事务），
  每天自动备份并保留最近 7 份；可在设置页手动备份 / 下载 / 一键还原
- **深度健康检查**：`GET /api/health/ready` 探测数据库与调度器状态（不只是「进程活着」）；
  启动时自动收尾上次崩溃遗留的「运行中」记录
- **界面**：响应式适配手机/PC（移动端抽屉侧栏 + 全屏弹层）；浅色/深色/自动三档主题
  （自动跟随系统 `prefers-color-scheme`，localStorage 记忆，预置脚本防首帧闪烁）；
  统一确认弹层（替代原生 `confirm`）、键盘焦点可见、`prefers-reduced-motion` 适配、
  13 个弹层全部具备焦点陷阱与焦点归还（读屏器可识别 `role="dialog"`）
- **草稿保护**：任务表单 20+ 字段自动草稿（防抖 400ms 写 localStorage），
  误触遮罩关闭会二次确认，新建任务恢复草稿、提交成功后清除

## 首次使用

```
网盘账号 → 添加账号（推荐扫码登录）
  → 转存任务 → 新建任务（粘贴链接 → 预览分享 → 选保存目录 → 可选定时）→ 运行
```

**上手要点**

- 建任务时可以只填「账号 + 至少一条分享链接 + 保存目录」，其余留空即为合理默认
- 「运行」按钮点下去会**立即返回**，进度在任务卡片下方实时显示，不用干等
- 定时任务建议不要设得太密：资源不是实时更新的，高频只增加账号风控风险
- 不确定某个正则规则的效果？「命名规则」页有试算器，粘真实文件名实时预览

## 界面导览

| 页面 | 用途 |
|---|---|
| **仪表盘** | 今日转存数 + 7 天趋势、账号状态、失败告警、下次调度。打开面板先看这一页 |
| **转存任务** | 卡片 / 表格双视图；勾选后批量运行/暂停/删除/移动分类/改调度 |
| **运行日志** | 每个任务最近的执行日志，可展开树状摘要与完整执行日志 |
| **网盘账号** | 驱动卡片、扫码登录、连通性探测；账号可停用（停用后不被调度） |
| **命名规则** | 内置规则覆盖、自定义规则、实时试算 |
| **分类与回调** | 分类可配置回调接口，转存完成后通知下游系统 |
| **设置** | 通知渠道、调度、转存策略、访问控制、数据备份、配置导入导出 |

**快捷键**：`⌘K` 搜索 · `n` 新建任务 · `g d`/`g t`/`g l`/`g a`/`g r`/`g s` 跳页 · `?` 帮助 · `Esc` 关闭弹层

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

网络层保护：**每账号请求间隔 300ms**（可在设置里调整；逐文件遍历是平台风控的主因）；
限流（光鸭 127/429/354、夸克 429 与业务限流码）指数退避重试 3 次；
目录与分享列表 30s 缓存（自己的写入立即失效）；单次运行总超时 30 分钟；
手动运行**异步执行**（HTTP 请求立即返回 `job_id`，进度通过 `GET /api/jobs/{id}` 查询），
刷新页面、断网、反代超时都不中断。

## 升级须知（凭据加密）

从本版本起账号凭据加密落库，**升级前请注意**：

1. **务必备份**：升级前先在「设置 → 数据备份」创建一份，或停容器拷贝 `data/` 整个目录。
2. **主密钥**：首次启动会自动生成 `data/master.key`（权限 0600）。
   **备份时必须连它一起保存**，否则备份里的凭据无法解密。
   也可以用 `PAN_TRANSFER_MASTER_KEY` 环境变量提供（容器部署推荐），
   生成方式：`openssl rand -hex 32`。
3. **旧库自动兼容**：升级前的明文凭据会被原样读取，不会导致账号一次性失效。
4. **迁移不可逆**：新版会写 `schema_migrations` 版本表，回退到旧镜像前请先确认
   该版本能读新 schema（列只会增加，一般不会出问题，但回退前建议先备份）。


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
- **备份**：优先用「设置 → 数据备份」页（`VACUUM INTO` 一致性快照，含 WAL 里未落盘的事务，
  容器内没有 `sqlite3` 所以手动 `.backup` 做不到这件事）。
  目录里会保留最近 7 份到 `data/backups/`，服务每天自动创建一份。
  **备份 `data/` 时必须连 `master.key` 一起保存**：没有主密钥，备份里的凭据无法解密。

### 注意事项

- **时区**：追剧定时按容器时区调度，compose 里 `TZ=Asia/Shanghai` 不要删。
- **安全**：容器端口不要直接暴露公网。需要外网访问时，用 Nginx 反代 + HTTPS，
  并在 设置 → 访问控制 里开启访问密码；反代终止 TLS 时加
  `PAN_TRANSFER_SECURE_COOKIE=1` 与 `PAN_TRANSFER_ALLOWED_ORIGINS=https://你的域名`
  （否则跨站请求会被拒绝）。
- **频率**：NAS 上同样不要调高定时频率，默认调度自带随机延迟；
  账号请求间隔默认 300ms，调大会拉长时间但更安全，调到 0 等于放弃这层保护。

## 目录结构

```
cmd/server/          入口：装配 + 内嵌静态资源 + 优雅关闭 + 还原运行时
internal/
  drivers → drivers/quark|guangya   网盘驱动（协议层，移植自 LitePan）
  driver/      驱动接口契约与注册表、限流重试、目录列表缓存、按账号请求间隔门
  domain/      领域模型与错误码
  httpx/       HTTP 客户端工具
  store/       SQLite 仓储（账号/任务/运行/规则/转存历史/设置/分类与回调日志）
              + 版本化迁移 + 备份还原（VACUUM INTO）
  secret/      凭据加密（AES-256-GCM）+ 密码哈希（{{SECRET_9b6f2cd4}}）
  accounts/    账号→驱动实例生命周期（凭据注入/回写/扫码/刷新单飞）
  transfer/    转存执行器 + 调度 + 分享预览 + 魔法重命名 + 分类回调
  notify/      多渠道通知
  httpapi/     REST API + 会话鉴权（含 CSRF 防护）+ 备份接口 + 深度健康检查
web/                 Vue3 前端（构建产物 embed 进二进制）
```

## API 速览

| 分组 | 端点 |
|---|---|
| 认证 | `POST /api/auth/login` · `POST /api/auth/logout` · `GET /api/auth/status` |
| 账号 | `GET/POST /api/accounts` · `PUT/DELETE /api/accounts/{id}` · `POST /api/accounts/{id}/probe` · `GET /api/accounts/{id}/profile` · `POST /api/accounts/qr/{start,poll}` |
| 任务 | `GET/POST /api/tasks` · `PUT/DELETE /api/tasks/{id}` · `POST /api/tasks/{id}/status` · `POST /api/tasks/{id}/run`（异步） · `GET /api/tasks/{id}/runs` · `GET/DELETE /api/tasks/{id}/saved-names` |
| 运行句柄 | `GET /api/jobs` · `GET /api/jobs/{id}` · `POST /api/jobs/{id}/cancel` · `DELETE /api/jobs/{id}` |
| 分类回调 | `GET/POST /api/categories` · `DELETE /api/categories/{id}` · `POST /api/categories/{id}/test` · `GET /api/categories/{id}/callback-logs` |
| 命名规则 | `GET/POST /api/rules` · `DELETE /api/rules/{key}` · `POST /api/rules/test` |
| 预览/浏览 | `POST /api/share/preview` · `GET /api/files/{list,resolve}` · `POST /api/files/mkdir` |
| 备份 | `GET/POST /api/backup` · `GET /api/backup/download` · `POST /api/restore` |
| 运维 | `GET /api/health` · `GET /api/health/ready` · `GET /metrics` |

## 声明

仅供学习与个人使用；请确保拥有对应账号与资源的合法使用权，遵守网盘服务条款。
使用逆向接口产生的账号与数据风险自行承担。
