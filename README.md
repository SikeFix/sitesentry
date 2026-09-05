# SiteSentry 哨兵 · 网站状态监测与通知系统

一个 **Go 单二进制** 的轻量自托管网站监测、日志收集与告警通知系统：定时探测网站可用性，收集外部站点上报的日志与异常，通过规则引擎发现异常（离线 / 响应变慢 / 日志爆发），调用 **大语言模型自动诊断根因并给出处理决策**，最后通过 **邮件 / Webhook（飞书、钉钉、企微）** 通知到人。

内置 **免登录公开状态页**（团队随时可见）、**Web 管理后台**（Vue 3 SPA，无需构建步骤）、**AI 聊天助手**（可结合系统内数据上下文问答）。

---

## 功能特性

- **网站状态监测**
  - 多目标管理：URL、期望状态码、关键词断言、超时、探测间隔（30s~1h）、独立通知邮箱、恢复通知开关
  - 状态机 `up / down / unknown`，原子翻转判定，故障期间不重复告警
  - 探测历史、响应时间曲线、24h 可用率统计；手动「立即检查」
- **异常检测（规则引擎）**
  - 网站离线（up→down 翻转即告警，**恢复时自动关闭对应离线异常**）
  - 响应时间突增（最近一次 > 24h 均值 × 系数 且 > 2s）
  - 日志错误爆发（某来源 10 分钟内 error/fatal 超阈值，30 分钟冷却去重）
  - 外部站点主动上报异常（token 鉴权 API）
- **AI 能力（OpenAI 兼容接口）**
  - **自动诊断**：异常产生时自动拼装上下文（探测记录 + 最近错误日志）调用 LLM，输出中文根因分析与修复建议，随告警邮件一起发出
  - **AI 自动决策**：诊断报告末尾固定给出 `DECISION: auto_resolve / watch / manual`：
    - `auto_resolve`：AI 判定可自动解决（如目标已恢复、一次性抖动）→ **异常自动关闭**，状态页显示「AI 自动解决」
    - `watch`：影响有限，继续观察
    - `manual`：仍需人工处理
    - 安全兜底：离线类异常仅当目标**当前确实在线**才允许自动关闭，防止误关仍在故障的站点；后台可一键停用
  - **恢复事件自动闭环**：「网站已恢复」属信息性事件，创建即自动关闭，不再占用待处理列表（恢复邮件照常发送）
  - **AI 助手**：后台聊天面板，会话持久化，可勾选最近的异常 / 日志作为上下文提问
- **日志收集（跨站点）**
  - 每用户可生成多个上报令牌，外部网站 `POST /api/v1/logs` 上报（level / message / context）
  - 日志中心：级别 / 来源 / 关键字 / 时间范围筛选、来源统计、上下文查看
- **通知**
  - 邮件：自实现轻量 SMTP 客户端（SSL / STARTTLS），multipart HTML 模板，**发信限速保护**（超限自动排队重发），全程审计日志
  - Webhook：飞书 / 钉钉 / 企微 / 自定义机器人，随异常推送诊断摘要
  - 通知对象：目标级邮箱列表 → 全局默认邮箱，逐级回退
- **公开状态页**
  - 免登录状态总览（`/`），目标图标服务端代理（绕过跨域 / 防盗链限制，内置 SSRF 防护）
- **用户系统**
  - 登录（bcrypt + 失败锁定 + Cookie 会话）、管理员 / 普通用户角色、多租户数据隔离（user_id）

## 技术栈

| 层 | 技术 |
|---|---|
| 后端 | Go + Gin（仅 3 个核心依赖：gin / go-sql-driver / x-crypto），交叉编译单二进制、无 CGO |
| 前端 | Vue 3 + Naive UI + Chart.js + marked，静态文件本地化，**无构建步骤**，由 Go 直接托管 |
| 数据库 | MySQL 5.7+ / 8.x（应用启动自动迁移，无需手工建表） |
| LLM | 任意 OpenAI 兼容接口（后台可改端点 / key / 模型） |
| 部署 | systemd + Nginx 反代（或任意反代），服务器无需 Go 工具链 |

