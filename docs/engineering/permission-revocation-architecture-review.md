# 权限撤销独立架构评审

评审日期：2026-10-15。评审角色：独立架构师 Agent。

**最终结论：本轮服务端与网页/桌面权限改动可交付进入受控平台验收；不通过全平台或生产发布关卡。** R1 的原跨工作区缺陷及第二轮发现的话题任务残余均已修复，两个独立失败复现已原样转绿；R3 已停止全部五处整域 CDN cookie 签发/续期，认证及头像回归通过。最终五包回归 5,369 项通过、0 失败、71 项环境跳过，明确排除了已证实的基线删除清单失败。R2 按用户选择不纳入本轮移动端开发，仍是未关闭的发布阻断项，不属于已修复或已接受风险。来源上下文清理曾连续失败、未改该子系统随后复测通过，根因未定，仍须跟踪可重复性风险。真实平台与历史 CDN 凭证另有未完成的上线门槛，不能由本次代码评审代替。没有发现可确认的 P0 或额外 P3 问题，不代表未检查的运行环境不存在问题。

本结论是技术关卡，不代替产品、测试负责人或上线责任人的验收，不构成提交、推送、合并、打包或部署授权。

## 范围与依据

- 仓库：`multica-worker`，分支 `codex/next-iteration-20261015`，基线提交 `025e0e40e547c51e8bfc3724bb6788903dad1f70`。
- 社区来源：PR 9249 固定提交 `f05b5519e4aac8e4dcd396e6ac0e63fe44d85ffe`，PR 8896 固定提交 `1801c01ca9fa12eadb9782beec44e77167ad02fa`；按本仓库群组、任务凭证和持久化中继做适配，不能用上游测试替代本地检查。
- 已阅读仓库规则、[社区同步记录](../../COMMUNITY_SYNC.md)、[执行计划](permission-revocation-execution-plan.md)、[开发验证记录](permission-revocation-verification.md)，覆盖最终候选全部 29 个已跟踪产品/测试差异及 13 个新增服务端文件；文末记录独立指纹。新增文件未因尚未跟踪而遗漏。
- 初审与复审覆盖实时写入授权、有界查询、事务提交后撤权、中继和重新加入、附件 HMAC 和真实认证身份、工作区/群组/任务令牌撤权、稳定链接与内容/Office 预览、Range、公开头像和本地存储入口，以及当前原生客户端消费附件 URL 的方式。
- 排除依赖缓存、密钥/账号配置、安装包和无关历史改动；没有访问生产数据库或真实简历，没有修改产品代码或既有测试。

初审发现问题后由开发方修复，本评审者未修改产品代码。文末分别保留初始和最终候选指纹。各发现的初始代码位置用于追溯，修复部分引用最终候选位置；后续改动如改变指纹，应重新判断是否需要复审。

## 发现与关闭条件

| 编号 | 优先级 | 当前状态 | 影响 | 关闭门槛 |
| --- | --- | --- | --- | --- |
| R1 | P1 | 技术关闭，原跨工作区及话题残余复现均转绿 | 原缺陷可让有效连接继续收到已撤权来源的延迟内容 | 已按真实工作区、群组及话题重新授权；最终差异、红/绿证据及整包结果已复核 |
| R2 | P2 | 未关闭；用户明确本轮不开发移动端，保留发布阻断 | 默认批量附件响应改成认证路径，当前原生客户端直接加载/打开失败 | 保持已安装客户端可消费的、仍可撤权的响应契约及缓存有效期；范围缩小不能代替修复 |
| R3 | P1 | 应用代码级复审通过；上线运维门槛仍未关闭 | 原新签发 cookie 与历史 CDN 凭证可绕开附件撤权 | 五处新签发/续期已停用并清除浏览器旧 cookie；历史凭证必须另行撤销或隔离 |

### R1：只检查连接的工作区，不能保护用户定向投递的来源内容

