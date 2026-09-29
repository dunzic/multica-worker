# 邮件插件服务生产验证

状态：任何生产 cohort 前强制执行。当前尚未通过。

所有测试使用隔离的 PostgreSQL 17、测试专用加密 payload store 和测试邮件供应商账号。默认测试只能使用 `httptest` provider；真实供应商测试必须显式 opt-in，且只允许发往已批准的测试邮箱。

## Gate A：身份与权限

- `mat_` token 中的 workspace/Agent/task 覆盖伪造 Header 和 body 字段。
- PAT、JWT、Cloud PAT、普通成员、其他管理员和跨工作区用户不能调用 Agent send 或审批。
- `MULTICA_EMAIL_PRODUCTION_APPROVER_EMAIL` 解析为一个稳定 UUID，且当前角色为 `owner/admin`；变量为空、0 个、多个、移除或降级均 fail closed。
- 调整该环境变量并重启后，旧 approval authority digest 不匹配，所有旧生产审批立即失效，只有新账号可以重新批准。
- Agent 为 `public_to` 时，任意调用者仍不能扩大 sender、白名单或额度。
- 请求中的 `sandbox`、`production`、`from`、provider URL、approval/config hash 不影响服务端判定。

通过标准：任何伪造路径都在 provider call 前被拒，跨租户响应不泄漏审批、消息 ID、额度或邮箱。

## Gate B：配置哈希与撤销竞态

- canonical JSON 顺序、白名单输入顺序和重复项不改变 digest。
- Prompt、插件制品、接口、provider route、sender、白名单或任一额度的语义变化改变 digest。
- 凭据轮换不改变 digest；权限范围或 sender 变化必须改变。
- 配置更新、旧审批 supersede 和新 pending request 在同一事务提交。
- 回滚到历史相同配置不会复活旧审批。
- 旧任务 pinned digest 不能使用新审批。

对 `send vs configure`、`send vs revoke`、`approve vs configure` 各运行至少 100 次真实 PostgreSQL 阻塞竞态。通过标准：只允许一个清晰线性化结果，撤销提交后不产生新的 provider claim。

## Gate C：沙箱、白名单与输入

- owner 邮箱从数据库解析，不接受 plus alias、点号变体或 display-name 推断。
- 未审批多收件人只要一个不是 owner 即整封拒绝。
- 已审批多收件人只要一个不在白名单即整封拒绝。
- `to` 空、重复、超量、非法地址、Unicode/IDN 边界、CRLF、超长主题/正文、未知字段均有固定结果。
- Preview 不触网、不扣额度、不创建 queue/message。
- Sandbox worker 不拥有生产凭据和生产 provider 网络权限。

通过标准：白名单检查作用于最终 envelope recipient，无部分绕过和静默地址改写。

## Gate D：幂等、额度与 worker

- 相同 key + 相同 canonical payload 并发 100 次，provider 只收到一次。
- 相同 key + 不同 recipient/subject/body/config 返回 409。
- 对硬额度发起 `N+100` 并发请求，成功预占精确等于 N。
- 幂等 replay 不重复扣额度；preview 和准入拒绝不扣额度。
- 配额预占、message claim 和审计在同一事务，回滚无幽灵额度。
- 三个 worker 的 lease steal、过期、滚动发布和 `kill -9` 不重复投递。
- PostgreSQL/安全依赖不可用时 production fail closed。

通过标准：10,000 并发/重放场景重复发送数为 0，额度超发数为 0。

## Gate E：供应商故障语义

注入：

- 连续 429、带/不带 `Retry-After`；
- 400/401/403/422；
- 500/502/503；
- DNS/TLS/连接 reset、写后超时、响应截断；
- provider accepted 后本地 receipt commit 失败；
- 成功响应缺少 message ID；
- 畸形或超大 provider response。

通过标准：

- 确定未接收，或供应商书面保证同一 key 可安全重放的 retryable 错误，使用同一 provider idempotency key，并设最大次数和短于供应商保留窗口的总时限；
- 永久失败不重试并返回稳定 error code；
- 认证失败熔断并告警；
- 接收状态不明进入 `ambiguous` 和 reconciliation，不盲目重发；
- 每次延迟重试前重新检查审批与撤销，撤销后 queued retry 被取消。

## Gate F：秘密与验证码隔离

用 canary secret 扫描 Agent 环境、Prompt、MCP 配置、stdio、tool result、CLI stderr、HTTP、日志、trace、metrics、数据库、审计、任务、任务正文、WebSocket 和 panic。

通过标准：

- provider credential/OAuth token 不出现在任何 Agent 可见面；
- 正文和完整邮箱不成为日志或 metric label；
- 验证码 API、模板、队列、sender、权限和指标与通知邮件隔离；
- 通知 API 不接受验证码类型或模板字段；
- Agent task token 不能查询或调用验证码能力。

## Gate G：万人容量与故障演练

容量模型：10,000 个智能体在 60 秒集中触发约 167 RPS，admission 按 3 倍余量测试 500 RPS，并增加单一热点智能体 1,000 并发。

最低目标：

- mock provider 下 admission p99 `< 300ms`，服务错误率 `< 0.1%`；
- 健康 provider 下 queue age p99 `< 30s`；
- 千万级 message、百万级 policy/approval 上关键查询无全表扫描；
- 三副本 worker 可横向扩容，热点 key 不形成全局锁；
- provider 限流时 backlog 有界，恢复后不发生重试风暴。

故障演练覆盖 DB 在 claim 前、provider 返回后和 receipt commit 时故障，worker 四个阶段 `kill -9`，凭据轮换/撤销，provider 长时间 429/5xx，毒 payload，积压恢复和数据库备份恢复。

## Gate H：可观测性、runbook 与灰度

- 全局 production kill switch 在演练中阻止新 admission 和 dequeue，不影响 preview/sandbox/验证码/邀请。
- Dashboard/告警覆盖拒绝率、429、5xx、ambiguous、dead、queue age、额度拒绝、hash mismatch 和 credential fault。
- Runbook 覆盖 provider 故障、ambiguous reconciliation、额度存储故障、凭据泄漏、紧急撤销和积压恢复。
- 首次 rollout 只开放 `wells.chen@transcendera.ai` 对应 owner 的 sandbox；随后只开放一个低额度、精确白名单的已审批智能体。
- 每个 cohort 都能关闭 production 并保留审计、消息状态和未发送队列。

通过标准：架构、产品、测试和 CEO 四方在 rollout Gate 均达到 3/3，并记录 cohort、证据、告警观察窗口和回滚结果。