## 目录结构

```
.
├── README.md
├── docs/大纲.md              # 设计调研与系统大纲
└── sentinel/
    ├── main.go               # 入口：加载配置 → 迁移 → Seed → 路由 → 调度器
    ├── config.example.json   # 配置示例（复制为 config.json 使用）
    ├── go.mod / go.sum
    ├── internal/
    │   ├── api/              # 路由、鉴权、各模块 Handler（含 /api/v1 外部上报）
    │   ├── auth/             # 用户 / 会话 / 上报令牌 / bcrypt
    │   ├── config/           # 配置加载（config.json + 环境变量覆盖）
    │   ├── store/            # MySQL 连接、自动迁移、默认设置
    │   ├── monitor/          # HTTP 探测引擎（状态码 / 关键词 / 超时）
    │   ├── detector/         # 异常规则引擎 + AI 诊断 + 自动决策
    │   ├── llm/              # OpenAI 兼容客户端（诊断 / 聊天 / 决策解析）
    │   ├── mailer/           # SMTP 客户端 + 限速队列 + HTML 模板
    │   ├── webhook/          # 飞书 / 钉钉 / 企微 / 自定义
    │   └── scheduler/        # 进程内定时器（探测 → 检测 → 诊断 → 发信 → 清理）
    └── web/                  # 前端静态文件
        ├── status.html       # 公开状态页（/）
        ├── index.html        # 管理后台 SPA（/admin）
        └── assets/           # app.js / style.css / vendor
```

## 快速开始

### 1. 准备

- Go 1.21+（仅构建时需要）
- MySQL 5.7+，创建一个空数据库

### 2. 配置

```bash
cd sentinel
cp config.example.json config.json
# 编辑 config.json，填入数据库 DSN 与监听地址
# LLM API Key 可写在 config.json 的 llm_api_key，或用环境变量 SENTINEL_LLM_API_KEY
# 两者都留空也可：首次启动后在后台「通知与设置 → AI 模型」里配置
```

### 3. 构建与运行

```bash
# 本机开发
go run .

# 生产构建（linux/amd64 单二进制）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o sentinel .
./sentinel -config /etc/sentinel/config.json
```

首次启动自动完成：建表迁移、写入默认设置、创建初始管理员 `admin` / `admin123`（**登录后请立即修改密码**）。

### 4. 访问

- 管理后台：`https://your-domain/admin`
- 公开状态页：`https://your-domain/`

## 配置说明

`config.json`（敏感项可用环境变量覆盖）：

| 字段 | 说明 | 默认值 |
|---|---|---|
| `app_name` | 应用名称（邮件 / 页面标题） | `SiteSentry 哨兵` |
| `base_url` | 对外访问的公网地址（邮件内链接用） | `http://127.0.0.1:33330` |
| `listen` | Go 服务监听地址 | `127.0.0.1:33330` |
| `db_dsn` | MySQL DSN `user:pass@tcp(host:port)/db?charset=utf8mb4&parseTime=true&loc=Local` | — |
| `check_tick_sec` | 调度器 tick 周期（秒） | `15` |
| `llm_api_key` | LLM Key（推荐改用环境变量） | 空 |

环境变量：`SENTINEL_LLM_API_KEY`（优先级高于 config.json）。

**运行时设置**（管理后台「通知与设置」修改，存于 `settings` 表，无需重启）：

| 键 | 说明 |
|---|---|
| `llm_base_url` / `llm_api_key` / `llm_model` / `llm_enabled` | OpenAI 兼容端点、Key、模型名、AI 总开关 |
| `ai_auto_resolve` | **AI 自动决策开关**：开启后 AI 判定 `auto_resolve` 的异常自动关闭 |
| `smtp_host` / `smtp_port` / `smtp_mode` / `smtp_user` / `smtp_pass` / `smtp_from_name` | SMTP 通道（`ssl` 或 `starttls`） |
| `default_notify_emails` | 全局默认通知邮箱（逗号分隔） |
| `log_burst_threshold` / `latency_multiplier` | 日志爆发阈值（条/10min）、慢响应倍数 |
| `webhook_type` / `webhook_url` | Webhook 渠道（feishu / dingtalk / wechat / custom）与地址 |