位置：`server/internal/realtime/membership.go:151` 的 `writeBatch`；相关投递链为 `server/cmd/server/chat_v2_listeners.go:69`、`:88`，`server/internal/realtime/hub.go:617`、`:632`，以及 `server/internal/realtime/redis_relay.go:124`。

初始实现捕获一批待写帧后，只用连接的 `workspaceID` 和 `userID` 查询当前成员关系。但 `SendToUser` 有意向用户的多个工作区连接投递，群组事件携带的来源 `workspace_id` 可能不是连接所在的工作区。中继重新投递时使用事件原有的收件人列表，并未重新查询来源群组成员。

触发条件：用户原来同时属于 A 和 B，在 A 的私密群组中；A 的事件已生成但在队列或中继延迟。用户被移出 A 或仅被移出 A 的群组后，B 的连接仍然有效。旧事件经 `ScopeUser` 到达 B 连接，写入前仅校验 B，因而仍能写出 A 的消息正文。只关闭 A 的连接、只在事件产生时选收件人或只判断移除事件不能关闭这个窗口。

独立验证不是伪造数据库业务状态或仅靠静态推断：使用真实本地 WebSocket、生产 `deliverEnvelope` 和写入路径，以权限检查器明确允许 B、拒绝 A，向用户作用域注入来源 A 的延迟群组事件，再投递 B 的无敏感对照事件。预期只收到 B，实际首先收到 A 的 `message:sent` 和 `content`，测试退出码为 1：

```text
TestArchitectScopeUserReplayChecksSourceWorkspace
want issue:updated, got {"event_id":"delayed-group-event","payload":{"workspace_id":"revoked-workspace","conversation_id":"private-group","content":"revoked workspace private content","recipient_user_ids":["test-user"]},"type":"message:sent"}, err=<nil>
FAIL github.com/multica-ai/multica/server/internal/realtime 0.016s
```

该实验直接证明工作区撤权绕过；仅移出群组的分支也缺少当前参与者检查，需由修复后的数据库集成测试分别证明，而不能把两种场景算作一次验证。

修复要求：

1. 冻结有界帧批次后，把连接工作区、每帧真实来源工作区，以及私密群组的 `conversation_id` 纳入最新授权检查；同一批次可能涉及多个工作区，不能对整批借用一个来源。
2. 来源 A 无权但连接 B 有效时，只丢弃 A 的内容，保留 B 的正常连接和内容。查询必须检查当前工作区成员及群组有效参与者，不复用旧收件人列表或正向授权缓存。
3. 缺少来源信息的私密消息不能默认为 B。工作区邀请、账号级事件和目标用户的清理通知需要明确、最小化的协议例外，不能靠通用回退放行正文。
4. 自己的 `participant:removed` 可以作为不含正文的清理信号，但仍须防止伪装成清理事件携带内容；个人收件箱、旧会话消息和持久化重放也须检查来源工作区。
5. 回归至少包含：移出工作区、仅移出群组、群组删除、错误工作区/会话配对、A 被撤权而 B 保持正常、同批多来源、乱序/丢失移除信号，以及数据库失败时拒绝私密内容。

第一轮复测：2026-10-15 06:58:55 UTC，在增加来源身份检查的代码上，以同一 overlay、同一测试命令重跑，得到 `PASS`、退出码 0（0.017s）。该证据证明原来的跨工作区复现已被阻断，且 B 对照事件仍送达；当时并未关闭整个 R1，后续继续检查数据库群组授权、全部新增文件和最终快照。

第二轮新增残余：`server/internal/service/task.go:7259` 的 `broadcastTaskDispatch` 展开任务上下文，加入 `topic_id` 和 `workspace_id`，以 `TopicRecipients` 快照定向投递；`:7310` 的 `taskEvent` 和 `:7342` 的 `publishTaskEvent` 同样产生携带话题的生命周期事件。该路径没有 `conversation_id`，当时的逐帧检查只对九种群组事件启用群组授权，任务事件只检查工作区。用户仅被移出群组、仍在工作区时，延迟用户作用域事件仍被允许。

