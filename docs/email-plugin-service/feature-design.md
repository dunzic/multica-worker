# Feature：邮件服务内置 MCP

## 定位

该 feature 为智能体提供通知邮件能力，用于“每日情报摘要 + 任务发布 + 全员提醒 + 邮件投递”等工作流。智能体只通过 daemon 挂载的内置 Email MCP 和 Multica 邮件网关调用，不直接接触 SMTP、邮件供应商 API、服务端密钥或 OAuth 凭据。

上线审批是独立于智能体调用权限和插件安装权限的第二道授权。Prompt 只能指导行为，不能构成权限边界；生产发送必须由邮件网关在服务端强制判定。

当前策略评审结论：`GO_FOR_DESIGN`，生产 `NO_GO`。

## 目标

- 未审批版本可以预览邮件，并且只能向该智能体 owner 的登录邮箱发送明确标记的沙箱测试邮件。
- 已审批版本只能使用批准的邮件接口、发件人、精确收件人白名单和额度发送生产邮件。
- 智能体提示词、邮件插件制品、邮件接口、发件人、收件人白名单或额度发生语义变化时，旧审批立即失效并自动产生新申请。
- 支持 HTML、纯文本、多个 `to`、主题和幂等键；返回 Multica 消息 ID 与可查询状态。
- 对确定未被供应商接收的 `429`，以及供应商书面保证同一幂等键可安全重放的 `5xx` 有界重试；永久失败明确报告；接收结果不明时进入 `ambiguous`，不得盲目重发。
- 验证码邮件保持完全独立，不进入通知邮件 API、队列、日志、任务或任务正文。

## 角色与权限

| 角色 | 能力 |
| --- | --- |
| 智能体 task token | 预览、发送、查询自己创建的消息；不能配置、审批、撤销或选择运行模式 |
| 智能体 owner | 以人类身份配置邮件策略、申请上线、查看状态和审计；不能绕过唯一审批人 |
| 唯一上线审批人 | 审批、拒绝、撤销当前配置版本；由部署环境变量指定，并解析为稳定 `user_id` |
| 其他 `owner/admin` | 可查看治理状态；不能代替唯一上线审批人批准生产权限 |
| 普通 `member` | 不获得审批权，也不能因可调用共享智能体而扩大邮件权限 |

部署通过以下配置指定唯一上线审批账号：

```dotenv
MULTICA_EMAIL_PRODUCTION_APPROVER_EMAIL=wells.chen@transcendera.ai
```

启用 feature 时，服务端必须在每个目标工作区中把该环境变量规范化后解析为唯一用户 UUID，并验证其当前角色为 `owner` 或 `admin`。不得按显示名比较，不得信任客户端提交的审批人 ID。配置为空、解析失败、匹配不唯一、成员被移除或角色降级时 fail closed，并阻止新的生产 claim；既有生产审批立即变为无效。

该变量是部署配置，修改后需要重启 backend。服务端为规范化邮箱和解析后的 UUID 计算 `approval_authority_digest`；审批记录保存该摘要，生产准入同时要求当前摘要与审批摘要一致。因此修改 `.env` 后旧审批会立即失效，新审批人必须重新批准，不依赖异步撤销任务。

## 行为矩阵

| 操作 | 未审批 owner 邮箱 | 未审批其他邮箱 | 已审批白名单 | 已审批非白名单 |
| --- | --- | --- | --- | --- |
| 预览 | 允许 | 允许，标明不可发送 | 允许 | 允许，标明不可发送 |
| 沙箱发送 | 允许 | 拒绝整封 | 仅 owner 邮箱允许 | 拒绝整封 |
| 生产发送 | 拒绝 | 拒绝 | 允许 | 拒绝整封 |

“本人邮箱”固定指 `agent.owner_id -> user.email`，不是任务触发者、请求 payload 或智能体自述的地址。无 owner、owner 已离开工作区或邮箱不可解析时，只允许预览。

## P0 范围

- 通知邮件接口 `notification-email/v1`，一个受管邮件供应商路由和一个受管 sender identity。
- `to`、`subject`、`html_body`、`text_body`、`idempotency_key`；至少一个正文非空。
- 预览、owner 沙箱测试、生产发送、消息状态查询。
- 版本化邮件策略、配置哈希、追加式审批/撤销审计、自动失效和重新申请。
- 服务端精确收件人白名单、单封收件人数、每分钟和每日硬额度。
- 持久投递队列、租约 fencing、供应商幂等、有界重试、永久失败与 `ambiguous` 状态。
- 全局生产 kill switch、工作区和智能体灰度 allowlist。
- 内置 stdio MCP 数据面与人类控制面 CLI 命令。

