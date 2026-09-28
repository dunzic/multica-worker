# 邮件插件服务架构

## 核心不变量

1. Agent 身份只来自服务端验证的 `mat_` task token。
2. 生产授权要求 `task pinned digest == current policy digest == approved digest`。
3. 准入、幂等 claim、额度预占和队列写入是一个 PostgreSQL 事务。
4. Worker 每次调用供应商前重新检查当前策略与撤销状态。
5. 邮件供应商凭据只存在于网关/worker 的服务端 secret provider 中。
6. 通知邮件与验证码使用不同 API、权限、队列、日志类别和 kill switch。

现有 `plugin_grant` 仅控制插件 contribution，不能表达某个智能体配置版本的生产邮件权限。现有 `EmailService` 是验证码/邀请的同步 SMTP/Resend helper，也不具备持久幂等、额度或歧义处理，因此不得直接暴露给智能体。

## 组件

```text
Agent Runtime
  -> built-in Email MCP (stdio, task environment only)
  -> task-token auth
  -> Email Admission Gate
       -> task/config snapshotter
       -> approval policy store
       -> recipient policy
       -> quota reservation
       -> idempotency + message ledger
  -> durable queue
  -> Email Worker
       -> final authorization check
       -> opaque credential provider
       -> notification provider adapter
       -> receipt / retry / ambiguous reconciliation
```

`Preview` 只运行规范化和策略评估，不创建供应商请求、不预占额度、不进入队列。

内置 MCP 是协议适配层，不是新的授权主体。它复用 daemon 已获取的 `mat_` task token，不签发或持久化长期 MCP API key。daemon 在 runtime 和 Agent MCP 配置合并后追加保留名 `multica-email`，固定到当前 `multica email-mcp` 二进制入口；token 只随 task environment 进入子进程，不能写入持久化 `agent.mcp_config`。远程 HTTP MCP 若未来确有需要，应通过 token exchange 签发短 TTL、`audience=email-mcp`、绑定 task/config digest 的 opaque session token，并仍在每次调用时实时回查任务状态。

## 配置哈希

服务端对版本化 canonical JSON 做 SHA-256。禁止直接 hash 任意 JSONB 文本；数组排序、邮箱规范化、IDN、缺省值和数字编码必须固定。

```json
{
  "schema": "agent-email-policy/v1",
  "agent_id": "uuid",
  "agent_prompt_digest": "sha256:...",
  "plugin": {
    "release_id": "uuid",
    "artifact_digest": "sha256:...",
    "entry_digest": "sha256:..."
  },
  "interface_version": "notification-email/v1",
  "provider_route": {
    "id": "uuid",
    "digest": "sha256:..."
  },
  "sender": {
    "id": "uuid",
    "from_address": "notifications@example.com"
  },
  "recipient_policy": {
    "schema": "agent-email-recipient-policy/v1",
    "allowlist": ["a@example.com", "b@example.com"]
  },
  "rate_policy": {
    "schema": "agent-email-rate-policy/v1",
    "provider_per_minute": 100,
    "provider_per_day": 10000,
    "workspace_per_minute": 50,
    "workspace_per_day": 2000,
    "agent_per_minute": 10,
    "agent_per_day": 200,
    "sender_per_minute": 100,
    "sender_per_day": 10000,
    "max_recipients_per_message": 20
  }
}
```

哈希包含可影响生产权限的智能体固定提示词、邮件插件 release/artifact/entry、接口契约、供应商路由语义、sender、白名单和全部额度。Issue 内容和每封正文不属于上线配置。密钥值不进入哈希；凭据轮换不撤批，接口、路由权限或 sender 变化必须撤批。

邮箱 canonicalization 固定为严格 addr-spec：只接受 ASCII dot-atom local part，domain 使用 IDNA Lookup 转 ASCII，随后整地址小写；拒绝显示名、注释、quoted local part、Unicode local part 和控制字符。收件人列表按 canonical 地址去重并排序；不执行 Gmail dot/plus folding。该规则同时用于 sender、收件人策略和审批人环境变量。

审批 authority 使用独立的版本化 canonical JSON：`schema=agent-email-approval-authority/v1`、规范化邮箱和解析后的稳定 `user_id`。当前成员关系与 `owner/admin` 角色不进入摘要，但在每次解析和最终授权时实时检查；成员被移除或降级时立即 fail closed。

任务入队时必须固定 `agent_prompt_digest` 与插件执行清单摘要。只看发送时最新 Agent 行会让旧任务借用新审批，属于权限穿越。