2026-10-15 07:46:16 UTC，新增独立真实 WebSocket overlay `TestArchitectTopicTaskReplayChecksParticipation` 明确允许来源与连接工作区、拒绝任何群组/话题身份，注入保留话题和任务身份的历史 `task:dispatch`，测试退出码 1：

```text
want issue:updated, got {"event_id":"delayed-topic-task-event","payload":{"workspace_id":"source-workspace","topic_id":"private-topic","task_id":"private-task","agent_id":"private-agent"},"type":"task:dispatch","workspace_id":"source-workspace"}, err=<nil>
FAIL github.com/multica-ai/multica/server/internal/realtime 0.016s
```

这证明私密任务/话题元数据泄漏，不声称该实验已包含真实用户提示词；任务上下文非空时原样展开是源码可达的额外暴露面。不能只给新生产事件补 `conversation_id`，因为持久化中继中已有的 `topic_id` 帧仍要安全处理。

第二轮修复与关闭证据：

- `membership_frame.go:42` 将携带 `topic_id` 的内容帧纳入资源身份，`:71` 对私密会话及已知任务生命周期要求可靠来源工作区；群组内容要求 `conversation_id`，拒绝矛盾的双来源。`membership.go:151` 先冻结至多 32 帧再授权，不允许新帧借用正在执行的查询结果。
- `server/pkg/db/queries/member.sql:19` 同一查询按成对身份检查当前工作区成员、`topic → conversation → workspace` 关系及 `left_at IS NULL` 的参与者；两个资源 ID 同时出现时必须配对。`router.go:2740` 返回与输入一致的身份键，覆盖合法 UUID 大小写差异，不靠字符串规范化丢失权限结果。
- 自身 `participant:removed` 的工作区清理例外只输出固定身份字段，重新构造帧并剥离正文/附件，不修改共享原帧。邀请例外只列出四种明确事件，不使用宽泛前缀。无权来源 A 的帧被丢弃，有效 B 连接及对照事件继续使用；失败查询不授权。
- 2026-10-15 07:59:16 UTC，两个独立 overlay 以 `-race` 原样复测均通过，退出码 0，包耗时 1.048s。开发方 `review-topic-red.jsonl` 在修复前真实数据库 + WebSocket 场景中复现仅移出群组、删除群组和删除话题仍收到合成私密任务上下文；`review-topic-green.jsonl` 修复后 101 项通过、0 失败、0 跳过，含成对校验、五种撤权及九类任务事件。
- 有界资源仍保持四个授权 worker、最多 256 项查询批次、有限队列、共同超时预算且无正向 TTL 授权缓存。重放绕过前置撤权信号仍必须走写入前授权；旧移除通知不能依赖陈旧快照误踢重新加入的成员。

R1 已达到应用代码技术关闭门槛。上述九类任务事件包含构造的保留帧，不能概括为全部业务生产者的端到端验收：当前 `ReportTaskMessages` 的纯话题任务没有进入广播条件，`TaskMessagePayload` 本身也不携带话题；以后扩展该生产路径时必须同步携带可靠资源身份并保留授权测试。

### R2：默认批量附件 URL 改变，使不走元数据刷新流程的原生客户端失效

位置：初始候选 `server/internal/handler/file.go:120`，特别是 `:128`、`:129` 和 `:135`；消费者为 `apps/mobile/lib/markdown/markdown-image.tsx:71`、`:93`，`apps/mobile/components/issue/comment-attachment-list.tsx:96`，以及 `apps/mobile/components/issue/composer-attachment-row.tsx:202`。

初始候选忽略 `attachmentURLMode`，默认和显式稳定路径模式都返回 `/api/attachments/{id}/download`。代码注释假定原生客户端会先请求单项元数据获取能力链接，但当前手机端并未这样做：图片直接交给 `RNImage.getSize` 和 `ExpoImage`，普通文件直接用 `Linking.openURL` 交给系统浏览器，均不附带 API Bearer token。`resolveAttachmentUrl` 只补齐站点前缀，不补认证。原来配置 CloudFront 签名时可直接使用的客户端，在后端更新后收到认证路径会得到未认证响应；这不仅是未测试的假设，而是生产调用链与认证路由契约的矛盾。