## P1 与明确非目标

P1 包含 Web/Desktop 审批中心、双人审批、审批有效期、退信/投诉/suppression webhook、模板、附件、CC/BCC、自定义 sender、供应商故障切换和成本预警。

P0 不包含营销邮件、通讯录导入、动态域名白名单、`@all` 自动展开、验证码复用、任意供应商 URL、Agent 自带凭据或同步 SMTP 发送。动态成员集合若影响生产收件人，必须形成新配置版本并重新审批。

## MCP 与控制面契约

智能体数据面只暴露三个 MCP tools：

```text
preview({to[], subject, html_body?, text_body?})
send({to[], subject, html_body?, text_body?, idempotency_key})
status({message_id})
```

Email MCP 是 `multica` 二进制内的隐藏 stdio 服务，由 daemon 以 server-owned `multica-email` 名称后置挂载。它只接受 daemon 注入的 `mat_` token、`MULTICA_SERVER_URL`、`MULTICA_AGENT_ID`、`MULTICA_TASK_ID` 和 `MULTICA_WORKSPACE_ID`；不得从 tool 参数、Agent 配置或用户 profile 获取身份。tools 不接受 Agent ID、workspace、task、mode、from、approval、供应商 URL 或凭据字段。

MCP 配置中只保存固定 binary path 和 `email-mcp` 参数，不保存 task token。Agent 自定义的同名 MCP entry 必须被 daemon 的后置 server-owned entry 覆盖。

人类控制面：

```bash
multica agent email configure <agent-id> --from notifications@example.com --allow-to a@example.com --per-minute 10 --per-day 200
multica agent email request-approval <agent-id>
multica agent email approval-status <agent-id>
multica agent email approve <agent-id> <request-id>
multica agent email reject <agent-id> <request-id> --reason <reason-code>
multica agent email revoke <agent-id> <approval-id> --reason <reason-code>
```

审批、拒绝和撤销命令必须拒绝 daemon-managed execution context，并在服务端再次验证唯一审批人 UUID。

## 稳定错误码

- `EMAIL_APPROVAL_REQUIRED`
- `EMAIL_APPROVAL_STALE`
- `EMAIL_SELF_TEST_ONLY`
- `EMAIL_RECIPIENT_NOT_ALLOWED`
- `EMAIL_RECIPIENT_ALLOWLIST_REQUIRED`
- `EMAIL_RATE_LIMITED`
- `EMAIL_IDEMPOTENCY_CONFLICT`
- `EMAIL_DELIVERY_AMBIGUOUS`
- `EMAIL_DELIVERY_PERMANENT_FAILURE`
- `EMAIL_APPROVER_UNAVAILABLE`

错误响应必须提供机器可读 `error_code` 和安全的行动建议，不返回供应商原始响应、凭据、完整正文或验证码。

## 已确认与待补输入

已确认当前部署配置为：

```dotenv
MULTICA_EMAIL_PRODUCTION_APPROVER_EMAIL=wells.chen@transcendera.ai
```

其他部署可以调整该值，但同一部署同一时刻只支持一个生产邮件审批账号。

需求提到“默认仅发送给两个地址”，但当前上下文没有给出这两个完整邮箱。P0 必须保持生产白名单为空，并以 `EMAIL_RECIPIENT_ALLOWLIST_REQUIRED` 拒绝审批，直到明确配置真实地址；不得猜测、使用占位符或自动把审批人邮箱当作生产白名单。

仍需在实现前冻结：默认 sender identity、每分钟/每日额度、单封最大收件人数、沙箱 sender、正文大小上限和审批是否过期。

## 验收标准

1. 任意未审批、旧哈希、已撤销或审批人失效的版本都不能在供应商调用前获得生产 claim。
2. 沙箱实际收件人只能是服务端解析出的智能体 owner 邮箱；多收件人中一个不符即整封拒绝。
3. 提示词、插件制品、接口、sender、白名单或额度变化后，旧批准立即失效，历史保留，新 pending 申请自动创建。
4. 相同幂等键和相同 payload 只产生一条消息；相同键不同 payload 返回 409。
5. 并发请求不会超发额度，服务重启和 worker 故障不会重复发送。
6. Agent、MCP、CLI、API、日志、指标、任务、任务正文和审计都不能读取或输出供应商凭据与验证码。
7. 供应商 `accepted` 与真实 `delivered` 明确区分；状态不明不会被报告为失败后自动重发。
8. 唯一审批人按稳定 UUID 执行；其他管理员、普通成员、task token、Cloud PAT 和跨工作区用户均不能审批。