任务行保存 `email_agent_config_digest`、`email_agent_policy_version_id` 和 `email_agent_approval_id` 三元快照。普通任务只在其 immutable `plugin_execution_manifest` 精确包含 policy 指定的 release/artifact/entry 时固定；自动 retry 只继承父任务快照，不重新解析当前审批。Agent 名称或 instructions 变化会同步把现有非 disabled 邮件 policy 退回 draft，使已排队 production 消息在 worker 最终授权处 fail closed。

## 数据模型

所有关系由应用层维护，不添加 foreign key。每张表都带 `workspace_id`，所有查询都按 workspace 过滤，workspace 删除事务显式清理子记录。

### `agent_email_policy_version`

不可变策略版本：

- `id, workspace_id, agent_id, version`
- `config_digest, agent_prompt_digest`
- `plugin_release_id, plugin_artifact_digest, plugin_entry_digest`
- `interface_version, provider_route_id, provider_route_digest`
- `sender_identity_id, from_address`
- `recipient_policy JSONB, rate_policy JSONB`
- `created_by, created_at`

### `agent_email_policy_state`

每智能体一行的线性化指针：

- `workspace_id, agent_id`
- `current_policy_version_id, current_config_digest`
- `assigned_approver_user_id`
- `approval_authority_digest`
- `state: draft|pending|approved|revoked|disabled`
- `updated_at`

### `agent_email_approval`

追加式决策日志：

- `id, workspace_id, agent_id, policy_version_id, config_digest`
- `approval_authority_digest`
- `decision: approved|rejected|revoked|superseded`
- `request_key_digest`
- `actor_user_id, reason_code, created_at`

旧审批永不原地改写。当前有效状态由最新决策投影得到。配置变化在同一事务中创建新 policy version、追加旧版本 `superseded` 决策并创建新 pending request。

### `email_message`

- `id, workspace_id, agent_id, task_id`
- `mode: sandbox|production`
- `policy_version_id, approval_id`
- `idempotency_key_digest, request_digest`
- `sender_identity_id, intended_to, effective_to`
- `subject_digest, body_digest, encrypted_payload_ref`
- `status: queued|sending|accepted|failed_permanent|ambiguous|dead|cancelled`
- `attempt_count, lease_token, lease_expires_at, next_attempt_at`
- `provider_message_id, provider_status, last_error_code`
- `created_at, accepted_at, completed_at`

正文若为异步重试而持久化，必须进入 KMS/信封加密对象或密文列并短期保留；账本、审计、日志和指标只保存摘要与安全元数据。多个收件人的供应商状态用独立 `email_delivery_recipient` 行记录，避免部分成功后整体重发。

必须使用独立 concurrent index migration：

- unique `(workspace_id, agent_id, version)`；
- unique `(workspace_id, agent_id, idempotency_key_digest)`；
- `(workspace_id, agent_id, config_digest, created_at DESC, id DESC)`；
- queue partial index `(workspace_id, next_attempt_at, created_at, id) WHERE status = 'queued' AND attempt_count < 20`；
- expired lease partial index `(workspace_id, lease_expires_at, id) WHERE status = 'sending'`。

## API

### MCP 后端数据面

```http
POST /v1/emails/preview
Authorization: Bearer mat_...
Content-Type: application/json

{
  "to": ["a@example.com"],
  "subject": "Daily brief",
  "html_body": "<p>...</p>",
  "text_body": "..."
}
```

返回规范化 sender/recipient、摘要、effective mode 和审批缺口，不触网。

```http
POST /v1/emails/send
Authorization: Bearer mat_...
Idempotency-Key: required

{
  "to": ["a@example.com"],
  "subject": "Daily brief",
  "html_body": "<p>...</p>",
  "text_body": "..."
}
```

请求体不接受 Agent ID、workspace、task、mode、approval ID、sender、provider URL 或凭据。服务端从 task token 和当前策略解析它们。成功返回 `202`：

```json
{
  "message_id": "uuid",
  "mode": "sandbox",
  "status": "queued",
  "production_authorized": false
}
```

未审批 `send` 的 `to` 必须精确等于 owner 邮箱；网关不能静默把任意目标改写为 owner 邮箱。

```http
GET /v1/emails/{message_id}
```

只允许同一 task/Agent 或治理管理员读取安全状态。供应商接收使用 `accepted`，不得在没有 webhook/readback 证据时返回 `delivered`。

### 人类控制面