共享 Web/Desktop 的元数据或 Blob 回退测试通过，不能证明这些手机端调用路径兼容。未进行真实手机验收，不能反过来把可见源码回归视为可忽略。

补充核对了实时更新，纯批量 HTTP 修复不足以关闭该项：`apps/mobile/data/realtime/use-issue-realtime.ts:132`、`:142` 直接追加/替换评论；`issue-ws-updaters.ts:661` 原样保留 `comment.attachments`，`:245` 直接写入 Query 缓存。正常带版本号的更新不重取时间线。`apps/mobile/components/issue/comment-card.tsx:556` 将该缓存的附件传给直接 `Linking.openURL` 的组件。服务端 `server/internal/handler/comment.go:1967`、`:3621` 同时把 HTTP 响应对象用于发布评论事件，因此不能直接让共享序列化函数为请求者签发链接，再把该凭证广播给其他成员。

若采用新版移动端即时认证媒体/下载适配，应先获得本轮范围确认，并把旧版原生客户端仍不能处理稳定事件链接列为未解决的发布兼容门槛，而不是用“列表现在可以打开”关闭 R2。

范围决定：用户已明确选择本轮只完成服务端与网页/桌面，不适配移动端。因此本项持续为发布阻断项；本轮限定范围开发完成并不表示移动端兼容，也不能将该决定解释为接受回归或批准全平台发布。

修复要求：

1. 不恢复不可实时撤权的 S3/CloudFront 直签名。可在已认证且已授权的读取中，为实际请求主体提供仍经 API 每次校验的绝对能力链接；显式声明稳定路径能力的客户端继续获得稳定路径。
2. 不得在公共响应或异步事件中借用上传者、其他成员或智能体身份签发能力链接。用户及可选任务令牌身份必须来自可信认证上下文，不接受客户端伪造身份头。
3. 批量读取与单次立即下载的使用寿命不同。直接复用 60 秒 TTL 会在列表停留或客户端缓存后继续失败；须覆盖合理缓存窗口或提供已安装客户端能执行的刷新机制。不能只验证响应生成后的立即点击。
4. URL 不进入持久化正文/数据库；兑换仍校验工作区、群组和任务凭证，并保留 `no-store`、Range 及错误兼容。
5. 测试需区分默认客户端、稳定模式客户端、上传返回、单项/批量读取、群组回复/事件，以及 URL 过期与撤权。真实 Windows/macOS 和手机端检查仍是发布验收项。

### R3：初审认证路径持续签发整域 CDN cookie，与应用撤权边界冲突

初始位置：`server/internal/auth/cloudfront.go:142` 定义 `Resource=https://<domain>/*` 的策略；生产签发点为 `server/internal/handler/auth.go:473`、`:740`，`server/internal/handler/session.go:131`，`server/internal/middleware/cloudfront.go:29` 及 `server/internal/middleware/session_renewal.go:75`。

触发条件是部署配置了 CloudFront signer，且该分发对受保护附件接受此策略。在这种配置下，工作区移除不会改变用户账号仍能登录/续期的事实，整域 cookie 能让用户绕过新的下载 API 直接读取已知对象路径。这不是只剩历史凭证等待到期的问题：登录、显式 session refresh 和自动续期都能持续得到新的有效整域策略。该机制在基线已经存在，属于本轮新撤权方案必须处理的集成缺口，而非新写入的回归。源码证据足以确认条件性缺口，但没有读取生产配置，不能宣称线上分发一定接受该策略。

修复要求：

