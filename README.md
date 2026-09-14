# Referral Service - 充值返佣扩展服务

本服务是专为 New API 设计的外部充值返佣扩展微服务。严格遵循《Referral Service 最终原则》设计，具备极低侵入性、高可靠性与强隔离性。

---

## 核心设计特性

1. **绝对业务隔离（原则 1、2、47）**
   - New API 负责核心业务（用户、订单、支付成功判定、原生邀请注册奖励）。
   - Referral Service 负责本次运行周期的充值返佣发放。
   - 任何一方异常或停止，均绝不影响 New API 的用户充值、支付回调与正常使用。

2. **随时暂停与停机不补发设计（原则 6、7、29、30）**
   - **服务启动 / 容器重启 / Resume 恢复**：每次都会开启一个全新运行周期（Run），读取当前 New API 最大的 TopUp ID 作为 `baseline_topup_id`。
   - **只处理新订单**：仅处理 `topup.id > baseline_topup_id` 的订单。
   - **停机与暂停期间订单永久忽略**：不恢复、不补发历史奖励，防止因历史脏数据或重启引发资金风险。
   - **历史未完成订单彻底废弃**：旧 Run 中遗留的 `pending`、`processing`、`failed` 状态订单在重启或暂停时统一标记为 `abandoned_restart`。

3. **纯后台高可用架构（根据需求无管理界面）**
   - 免去复杂臃肿的前端界面，提供轻量可靠的 HTTP 控制端点与 Docker 健康检查探针（`/health`）。
   - 内存占用仅约十几 MB，响应极快。

4. **严格数据边界（原则 18、19、20、21、22）**
   - **环境变量描述“系统应该怎么运行”**：所有系统配置（Token、MySQL DSN、返佣比例等）均来自环境变量，禁止通过数据库或页面热修改。
   - **MySQL 记录“系统实际运行过什么”**：MySQL 仅记录四类事实：`service_runs`、`referral_orders`、`audit_logs`、`system_logs`。

5. **高精度返佣计算（原则 10、11）**
   - 返佣基数是充值用户实际入账的 `creditedQuota`，而非简单的 `money * rate`。
   - 严格适配各渠道规则：
     - `epay` / `waffo` / `waffo_pancake`: `floor(Amount * 500000)`
     - `stripe`: `floor(Money * 500000)`
     - `creem`: `Amount`
     - 未知 Provider: 跳过并记录 `skipped_unsupported_provider`，绝不猜测。
   - 全程使用 `shopspring/decimal` 高精度计算，向下取整（`floor`）。

6. **邀请人可信度验证（原则 12、13、14、15）**
   - 邀请关系严格以 New API 的 `users.inviter_id` 为准。
   - 若 `inviter_id <= 0`，标记 `skipped_no_inviter`。
   - 若邀请人明确不存在（404/deleted），标记 `skipped_inviter_not_found`，该笔永久作废。
   - 若遇临时网络抖动（5xx/超时），标记 `failed` 并在当前 Run 内重试。

---

## 目录结构

```text
referral-service/
├── api/
│   └── server.go             # HTTP 服务路由（/health, /api/status, /api/pause, /api/resume 等）
├── config/
│   └── config.go             # 环境变量加载与敏感信息脱敏
├── model/
│   └── model.go              # MySQL 实体模型与 AutoMigrate
├── service/
│   ├── calculator.go         # 各 Provider 入账与高精度 Decimal 返佣计算
│   ├── calculator_test.go    # 计算器单元测试
│   ├── cleaner.go            # 日志与审计定期清理器（永久保留业务订单）
│   ├── newapi_client.go      # 与 New API 通信的 OpenAPI 客户端
│   └── worker.go             # 核心轮询返佣引擎与生命周期管理
├── .env.example              # 环境变量配置模板
├── docker-compose.yml        # Docker 独立部署编排
├── Dockerfile                # 多阶段构建、non-root 镜像
├── go.mod / go.sum
└── main.go                   # 主入口与平滑停机
```

---

## 部署与使用指南

### 1. 配置环境变量

复制 `.env.example` 为 `.env`：

```bash
cp .env.example .env
```

修改关键配置：

```env
NEWAPI_BASE_URL=http://new-api-host:3000
NEWAPI_ACCESS_TOKEN=your_admin_pat_token

MYSQL_DSN=root:your_mysql_password@tcp(mysql-host:3306)/referral?charset=utf8mb4&parseTime=True&loc=Local

COMMISSION_RATE=0.10
POLL_INTERVAL_SECONDS=15
```

### 2. Docker Compose 一键启动

```bash
docker compose up -d
```

### 3. 查看运行日志

```bash
docker compose logs -f referral-service
```

---

## 控制与状态端点

本服务监听在 `HTTP_PORT`（默认 8080），提供简洁的 HTTP 接口：

| 请求方法 | 端点 | 功能说明 |
|---|---|---|
| `GET` | `/health` | 服务健康检查探针（返回数据库连通性、当前 Run 状态） |
| `GET` | `/api/status` | 只读查看当前运行周期、Baseline TopUp ID、脱敏配置 |
| `POST` | `/api/pause` | **随时暂停监控**：停止拉取新订单，废弃当前中间单 |
| `POST` | `/api/resume` | **恢复监控**：开启新 Run 周期，重置 Baseline，暂停期间订单全部忽略 |
| `GET` | `/api/orders` | 只读分页查看返佣订单记录（支持 `?status=&run_id=` 过滤） |
| `GET` | `/api/audit` | 只读分页查看系统审计日志 |
| `GET` | `/api/logs` | 只读分页查看系统运行日志 |

---

## New API 端极薄接口说明

New API 官方代码仅修改 1 个文件（`router/api-router.go`），新增 1 个接口：

- **路由**：`POST /api/user/aff/reward`
- **鉴权**：`AdminAuth()`（管理员 Bearer Token）
- **职责**：
  1. 验证 Payment Compliance
  2. 验证目标用户存在
  3. 原子增加 `aff_quota` 与 `aff_history`
  4. 写入系统邀请充值日志
  5. 严格不增加 `aff_count`，不直接增加普通 `quota`，不修改 `inviter_id`