## 生产部署

### systemd

```ini
# /etc/systemd/system/sentinel.service
[Unit]
Description=SiteSentry sentinel
After=network.target mysql.service

[Service]
Type=simple
WorkingDirectory=/opt/sentinel
ExecStart=/opt/sentinel/sentinel -config /opt/sentinel/config.json
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload && systemctl enable --now sentinel
```

### Nginx 反向代理（示例）

```nginx
server {
    listen 443 ssl http2;
    server_name status.example.com;

    # 证书...

    location /api/ {
        proxy_pass http://127.0.0.1:33330;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 180s;   # LLM 诊断可能较慢
    }
    location / {
        proxy_pass http://127.0.0.1:33330;
        proxy_set_header Host $host;
    }
}
```

> 路由约定：`/` 返回公开状态页；`/admin`（及子路径）返回管理后台 SPA；两者共用 `/assets/*` 静态资源与 `/api/*` 接口。

## API 文档

统一返回格式：`{"ok": true, "data": ...}` 或 `{"ok": false, "error": "..."}`。

### 鉴权

- **Web 端**：`POST /api/auth/login` 后使用 Cookie 会话
- **外部上报**：`Authorization: Bearer <token>`（后台「上报令牌」生成）

### 认证与会话

| 方法 | 路径 | 说明 |
|---|---|---|
| POST | `/api/auth/register` | 注册（首个用户自动成为 admin） |
| POST | `/api/auth/login` | 登录 `{username, password}` |
| POST | `/api/auth/logout` | 退出登录 |
| GET | `/api/auth/me` | 当前用户 |
| POST | `/api/auth/password` | 修改密码 `{old_password, new_password}` |

### 监测目标

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/targets` | 目标列表（含 24h 可用率、平均耗时等统计） |
| POST | `/api/targets` | 新建目标 |
| PUT | `/api/targets/:id` | 更新（名称 / URL / 期望状态码 / 关键词 / 间隔 / 超时 / 通知邮箱 / 恢复通知 / 图标 / 公开开关） |
| DELETE | `/api/targets/:id` | 删除（级联删除探测历史） |
| POST | `/api/targets/:id/check` | 立即检查（一次探测并即时评估异常） |
| GET | `/api/targets/:id/history` | 探测历史 `?hours=24` |

目标字段示例：

```json
{
  "name": "官网", "url": "https://example.com/",
  "expect_status": 200, "keyword": "",
  "interval_sec": 60, "timeout_sec": 10,
  "notify_emails": "", "notify_recovery": 1,
  "public": 1, "icon": "https://example.com/favicon.ico"
}
```

### 日志

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/logs` | 日志列表 `?level=&source=&keyword=&from=&to=&page=&size=` |
| GET | `/api/logs/sources` | 来源统计 |

### 异常告警

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/anomalies` | 异常列表 `?status=&type=&severity=&page=&size=` |
| GET | `/api/anomalies/:id` | 详情（含 AI 诊断、AI 决策、最近探测） |
| POST | `/api/anomalies/:id/resolve` | 手动标记已处理 |
| POST | `/api/anomalies/:id/rediagnose` | 重新 AI 诊断（会重新执行自动决策） |

`type` 取值：`check_down`（网站离线）/ `check_recovery`（网站恢复，信息性事件自动关闭）/ `latency_spike`（响应变慢）/ `log_burst`（日志爆发）/ `external`（外部上报）。

### AI 助手

| 方法 | 路径 | 说明 |
|---|---|---|
| GET/POST | `/api/ai/conversations` | 会话列表 / 新建 |
| DELETE | `/api/ai/conversations/:id` | 删除会话 |
| GET | `/api/ai/conversations/:id/messages` | 消息列表 |
| POST | `/api/ai/conversations/:id/messages` | 发送消息（可带异常 / 日志上下文） |
| POST | `/api/ai/conversations/:id/messages/stream` | SSE 流式回复 |

### 上报令牌

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/tokens` | 令牌列表 |
| POST | `/api/tokens` | 创建 `{name}` |
| DELETE | `/api/tokens/:id` | 吊销 |