1. 应用侧停止新发/续签覆盖受保护对象的整域 cookie，覆盖上述全部入口，包括只挂认证中间件、未挂 CDN 中间件的路由组。
2. 保持登录和刷新 API 本身的状态码、JSON、会话延续及 Bearer/cookie 分支兼容；清除旧浏览器 cookie 必须匹配原名字、Domain 和 Path。清除客户端 cookie 不是撤销被复制的签名凭证。
3. 公开头像保留经过头像类检查的单对象签名或代理；不得为了保持头像加载恢复整域签发。当前生产对象级 `SignedURL` 消费点为 `server/internal/handler/avatar.go:423`。
4. 补登录、显式刷新、滑动续期、独立 CDN 中间件和头像回归。旧 cookie、旧签名 URL、已缓存内容、公开 bucket 仍必须由上线方案处理，不能仅以新代码停止签发关闭该运维门槛。

第二轮修复与关闭证据：`CloudFrontSigner.ExpiredCookies`（当前 `cloudfront.go:146`）只生成三枚匹配原名称、Domain、Path 的空值过期 cookie；所有五处调用已改为清除，`SignedCookies` 已删除。`ExpireCloudFrontCookies` 在发现任意旧 CDN cookie 时清除全组三枚，并避免与本次滑动续期重复写入；只挂 `Auth` 的路由同样清除。登录、显式刷新及自动续期自身的会话行为未被取消。全仓再次检索未发现遗留整域签发入口，唯一生产对象级 `SignedURL` 消费仍在 `avatar.go:423`，受头像分类判断保护。

开发方 `review-cdn-red.jsonl` 已实证修复前登录状态中间件会生成新宽泛凭证；独立执行的中间件/续期测试以 `-race` 通过。`review-login-avatar-race.jsonl` 已独立回读为 38 项通过、0 失败、0 跳过；新 `auth_cdn_cookie_test.go` 两个真实会话测试覆盖邮件验证码和 Google 登录（Google 响应由本地 RoundTripper 提供，无外部登录请求），并用后续 `Auth/GetMe` 证明 session 仍可用，同时确认三枚旧 CDN cookie 全被清除。显式刷新和公开头像回归也在该日志中。

R3 **仅应用代码侧关闭**。清除浏览器 cookie 不会吊销复制到其他设备的签名，不能替代下列上线 CDN/bucket 检查。

## 已核对的边界及非阻断说明

### 认证身份、能力链接和预览

最终候选的 v2 HMAC 覆盖附件 ID、用户 ID、可选任务令牌 ID、到期时间和下载用途；公开兑换入口显式使用已验证主体，不借用 `X-Actor-*` 请求头。任务令牌还需当前有效、匹配用户和工作区，并以对应智能体检查群组身份。无工作区权限和无群组权限不是同一条件，不能只保留前者。

稳定下载、元数据、文本/Office 预览入口共享当前权限检查。正文、评论或回复引用站内附件链接不改变其资源归属；粘贴到有权限的任务不会给文件本身授权。API 新受保护下载不再换取不可撤销的对象存储签名，而是代理读取。已有客户端取得的文件字节、缓存和正在传输的内容无法事后收回。

能力链接仍是可转交的 Bearer 凭证。绑定用户是为了按该主体撤权，不是证明兑换者本人；全账号会话注销和工作区成员移除也不是同一种撤权语义。

### 历史 `chat_message_id` 无 `topic_id` 分支

`server/internal/handler/chat_v2_attachment_access.go:31` 查找 v2 消息，查询仅匹配具有 `topic_id` 的消息；`:32` 的无记录分支保留旧消息附件的工作区权限语义。迁移为历史消息补话题，并不意味着所有附件都持有 `topic_id`；当前旧私聊路径也可能继续产生没有 v2 话题的消息。不能直接把所有 `ErrNoRows` 改成拒绝并声称兼容无损。

现有群组新上传、绑定和输出路径会保存附件 `topic_id`；只有 `chat_message_id` 的可解析 v2 消息仍应追到话题并做群组校验。最终 `attachment_access_test.go:63` 的命名回归已明确验证三类：真实 v1 附件延续原权限、仅消息 ID 的 v2 附件不能跨群组、已有话题 ID 但话题不存在时拒绝。初审和复审均没有把合法 v1 回退列为本轮新缺陷。历史私聊若需要比工作区更严格的隐私策略，应另立迁移和授权设计，不能隐含承诺已在本轮完成。

