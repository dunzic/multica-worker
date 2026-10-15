# 权限撤销开发验证记录

更新日期：2026-10-15。

本轮已实现工作区实时连接撤权、附件能力链接撤权及本仓库群组和智能体身份适配，并完成独立初审发现的跨工作区／话题消息投递与整域 CDN 凭证问题的修复复审。以下结论来自本地隔离数据库、真实 HTTP 和 WebSocket 测试及共享前端组件测试，可以进入限定范围的受控平台验收。移动端兼容尚未修复；用户已确定本轮仅完成服务端和网页／桌面，将该问题保留为发布阻断项，因此当前不是可直接上线的全客户端版本。结论及快照记录在[架构评审报告](permission-revocation-architecture-review.md)。

## 代码和环境

- 分支：`codex/next-iteration-20261015`。基线：`025e0e40e547c51e8bfc3724bb6788903dad1f70`；实现提交：`e869e6197a5824fb8e47af67f79fefae0cc4fc48`。提交前重新核对跟踪差异及 13 个新增文件，均与独立复审指纹一致。
- 来源：[实时连接候选 PR 9249](https://github.com/multica-ai/multica/pull/9249)，固定版本 `f05b5519e4aac8e4dcd396e6ac0e63fe44d85ffe`；[附件候选 PR 8896](https://github.com/multica-ai/multica/pull/8896)，固定版本 `1801c01ca9fa12eadb9782beec44e77167ad02fa`。
- 测试库：仓库环境脚本创建的 `multica_permission_revocation_20261015_992`，迁移至 610。未使用默认业务库，未重建共享 PostgreSQL，也未修改账号。
- Go：官方 `golang:1.26.9` 容器，镜像摘要 `sha256:37cfcca63222ec499bec5e8ad7aad52dbf7529db36d5812b8eae2f0621aa92d6`。Go 测试经 `scripts/go-test-with-agent-cli-guard.sh`，不调用本机用户安装的智能体 CLI。
- 前端：使用工作区已安装的 Vitest 4.1.0、TypeScript 和 ESLint。全局 pnpm 尝试切换至仓库指定版本时因注册表获取及签名验证失败而中止；未关闭签名验证、修改锁文件或重装依赖。

## 适配结果

实时连接在发送已捕获的一批消息前查询最新成员关系，同时检查连接工作区和每帧内容的来源工作区；群组内容还检查当前有效参与者和工作区／群组配对。仅有 `topic_id` 的任务生命周期帧通过当前话题反查所属群组；同时携带话题和群组时要求两者真实配对，话题或群组已删除时拒绝。批量查询不复用正在查询或已成功的旧授权结果。A 来源失权但 B 连接仍有效时，仅丢弃 A 内容，保留 B 连接。自己的群组移除通知只保留清理界面所需的标识，删除可能夹带的正文或附件字段。

成员移除和主动退出事务提交后关闭对应工作区连接并丢弃队列；移除事件延迟、丢失、队列过载和重新加入的回归由发送前检查及事件再确认共同覆盖。保留群组定向投递和本仓库的持久化中继。

短期附件链接的签名覆盖附件 ID、用户 ID、可选任务令牌 ID、过期时间及下载用途。每次兑换查询工作区和群组权限；使用任务令牌的智能体还须具有未过期、未撤销且绑定同一工作区的令牌。令牌身份来自认证中间件上下文，不信任公开请求头。链接依旧是可转交的短期 bearer capability，不证明兑换者就是申请人。

稳定下载、下载元数据、预览定位、文本和 Office 预览复用当前权限校验；任务正文、评论和回复中的站内链接不因粘贴位置改变权限。预览和下载能力链接在所有存储模式下均由 API 读取存储并流式返回，保留 Range，不再通过附件入口签发 S3 或 CloudFront 直链。字段及认证入口保留，受保护文件的持久化链接使用稳定 API 地址。

本地 `/uploads/*` 不公开工作区受保护文件，也不因附件数据库记录删除后遗留文件而变成公开路径。独立、未绑定的图片保留既有公开头像类约定；绑定任务、评论、群组话题或上下文快照后不能通过头像入口公开。非工作区公开素材和外部链接不改写。

五个整域 CDN cookie 生产入口均停止签发：验证码登录、Google 登录、显式 session refresh、自动滑动续期和 CDN 中间件。原 cookie 以相同名字、Domain 和 Path 过期清理，不再生成覆盖全部对象的策略。公开头像仅保留经公开类别检查的单对象签名。已复制的旧凭证不会因为浏览器清 cookie 而作废，仍须按上线门槛处理。

`make sqlc` 已执行，保留三项新增查询对应的生成结果：实时工作区／用户／群组配对查询、有效任务令牌 ID 查询和存储 URL 附件存在性查询。复审补充显式 UUID 转换后，模块代理临时失败；使用已缓存的同版 sqlc 1.31.1 源码离线构建生成器并成功重新生成，见 `review-sqlc-cached.log` 对应退出码 0。生成器产生的无关任务／消息字段排序变化已排除；没有新增迁移或变更数据库约束。

## 独立评审问题处理

| 问题 | 本轮处理 | 当前门槛 |
| --- | --- | --- |
| R1 跨工作区和群组任务旧消息泄漏 | 按帧校验来源工作区、群组和话题，补最小化移除通知；两个独立失败复现及真实数据库五类撤权回归已转绿 | 最终快照独立复审确认技术关闭 |
| R3 整域 CDN cookie 绕行 | 停止全部五处新签发和续签，清理浏览器旧 cookie；登录、刷新、公开头像回归通过 | 应用代码级独立复审通过；历史凭证和实际 CDN 策略仍须上线前验证 |
| R2 移动端默认附件契约回归 | 本轮不修改移动端。旧手机端直接加载／打开需要认证的稳定地址，仍缺少媒体认证和下载刷新流程 | 用户明确保留为发布阻断，未修复、未风险接受，不可据此直接上线后端 |

## 失败基线和修复后证据

原始快照保存在本机临时目录 `/private/tmp/multica-permission-baseline.wepmz2`，由基线归档生成，只加入缺陷回归测试。证据目录为仓库内忽略路径 `.context/permission-revocation-20261015/`，不含真实简历或生产账号数据。

| 检查 | 实际结果 | 证据文件 |
| --- | --- | --- |
| 原始代码旧连接撤权 | 删除和主动退出后，旧连接未关闭，两个子场景断言失败；事务回滚对照通过 | `baseline.log` |
| 原始代码旧链接撤权 | 成员移除后，旧链接仍返回 HTTP 206 文件片段，断言失败 | `baseline.log` |
| 修复后权限与群组窄回归 | 通过，涵盖真实认证、签名、群组单独移除、正文/评论/回复链接、过时正向缓存、Range 和 Office 权限 | `targeted-fixed.log` |
| 初审前 HTTP/WebSocket 竞态回归 | 194 项通过，含子测试；覆盖任务凭证删除/过期/跨工作区/智能体移出群组、公开头像边界、本地遗留文件和来源权限；不能代替初审新增发现的回归 | `integration-race-final.jsonl` |
| 实时整包竞态检测 | 通过；包含 1024/4096 个写入者的有界批次和故障注入测试，属于合成负载，不是生产容量保证 | `realtime-race.log` |
| 共享前端预览和下载 | 最终重跑 5 个文件共 167 项通过；新增相对能力链接在令牌认证客户端回退到已认证图片读取的回归 | `review-frontend-final.log`；初次记录 `frontend.log` |
| Views 类型检查和改动测试 ESLint | 通过，使用已安装工具直接执行；未完成根 pnpm 工作区检查 | `views-typecheck.log`；ESLint 退出码 0 |
| 受影响 Go 整包测试 | realtime、middleware、cmd/server 通过；handler 仅删除清单测试失败，见下一节 | `packages.log` |
| 初审前 Go 静态分析 | 4 个受影响包通过 | `go-vet.log`，退出码 0 |
| 初审前排除基线失败的整包复核 | 4 个受影响包通过 5,181 项，含子测试；明确跳过删除清单测试，另有 58 项环境相关跳过 | `packages-excluding-known-baseline.jsonl` |
| R1 独立失败复现 | 真实 WebSocket 上，延迟 A 消息错误到达 B；相同 overlay 在修复后通过。另补真实数据库工作区移除、仅群组移除、群组删除、错误工作区配对及 B 连接可用断言 | 详见架构评审报告；正式用例在 `cmd/server/realtime_membership_test.go` |
| R3 停发前失败复现 | 已配置 CDN 的请求仍产生有效 `CloudFront-Policy`，断言失败；修复后对应回归转绿 | `review-cdn-red.jsonl`；`review-revocation-race-final.jsonl` |
| 修复后专项竞态回归 | 5 包共 130 项通过、0 失败、7 项 Redis 环境跳过；包含来源帧、群组撤权、历史 v1/v2 附件及 cookie 退役 | `review-revocation-race-final.jsonl` |
| 登录和头像补充竞态回归 | 38 项通过、0 失败、0 跳过；验证码和模拟 Google 登录保持有效会话，显式刷新及公开头像均正常 | `review-login-avatar-race.jsonl` |
| R1 话题任务消息追加复现 | 仅退出群组或删除话题后，旧收件人任务帧仍到达，正式测试失败；补充话题反查后同场景通过，正常消息和 B 连接不受影响 | `review-topic-red.jsonl`；`review-topic-green.jsonl` |
| R1 最终话题竞态回归 | 101 项通过、0 失败、0 跳过；覆盖帧解析、工作区／群组／话题配对、九种任务事件、移除群组及话题删除等 | `review-topic-green.jsonl` |
| 最终 Go 静态分析 | 5 个受影响包通过 | `review-topic-go-vet.log`，退出码 0 |
| 话题补修前整包回归 | 首次 5 包共 5,353 项通过、0 失败、71 项环境跳过；后一次重跑出现清理生命周期测试失败，不能仅用首次通过覆盖此异常 | `review-packages-final.jsonl`；`review-packages-reviewed.jsonl` |
| 清理生命周期重复核对 | 当前代码单独重复 5 次均失败；随后原主线 3 次通过、当前代码 3 次通过，期间未修改该实现或测试。原因尚未确定，未作为已确认基线失败排除 | `review-source-context-repeat.jsonl`；`review-source-context-baseline.jsonl`；`review-source-context-recheck.jsonl` |
| 最终五包整包回归 | 5,369 项通过、0 失败、71 项 Redis／外部服务环境跳过；明确排除原基线删除清单测试，包含并通过上述清理生命周期用例 | `review-topic-packages-final.jsonl`，退出码 0 |

典型复现命令在 `server/` 执行，必须显式配置专用测试库，不能落到默认数据库。容器内使用同一组命令，并挂载工具缓存及源码：

```bash
bash ../scripts/go-test-with-agent-cli-guard.sh go test -p 1 ./internal/realtime ./internal/middleware ./internal/handler ./cmd/server -count=1
bash ../scripts/go-test-with-agent-cli-guard.sh go test -race ./internal/realtime -count=1
bash ../scripts/go-test-with-agent-cli-guard.sh go test -race -p 1 ./cmd/server ./internal/handler -run '^(TestRealtime|TestAttachment|TestGetAttachment|TestDownloadAttachment|TestBuildMarkdown|TestServeLocal|TestLocalGroupFiles|TestOfficePreview|TestResolveAttachment|Test.*Avatar|TestTopic|TestCollaboration)' -count=1 -json
go vet ./internal/realtime ./internal/middleware ./internal/handler ./cmd/server
```

复审修复后的五包复核和补充用例：

```bash
bash ../scripts/go-test-with-agent-cli-guard.sh go test -p 1 ./internal/realtime ./internal/auth ./internal/middleware ./cmd/server ./internal/handler -skip '^TestWorkspaceDeletionManifestCoversPublicSchema$' -count=1 -json
bash ../scripts/go-test-with-agent-cli-guard.sh go test -race -p 1 ./internal/realtime ./internal/auth ./internal/middleware ./cmd/server ./internal/handler -run '^(TestMembership|TestRealtime|TestRegisterListeners|TestCloudFront|TestExpireCloudFront|TestAuth_ExpiresCloudFront|TestAttachmentAccess_Historical|TestRefreshSession)' -count=1 -json
bash ../scripts/go-test-with-agent-cli-guard.sh go test -race -p 1 ./internal/handler -run '^(TestLoginRetiresBroadCDNCookies|TestRefreshSession_RetiresBroadCDNCookies|Test.*Avatar)' -count=1 -json
go vet ./internal/realtime ./internal/auth ./internal/middleware ./cmd/server ./internal/handler
```

Google 登录在测试中使用本地 HTTP transport 模拟提供方响应，没有访问真实 OAuth 账号。测试数量含父测试和子测试，不能相加视为独立业务场景数。中间的 fixture／编译配置失败不计作产品缺陷证据；最终结果以本节明确列出的成功日志为准。

任务消息回归包括带 `topic_id` 的合成 `task:message` 帧；当前生产 `ReportTaskMessages` 只在任务绑定普通任务或旧会话时广播该类型，不能把该合成测试称为真实群组任务流式消息端到端验收。任务派发和其他话题任务生命周期使用实际生产事件路由与真实 WebSocket，未调用真实智能体或模型。

## 清理测试可重复性异常

`TestCommentSourceContextLifecycle` 曾在一次整包重跑及随后五次单独运行中失败，断言为 `cleanup target delete intents = 1`，预期至少 3。原主线同库对照三次通过，随后当前代码同库三次也通过，最终五包整包内同样通过；清理实现和该测试与原主线文件摘要一致。这些证据不能证明是本轮缺陷，也不能证明是已确认的基线缺陷。最终整包保留该测试，没有降低断言、修改清理业务逻辑或扩大排除列表；可重复性风险仍未关闭，后续须调查实际清理队列状态及并发／重试影响，不能把推测写成已查明原因。

## 已有基线失败

`TestWorkspaceDeletionManifestCoversPublicSchema` 检查失败，原始基线快照在同一隔离库复测也得到相同结果，见 `baseline-manifest.log`。这不是本轮新增迁移或查询引入的差异，但完整受影响 Go 套件不能标记为全绿。

清单未分类的现有表为 `agent_email_approval`、`agent_email_policy_state`、`agent_email_policy_version`、`agent_email_quota_reservation`、`email_delivery_recipient`、`email_message` 和 `role_source_runtime_attestation_observation`；另外十张插件表被清单要求但在本次条件迁移环境中不存在。需要另行核对工作区删除实现与条件迁移规则，不能仅修改测试清单让其通过。本轮未修改该子系统。

## 合并和上线前仍需确认

1. 独立架构师已给出限定范围进入受控平台验收的结论，R1／R3 的应用修复已复核；这不是全平台或生产放行。清理测试可重复性风险及下述发布门槛仍未关闭；产品、测试负责人和上线责任人的批准由各自承担。
2. 公开 bucket、已有 CDN 签名链接及 CloudFront 签名 cookie 不会被这份代码自动撤销。应用已停止新的整域 cookie 和受保护文件直链签发；现有对象存储链接默认有效期为 30 分钟，部署可覆盖，旧 cookie 有效期须按实际策略盘点。上线前须验证存储私有、CDN 不再接受能绕过应用授权的旧策略，并处理历史凭证／缓存窗口。清理浏览器 cookie 不等于撤销已复制的凭证。
3. 旧格式附件能力链接主动失效。新旧后端不能无计划地混部；客户端应重新获取元数据。旧正文中直接写入的存储 URL 不能获得新的 API 权限保证：本地受保护原始路径会拒绝，需用对应稳定附件链接恢复展示；外部/CDN 原始地址按来源系统处置。本轮未批量改写历史正文。
4. 共享前端组件已验证；未做真实浏览器端到端操作、Windows/macOS 原生下载验收、手机端检查或真实 S3/CDN 部署验收。Office 服务在本轮权限测试中使用模拟转换器，不是新增 Word/Excel/PPT 转换质量验收。无需运行真实模型来证明这些访问控制，但不能把这些测试称为 HR 业务验收。
5. 已交付到客户端的字节、缓存和正在传输的内容不能收回。JWT/PAT/Cloud Node 凭证本身撤销与工作区撤权不是同一机制；本轮能力链接绑定用户成员资格，并额外绑定智能体任务令牌，不承诺全账户会话注销后所有能力链接即时失效。
6. 移动端兼容 R2 是已确认的发布阻断，不只是缺少测试。用户明确将移动端实现移出本轮范围，未授权忽略该回归发布。当前原生消费者和实时事件链均需适配，不能仅修列表或恢复存储直链；待完成客户端升级／版本协调和移动端单独回归后再评估发布。
7. 用户已授权将本轮代码提交、合并至 `main` 并推送到自有仓库 `dunzic/multica-worker`；实现已提交为 `e869e6197`。本次 Git 交付沿用相同代码快照的既有测试证据，不声称重新执行完整测试。未创建发布标签、部署或打包；后续服务及安装包发布仍须满足上述门槛并另获授权。

执行流程见[权限执行计划](permission-revocation-execution-plan.md)，来源和后续吸收规则见[社区同步记录](../../COMMUNITY_SYNC.md)。
