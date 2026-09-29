# 邮件插件服务实施状态与评审记录

更新时间：2026-08-17

## 当前状态

| Slice | 状态 | 当前证据 | 下一 Gate |
| --- | --- | --- | --- |
| EPS-01 feature 定义与治理 | 输入阻断 | 产品边界、唯一审批人、状态机、P0/P1 与验收标准已记录 | 冻结两个生产收件地址、sender、额度与 payload 保留策略 |
| EPS-02 配置版本与审批 | canonical 与 enqueue snapshot 基础完成 | migration 399-423、sqlc 查询、append-only 审批账本、typed canonical policy/prompt/authority digest、严格邮箱规范化、workspace authority resolver、任务 config/policy/approval 快照和插件执行清单精确匹配已实现 | 实现 active sender/route resolver 与事务 configure/approve 服务；server binary 导致的 Mika prompt 变化仍须由 resolver 重算 digest |
| EPS-03 Email MCP 与数据面 | MCP 协议适配器完成 / 默认禁用 / handler 未开始 | 内置 stdio `preview/send/status` tools、三版协议协商、严格 JSON-RPC 错误、task-env-only 身份、server-owned daemon 注入、HTTP 合约测试、task-token-only 与 strict-human allowlist middleware 已实现；旧 Agent 数据面 CLI 已移除，人类治理 CLI 保留 | 注册 handler，并执行 task/agent/workspace/status 防御性回查、server feature gate 和 cohort gate；当前真实服务端对 `/v1/emails/*` 返回 404 |
| EPS-04 持久投递与幂等 | 数据层状态机完成 / worker 禁止接线 | ledger/lease SQL、Resend adapter、失败分类、generation fencing、授权后 lease 过期转 `ambiguous`、授权前 lease 恢复和 workspace 前导队列索引已实现，并有真实 PostgreSQL 行为测试 | 实现加密 payload store、单次 provider 调用 worker 与 readback/reconciliation；未接线前不得启用 |
| EPS-05 额度、观测与运维 | 数据层完成 | 四层 advisory-lock 额度预占与窗口索引已实现 | kill switch、灰度 allowlist、指标、告警、容量和恢复证据 |
| EPS-06 production rollout | 阻断 | 只有基础实现和单元/迁移演练证据，无端到端或生产证据 | 完成 production validation 全部 Gate |

## 2026-08-16 策略评审会议

Feature：邮件服务内置 MCP  
Gate：strategy  
最终决策：`GO_FOR_DESIGN` / production `NO_GO`

### 架构专家：1/3

结论：现有 task token、插件执行快照和 channel delivery 语义可复用，但 `plugin_grant` 与现有同步 `EmailService` 不能承担生产授权。阻断项是任务 pinned digest、独立配置审批、原子 admission、持久队列、数据库幂等、fail-closed 额度和歧义 reconciliation。

### 产品专家：1/3

结论：智能体调用权限与邮件上线权限必须正交；“本人”定义为 Agent owner 登录邮箱；唯一审批人必须绑定稳定用户 UUID。两个生产收件邮箱尚未提供，生产白名单保持空并阻断审批。

### 测试专家：1/3

结论：必须用真实 PostgreSQL 证明 send/update/revoke、额度与幂等线性化；默认测试不得调用真实邮件服务。供应商写后超时和没有可验证幂等重放契约的不确定 5xx 进入 `ambiguous`，不得当作普通失败重试。

### CEO：1/3

结论：该能力能补齐智能体从产出到外部通知的闭环，但邮件具有声誉、成本、隐私和合规风险。批准有限 P0 进入实现；在 kill switch、审计、容量、故障恢复和单智能体灰度证据完成前，不批准生产上线。

## 已确认决策