- `POST /api/workspaces/{id}/agents/{agentId}/email-policy`
- `POST /api/workspaces/{id}/agents/{agentId}/email-approval-requests`
- `POST /api/workspaces/{id}/agents/{agentId}/email-approvals/{approvalId}/approve`
- `POST /api/workspaces/{id}/agents/{agentId}/email-approvals/{approvalId}/reject`
- `POST /api/workspaces/{id}/agents/{agentId}/email-approvals/{approvalId}/revoke`
- `GET /api/workspaces/{id}/agents/{agentId}/email-policy/status`
- `GET /api/workspaces/{id}/agents/{agentId}/email-audit`

审批类端点要求人类 credential、当前工作区成员关系和精确匹配 `MULTICA_EMAIL_PRODUCTION_APPROVER_EMAIL` 已解析的用户 UUID。通用 `RequireHumanActor` 对未来未知 actor source 的语义不足以单独构成此门禁，handler 必须使用严格 actor allowlist。

部署当前配置为：

```dotenv
MULTICA_EMAIL_PRODUCTION_APPROVER_EMAIL=wells.chen@transcendera.ai
```

启动时规范化该邮箱，并在每个工作区解析唯一 `owner/admin` UUID。服务端根据配置邮箱和 UUID 计算版本化 `approval_authority_digest`，将其写入 policy state 和每条 approval。生产 admission 与 worker final check 都要求当前 digest 匹配 approval digest。环境变量为空、解析失败或重启后值发生变化时，旧审批不再有效；新 authority 必须重新审批。

## 原子准入

HTTP 事务：

1. 从 task token 校验 workspace/agent/task/user 与任务有效性。
2. 锁定 Agent 和 `agent_email_policy_state`。
3. 读取任务 pinned digest，重算当前 config digest，查询同 digest 的最新有效审批。
4. 规范化 envelope recipients，整封执行 owner/allowlist/suppression/单封上限检查。
5. 以数据库时间原子预占 Agent、workspace、sender 和全局额度。
6. 以唯一幂等约束写入 `email_message`；同 key 异 payload 返回 409。
7. 写入内容无关审计并提交；返回 202。

Redis 只能作为性能预检，不能作为最终额度授权。Redis 或 PostgreSQL 安全依赖不可用时 production fail closed。

## Worker 与失败语义

Worker 使用 `FOR UPDATE SKIP LOCKED`、有期限 lease 和 fencing token 横向扩容。调用供应商前重新核验 task/config/approval；撤销先提交则消息进入 `cancelled`。Worker 已越过最终授权线性化点并调用供应商后，撤销不能召回邮件，此边界必须记录授权时间。

供应商调用使用稳定 key `multica:{message_id}`。错误分类为：

- `definite_retryable`：明确未接收的 429，或供应商书面保证同一幂等键安全重放的可重试 5xx；遵守 `Retry-After`，指数退避 + full jitter；
- `definite_permanent`：无效地址、无效 sender、内容拒绝，不重试；
- `ambiguous`：写后超时、连接中断、成功无 message ID、本地 receipt 提交失败，先 readback/reconcile，不盲目重发。

当前 Resend adapter 依赖其 [`POST /emails` 幂等契约](https://resend.com/docs/dashboard/emails/idempotency-keys)：同一 key 在 24 小时内安全重放并返回同一结果。因此 adapter 可将明确的 5xx 响应标为 `definite_retryable`，但 worker 的总重试窗口必须短于 24 小时。该结论不得自动继承给其他 provider；缺少等价契约或无法在窗口内完成重放时必须进入 `ambiguous`。

认证失败触发 provider route 熔断和告警，不允许 Agent 无限重试。达到有限尝试/总时限后进入 `dead`，返回安全错误码。

## 运行与扩容

- 四层硬额度：provider global、workspace、Agent、sender；多收件人按实际 recipient 数计费。
- 全局 kill switch 同时阻止新 production admission 和 worker production dequeue，但不影响预览、沙箱、验证码和邀请。
- 指标包含 queue depth/age、admission latency、accepted/permanent/ambiguous/dead、429/5xx、审批/hash/白名单/额度拒绝；不得使用邮箱或正文作为 label。
- 日志只记录 message ID、Agent/workspace 哈希或 ID、安全 error code 和 provider route ID；邮箱默认掩码。
- P0 只启用一个支持供应商幂等和状态查询的 HTTP provider。原始 SMTP 无可靠幂等/readback，只允许既有验证码/邀请或开发沙箱，不进入生产 Agent 路径。