### 公开头像和本地上传

独立、未绑定的图片继续按既有头像类公开约定处理；这是有意保留的产品边界，不等于任意附件可公开。头像发布/读取判断已把群组话题和来源上下文加入绑定条件。绑定任务、评论、消息、话题或上下文后，不能借头像入口公开；工作区本地文件和附件行删除后的遗留原始路径也应拒绝，不能仅因找不到附件行就放行。

这不撤销此前在头像仍属公开类别时已发出的存储签名、浏览器缓存或已下载内容。头像类对象转为私密后如要求消除所有历史可读窗口，还需要对象键/缓存和 CDN 策略配合。

## 验证证据的层级

| 来源 | 实际核对内容 | 不能推出的结论 |
| --- | --- | --- |
| 独立执行 | 两个 Go overlay + 真实本地 WebSocket 的 R1 失败复现；最终同一测试以 `-race` 转绿；R3 中间件/滑动续期竞态回归通过，未连接数据库 | 不是 Redis 集群或生产环境验收 |
| 独立源码审查 | 初始与最终候选全部相关跟踪差异、新增文件、原生客户端消费者与授权/存储调用链 | 不是实际 Windows/macOS/手机运行证据 |
| 开发方日志回读 | 原基线旧连接和旧能力链接撤权失败；修复后窄回归、194 项竞态集成回归、实时竞态测试；共享前端 5 文件 167 项通过 | 原先测试没有覆盖 R1/R2，不能因为总数多就关闭新发现 |
| 开发方首轮整包证据 | 4 个受影响包显式排除已知基线失败后 5,181 项通过、58 项按环境跳过；Go vet 退出码 0 | 不能用首轮统计替代最终候选结果 |
| 开发方最终整包日志独立回读 | `review-topic-packages-final.jsonl`：5 个包终态均通过，5,369 项通过、0 失败、71 项环境跳过；包含最终话题修复、登录测试与来源上下文生命周期测试 | 包含子测试；显式排除已知删除清单失败，不能写成所有测试无跳过或稳定性风险清零 |
| 最终静态及共享前端复核 | 开发方执行 `go vet` 的实际工具退出码为 0，日志 `review-topic-go-vet.log` 无输出；`review-frontend-final.log` 已独立回读为 5 文件、167 项通过 | 空日志本身不证明成功；共享前端测试不验证手机端或原生安装包 |

开发日志路径与具体命令见[开发验证记录](permission-revocation-verification.md)。`TestWorkspaceDeletionManifestCoversPublicSchema` 在原主线同一隔离库也失败，`baseline-manifest.log` 已确认；属于现有删除清单/条件迁移问题，不列作本轮新增回归，也不隐去。

第二轮新增日志已独立回读：`review-revocation-race-final.jsonl` 为 130 项通过、7 项环境跳过、0 失败；`review-packages-final.jsonl` 为 5 包、5,353 项通过、71 项环境跳过、0 失败；`review-login-avatar-race.jsonl` 为 38 项通过、0 跳过、0 失败；`review-topic-green.jsonl` 为 101 项通过、0 跳过、0 失败。以上计数均包含子测试，不是独立业务场景数量。

**未定性风险：来源上下文清理测试可重复性。** 更新登录测试后的 `review-packages-reviewed.jsonl` 在 07:40:59 UTC 出现 `TestCommentSourceContextLifecycle` 失败，`source_context_integration_test.go:698` 报 `cleanup target delete intents = 1, err=<nil>`；`review-source-context-repeat.jsonl` 随后连续五次同样失败。原主线同一专用数据库连续三次通过（`review-source-context-baseline.jsonl`），紧接当前代码在未修改该子系统/测试的情况下连续三次通过（`review-source-context-recheck.jsonl`）；最终五包回归中该父用例也在 08:08:19 UTC 通过。相关日志均已独立回读。没有证据将前五次失败归为已确认基线、无关抖动或已修复回归；本轮未降低断言或跳过该用例。该不稳定性需另行定位，受控环境验收应复查，不能以最终单轮通过宣称根因已解决。