### 设置（管理员）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/settings` | 读取（普通用户可见只读子集） |
| POST | `/api/settings` | 保存（仅白名单键；`smtp_pass` 留空表示不修改） |
| POST | `/api/settings/test-mail` | 发送测试邮件 |
| POST | `/api/settings/test-llm` | 测试 LLM 连通性 |
| POST | `/api/settings/test-webhook` | 发送测试 Webhook |

### 用户管理（管理员）

`GET/POST /api/users`、`PUT/DELETE /api/users/:id`

### 公开接口（免鉴权）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/public/status` | 状态页数据（仅 `public=1` 的目标） |
| GET | `/api/public/targets/:id` | 单目标详情 |
| GET | `/api/public/icon?url=<encoded>` | 图标代理（服务端抓取，绕过目标站跨域 / 防盗链；内置 SSRF 防护，禁内网地址，限 1MB） |

### 外部上报（token 鉴权，含 CORS）

**上报日志**

```bash
curl -X POST https://status.example.com/api/v1/logs \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d '{"source":"shop-api","level":"error","message":"支付回调超时","context":{"order_id":"123"}}'
```

`level`：`info` / `warn` / `error` / `fatal`；error/fatal 计入日志爆发检测。

**上报异常**（直接进异常队列，触发 AI 诊断 + 通知）

```bash
curl -X POST https://status.example.com/api/v1/anomalies \
  -H "Authorization: Bearer <token>" -H "Content-Type: application/json" \
  -d '{"source":"shop-api","severity":"critical","title":"支付网关不可用","detail":"连续 5 次 502"}'
```

**连通性自检**：`GET /api/v1/ping`

## AI 自动决策工作机制

1. 异常创建（`status=open`，恢复事件除外）→ 调度器**原子认领**（并发安全，保证每条异常只诊断一次、只发一封邮件）
2. 拼装上下文：目标信息 + 最近 10 次探测 + 最近 20 条 error/fatal 日志
3. LLM 输出结构化报告（根因分析 / 可能原因 / 修复建议），**末尾必须给出决策行**：
   `DECISION: auto_resolve` / `DECISION: watch` / `DECISION: manual`
4. 系统解析决策行（存入 `anomalies.ai_decision`，展示文本中剔除该行）：
   - `auto_resolve` 且 `ai_auto_resolve=1`：目标当前在线（或无目标）→ 自动置 `resolved`；离线类异常若目标仍故障 → 保持 `open` 并记日志
   - `watch` / `manual`：保持 `open` 等待人工处理
5. 目标恢复（down→up 翻转）时：自动关闭该目标所有未处理的离线异常 + 创建「网站已恢复」信息事件（自动关闭）+ 发恢复邮件

## 数据表

`users` / `sessions` / `monitor_targets` / `checks` / `api_tokens` / `logs` / `anomalies` / `llm_conversations` / `llm_messages` / `mail_queue` / `mail_log` / `settings`

`anomalies` 关键字段：`type` / `severity` / `status(open|resolved)` / `notified` / `llm_analysis` / `llm_at` / `ai_decision(auto_resolve|watch|manual)` / `resolved_at`

## 安全说明

- `config.json` 与构建产物已被 `.gitignore` 排除，**不要提交真实密钥**；发布前请确认配置为 `config.example.json` 的占位符形式
- 数据库 DSN、SMTP 授权码、LLM Key 均为敏感信息，建议 LLM Key 使用环境变量 `SENTINEL_LLM_API_KEY` 注入
- 初始管理员 `admin/admin123` 仅首启创建，部署后请立即修改
- 图标代理内置 SSRF 防护（拒绝回环 / 私网 / 链路本地地址）
- 外部上报仅接受 Bearer token，且带 CORS 白名单

## 开发

```bash
cd sentinel
go build ./...        # 编译检查
go vet ./...          # 静态检查
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o sentinel-linux-amd64 .
```

前端为纯静态文件（`sentinel/web/`），改完直接同步到站点目录即可，无构建步骤。