- 唯一上线审批账号由 `MULTICA_EMAIL_PRODUCTION_APPROVER_EMAIL` 指定；当前部署值为 `wells.chen@transcendera.ai`。
- 运行时解析并持久化其稳定 `user_id`，不用显示名做权限判断。
- `.env` 调整并重启后 authority digest 变化，旧审批立即失效并需要重新审批。
- 其他管理员不可代批；审批人失效时 production fail closed。
- 通知邮件与验证码隔离。
- 未审批只能预览或给 Agent owner 邮箱发送沙箱邮件。
- 生产发送使用服务端 sender，不接受请求自定义 `from`。
- 供应商凭据不进入 Agent 环境、Prompt、MCP 配置、tool result、CLI 参数或响应。
- MCP 复用 daemon 的短期 `mat_` task token，不新增长期 MCP key。
- `MULTICA_AGENT_EMAIL_MCP_ENABLED` 默认 `false`；admission handler、加密 payload store 和 worker 接线完成前不得启用。
- 该 daemon 开关只控制 MCP 自动挂载，不是邮件权限边界；生产 kill switch、cohort allowlist 和每次调用授权必须由服务端强制。
- Cursor 暂不自动挂载：其 managed MCP sidecar 会拒绝覆盖仓库已有 `.cursor/mcp.json`；在安全的独立配置注入完成前保持 availability fail-soft。
- 任务邮件授权在 enqueue 时一次性固定 config digest、policy version 和当时有效的 approval；自动 retry 继承原快照，首次 MCP 调用不得懒绑定。
- enqueue 只在任务的 immutable plugin execution manifest 精确包含获批 release/artifact/entry 时固定邮件 policy；Agent 名称或 instructions 变化由数据库触发器立即把非 disabled policy 退回 draft。
- worker 最终授权同时要求任务仍为 `running`、Agent 未归档、sandbox owner/邮箱未变化；production 还要求 server feature gate、cohort gate、当前审批/authority/member role 全部有效。

## 未决产品输入

- 两个默认生产收件邮箱的完整地址。
- 受管 sender identity 和沙箱 sender。
- `per_minute`、`per_day`、单封最大收件人数和正文大小。
- 是否要求审批过期；建议 P0 设置最长有效期并在到期后自动 pending。
- 确认单人工作区中申请人与审批人为同一人的治理降级是否接受。

## 分支

本 feature 在 `feat/email-plugin-agent-cli` 分支迭代。当前分支包含持久化基础、内置 MCP adapter、人类治理 CLI 和 provider adapter，但尚未注册邮件 handler、admission service 或 worker，因此不构成可调用真实供应商的端到端实现，production 仍为 `NO_GO`。

当前 canonical service 会把缺失 route、sender、插件三元组或任一部署额度判为无效策略；authority resolver 对空/非法邮箱、用户不存在、成员移除和角色降级统一 fail closed。环境变量尚未在 router bootstrap 消费，因此当前 server 仍不会创建 production authority。

## 当前验证证据

- `go test ./cmd/multica -run Email -count=1`
- `go test ./internal/daemon -run 'BuiltinEmailMCP|InjectBuiltinEmailMCP' -count=1`
- `go test ./internal/cli -count=1`
- `go test ./internal/integrations/agentemail -count=1`
- `go test ./internal/service -run 'AgentEmail|NormalizeAgentEmail' -count=1`
- `go test ./internal/handler -run 'Require(Human|StrictHuman|TaskToken)Actor' -count=1`
- `go test ./internal/migrations -run AgentEmail -count=1`
- `go test ./cmd/migrate -run '^TestConcurrentIndexCleanupsMatchTheirMigrations$' -count=1`
- 隔离 PostgreSQL 17 从空库完整执行 migration 1-423，并按 423→399 成功逆向；回滚后邮件表和任务邮件 pin 列均不存在。
- `MULTICA_LIVE_AGENT_EMAIL_TEST=1` 通过 12 个真实 PostgreSQL 子测试，覆盖并发单 lease、generation fencing、授权后过期转 ambiguous、未授权恢复、旧任务不能借用后续审批、插件快照不匹配不 pin、终态任务/owner 变化拒绝 sandbox、production feature/cohort gate、撤批、审批人降级和 prompt 变化失效。
- 隔离测试库已删除；现有开发业务库未执行 migration 417-423，邮件 policy/message/task pin 均为 0 行。

整包验证当前还受分支基线问题影响：`cmd/multica` 与 `internal/daemon` 的 role-source symlink 测试在本机失败；`internal/handler` 全包测试还受缺失 Google client 测试环境和既有 `role_source_runtime_attestation_observation` workspace-delete manifest 漂移影响；migration lint 报告 273-318 的既有重复数字前缀，`cmd/migrate` 全包测试报告 migration 321/380 的既有 down cleanup 缺口。这些均非邮件 migration 399-423 引入。