独立复现实验保存在本机临时路径 `/private/tmp/multica-permission-architect.bFXed0`，测试文件为 `architect_review_test.go`，通过 `overlay.json` 加载到 Go 包，不改变产品或既有测试文件。运行的是 `golang:1.26.9`，经仓库 CLI 防护脚本；最终匹配 `^TestArchitect(ScopeUserReplayChecksSourceWorkspace|TopicTaskReplayChecksParticipation)$`，使用 `-race -count=1 -v`。未访问生产或用户安装的智能体 CLI。临时文件不是版本库内永久证据；正式仓库已加入来源帧、话题和真实数据库/WebSocket 的稳定回归。

## 生产和发布门槛

1. **历史凭证和存储绕行仍是上线门槛。** 目前代码已停止新的整域 cookie 签发/续期，但公开 bucket、已有 CDN 签名 URL、复制过的 CloudFront 签名 cookie 和已缓存字节不会被本次 API 代码自动撤销。必须检查受保护对象 namespace、CDN 策略/签名密钥、旧凭证有效期、边缘缓存和 bucket 访问策略，确认私密对象不能绕过 API；不能只清除当前浏览器后宣称全局撤权。没有读取生产配置，也没有执行真实 CDN 撤权验证。
2. **混合版本必须有方案。** v1 能力链接主动失效；混跑只认识旧格式和新格式的后端会导致间歇错误。需安排部署切换及客户端重新取元数据，不能仅靠接口字段名称未变判定兼容。
3. **真实平台仍待验。** 本轮没有真实 S3/CDN、浏览器端到端、Windows/macOS 原生下载或手机验收；Office 测试使用模拟转换器，只证明权限，不证明 Word/Excel/PPT 转换质量。没有调用真实模型，不构成 HR 业务验收。
4. **旧正文原始地址另行处理。** 本地受保护原始 `/uploads` 地址现在拒绝；历史正文直接嵌入的存储 URL 需按附件身份恢复稳定 API 引用，外部网盘/公开链接依赖来源服务。本轮没有批量改写历史正文。
5. **R2 移动端发布阻断仍未关闭。** 本轮范围排除了移动端实现，不表示后端升级对现有移动客户端无影响；发布者必须先解决默认附件和实时事件的消费兼容，不能因移动端没有发新版就让该服务端回归自动上线。
6. **保留来源上下文失败调查。** 最终回归虽通过，前述重复失败根因未定；不能用删除清单的已知基线问题代替解释这项不同的失败。

## 快照指纹与复审记录

初始快照的已跟踪产品差异（包含前端回归测试）使用以下方式计算：

```text
git diff --binary -- server packages/views/editor/attachment.test.tsx | shasum -a 256
a202333f442ade0dcba67075e8d821e5c693124f3f3b442486d7e87aa33836fc
```

初始新增服务端文件另行计算 SHA-256，避免漏审未跟踪文件：

```text
9c9c64977ee07d0756275bbd25a39af6e1d2b627bd37e5b27759c77d1479f0f1  server/cmd/server/realtime_membership_test.go
24095341bde96880cde0bf00593db5abfc7dfd6f37ca15c3a50e9de141de08ba  server/internal/handler/attachment_access.go
529974fed8d259d34f22ba03bf257e7e0a38de8778bf6f5b3ca8626c5599a84a  server/internal/handler/attachment_access_test.go
05b416aab38032e868012d1d16e4fcb5a6eb8b229a36b719659728facd709a20  server/internal/handler/attachment_capability_revocation_test.go
f4b57d92ad73a08c565bc8b6ebb26a8e55b65e4d68ed1f9aa4ecab85e6fc06ea  server/internal/realtime/membership.go
852a46e90c09b684de0e198786b4241f3b7f80b1ae70cb8635b673bdfe7c33ad  server/internal/realtime/membership_batch.go
e5179557d69f1725cef0ba2b757d176d8e6eec12784ab0a82e5f3f5fee5d0434  server/internal/realtime/membership_performance_test.go
de340cc7df5fa55515992aedb47216b1e23bb180a602dbcaeb9e4e9737d59556  server/internal/realtime/membership_removal.go
6fa5d1ef4eaa74f506a2ad8a0943cc9dc311bbb76b37746bed840b29c5b6153a  server/internal/realtime/membership_removal_test.go
2e07ff1afbc3d71adfb5607108cca08d4998db6f0e2637291bb51b268f526c7d  server/internal/realtime/membership_test.go
```

最终候选的已跟踪产品/测试差异按同一命令独立重算，基线提交仍为 `025e0e40e547c51e8bfc3724bb6788903dad1f70`：

```text
1b2932517593d30c4b84e363e4bc6062f81fd55da62bcf7aa19b8e5e7a474a34
```

最终候选全部 13 个新增服务端文件 SHA-256：

```text
dd53edabfceee42e55bdf5cc0f69a528c18aedad9db966c391e60588c1381f9e  server/cmd/server/realtime_membership_test.go
24095341bde96880cde0bf00593db5abfc7dfd6f37ca15c3a50e9de141de08ba  server/internal/handler/attachment_access.go
cbe320659175985f0bbe6bdd8e7548a5b283573bd237a8fa555a69fd8e4e8e87  server/internal/handler/attachment_access_test.go
05b416aab38032e868012d1d16e4fcb5a6eb8b229a36b719659728facd709a20  server/internal/handler/attachment_capability_revocation_test.go
07ce1f3f8dc21c12d3d67b4116fd639f7726c897a3d1e56707a1d205ca8cfb41  server/internal/handler/auth_cdn_cookie_test.go
72a10fad443627a8ea77f0c453a94ba67bcb8cb300237bf27da12e4107bc07aa  server/internal/realtime/membership.go
6a53e51535f3544d127e3697733ea905e9866132cc6821907b6e2f88cce9cf18  server/internal/realtime/membership_batch.go
017b66864e3f3c02445b851e892deb13881458d803a3ed7c732c9f35320474f2  server/internal/realtime/membership_frame.go
79943f9cf156daf3bde231cd12ad6f784c4bca322e1222dd22f4304487f08d43  server/internal/realtime/membership_frame_test.go
e5179557d69f1725cef0ba2b757d176d8e6eec12784ab0a82e5f3f5fee5d0434  server/internal/realtime/membership_performance_test.go
de340cc7df5fa55515992aedb47216b1e23bb180a602dbcaeb9e4e9737d59556  server/internal/realtime/membership_removal.go
6fa5d1ef4eaa74f506a2ad8a0943cc9dc311bbb76b37746bed840b29c5b6153a  server/internal/realtime/membership_removal_test.go
2e07ff1afbc3d71adfb5607108cca08d4998db6f0e2637291bb51b268f526c7d  server/internal/realtime/membership_test.go
```

复审关卡记录：

- [x] 开发方交付 R1/R3 最终修复和测试，冻结产品源码；R2 按明确范围选择保留发布阻断，不视为关闭。
- [x] R1 两个独立失败复现转绿；复核来源工作区、群组/话题授权、清理帧最小化、SQL/生成结果及有界资源使用。
- [x] R2 检查真实原生消费者及 HTTP/实时缓存链；确认仅修改批量 HTTP 响应不足以关闭，事件不得代签别人的能力链接。
- [x] R3 检查全部五处整域 cookie 停发/清除、认证/刷新兼容和公开头像回归；历史凭证仍列上线运维门槛。
- [x] 复核最终完整差异和全部新增文件，记录最终指纹；回读最终五包结果及中间失败，不将后来通过改写为从未失败。
- [x] 给出限定范围可交付进入受控平台验收的技术结论，不代替业务验收或授权生产/全平台发布。
- [ ] 发布前关闭 R2、真实平台/CDN 门槛，并完成来源上下文清理不稳定性的调查。
