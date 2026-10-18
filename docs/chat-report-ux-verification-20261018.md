# 聊天列表与报告协作：开发、评审和验收执行记录

本记录对应[开发前需求与验收计划](chat-report-ux-acceptance-plan-20261018.md)。范围是通用聊天与报告协作，不是 HR 专用功能。

## 结论与交付边界

- 本地实现完成；最终差异自审及发现问题的修复、定向复测完成。本轮未启动另一个独立架构师 Agent，不能称为独立评审。
- 前端完整测试、类型检查、lint、knip、Go race 回归通过；真实 Chrome 7 条综合流程通过。
- macOS 原生客户端执行了统一列表、报告预览、系统下载、反馈保存/重开和历史查看。Windows 没有可用真机，阻断其平台验收。
- **不是完整发布验收**：下表列明部分计划子场景尚未实测，Office 转换服务、历史版本升级及跨工作区延迟导航的完整真机证据也未补齐。不能将组件测试替代这些缺口。
- 未 commit、合并、push、打包或部署；未运行真实 Agent、重跑简历或修改生产数据。
- 后续扩展屏补测已完成桌面开发热更新导航缺陷的修复、自审和 macOS 定向真机回归，详见[桌面标题/正文同步补测记录](desktop-navigation-sync-acceptance-20261018.md)。下方原测试数字和证据保留为前一轮记录；最新前端为 9,561 项（含缓存复用），桌面 720 项，完整 lint 实际为 27 个既有 warning，不能把旧统计当作新增轮次。

## 实现范围

1. 单聊和群聊在一个列表中显示，保留既有单聊置顶/归档逻辑，群聊显示独立标识、名称、真实摘要和活动时间。不新增或猜测群未读数。
2. 群组旧深链接继续可用；一个默认空话题不再显示成第二个“未命名”群，多话题使用明确的第二层导航。消息、话题、草稿和附件的所有权没有合并。
3. HTML 的站内附件链接交给平台执行认证预览或下载。继续使用 `sandbox="allow-scripts"`，没有增加同源、弹窗、顶层导航权限。报告脚本不接收令牌或原文件内容。
4. “报告反馈”显示文字和展开状态；已有字段的报告默认展开。记录名称旁可编辑、预览、下载，避免在宽表最右侧寻找操作。宽屏并排、窄屏上下排列。
5. 保留原有最多 20 个字段、6 种类型、分页、版本冲突和审计机制。增加保存成功反馈和临时读取失败后的重试。没有迁移或重建既有反馈数据。
6. 修复草稿切换、防抖卸载、短时刷新失败及撤权后缓存展示的问题。群摘要查询只读取有权会话的有界投影，API 新字段可选并有 malformed-response 测试。

## 代码与环境

| 项目 | 实际值 |
| --- | --- |
| 日期/分支 | 2026-10-18；`codex/chat-100-attachments-20261018` |
| 代码基线 | `1a3704d480f73f50f0dc66e6c6c9b5144616bff4` 加本轮工作区未提交差异及新增文件 |
| 环境 | `chat-report-ux-20261018`；网页 13992 / API 19072 / Electron renderer 6166 |
| 数据库 | 专用 `multica_chat_report_ux_20261018_992`；原数据库及账号未修改 |
| 主机 | macOS 27.0，Build 26A428 |
| 浏览器 | 本机 Chrome 154.0.8037.98，有界面运行，实际网页/API/数据库 |
| 原生客户端 | Electron 39.8.7，`Multica Canary chat-report-ux-20261018`，本次源码开发构建；不是签名安装包 |
| 数据/身份 | TestApiClient 创建的合成工作区、owner/collaborator/nonmember；原生样例为 `desktop-review-1792288418999`，17 条交付记录 |
| 证据根目录 | 仓库下 `.context/chat-report-ux-20261018/evidence/`，仅本地、忽略于 Git |

Playwright 用例结束后清理本次合成工作区；原生样例保留以便本地复查。测试代码使用非生产开发登录，不共享令牌、连接密码或真实简历。粘贴/拖放批量测试使用浏览器 DataTransfer 事件，不构成系统剪贴板验收。响应式 390×844 不是手机真机。

## 评审问题及闭环

最终检查包括新增文件、共享包边界、网页/桌面路由、API 兼容和 SQL；按 `web-design-guidelines` 检查可发现入口、键盘语义、选中态、溢出和错误恢复。参考规范为 [Web Interface Guidelines](https://github.com/vercel-labs/web-interface-guidelines/blob/main/command.md)。

| 编号 / 级别 | 发现与影响 | 修复位置 | 定向复测与结论 |
| --- | --- | --- | --- |
| RV01 / P1 | 只验证公共 postMessage 和父窗口激活，打开报告后的残留激活可被脚本用于自动下载 | `packages/views/editor/utils/iframe-attachment-bridge.ts`；`hooks/use-html-preview-location.ts:70` | 先注入桥接并持有私有 MessageChannel；只接受真实点击、当前 iframe/scope、允许来源和严格附件路径。真实浏览器的自动点击、公共伪造、假 port、旧 scope、外部链接均未触发目标请求；通过 |
| RV02 / P1 | 组件卸载/快速切换话题时，防抖未落盘或两个空 prop 值导致旧文字留在新话题 | `packages/views/chat/components/chat-input.tsx:252`、`:358`；`packages/core/chat/store.ts:496` | 卸载前 flush、按旧工作区写回、切换时显式 adopt，保留新单聊延迟创建语义。快速话题切换、上传中切群、单群草稿/附件隔离通过 |
| RV03 / P1 | 反馈后台读取临时失败时自动关闭面板，可能卸载尚未保存的编辑草稿 | `packages/views/editor/attachment-preview-modal.tsx:770` | 临时错误保持授权缓存和编辑上下文，仅权限拒绝清除；断网、恢复、另一行更新时草稿保持，通过 |
| RV04 / P1 | 被移出群后反馈已隐藏，但父预览仍展示缓存 HTML 正文 | `packages/views/editor/attachment-preview-modal.tsx:1122` | 401/403/404 清除正文和反馈；真机轮询撤权后 iframe/editor/row 消失，保存/历史/下载/定位均 404，其他有权报告仍 200；通过 |
| RV05 / P2 | 临时刷新失败只有说明，没有直接重试入口 | `packages/views/reports/report-feedback-panel.tsx:285` | 显式 Retry，保留旧值与草稿；真实请求中断、恢复和成功回读通过 |
| RV06 / P2 | 摘要可能显示 Markdown 附件原始 URL，影响列表可读性 | `packages/views/chat/components/chat-thread-list.tsx:61` | 展示可读文件/链接标题；列表组件和原生截图核查通过 |

以上“通过”仅指列出的已修复问题和对应验证层。静态评审未发现新的已确认安全/数据丢失阻断问题，不代表下列未执行平台和场景已经验证。

## 自动化结果

| 检查 | 最终结果 | 本地日志 |
| --- | --- | --- |
| `pnpm test --concurrency=1 -- --maxWorkers=2` | 768 文件、9,555 测试通过；core 2,333 / views 6,160 / web 286 / desktop 714 / ui-lab 46 / docs 16；6 个 workspace 任务成功，其中 2 个缓存复用 | `frontend-final-serial.log` |
| `pnpm typecheck` | 10 个任务成功 | `typecheck-complete.log` |
| `pnpm lint` | 7 个任务成功，0 error；21 个既有 warning，未为消除警告扩大改动 | `lint-complete.log` |
| `pnpm knip` | 通过，无发现 | `knip-complete.log` |
| `make test ENV_FILE=.env.worktree` | Go race 全套命令退出 0，含数据库权限/反馈/聊天集成；环境条件 skip 不视为通过 | `backend-race-canonical-tmp.log` |
| 最终定向组件回归 | 116 项（草稿/输入/列表/报告/桥接）及 95 项（预览/恢复/撤权）通过；随后完整前端通过 | `review-fixes-targeted-final.log`、`preview-recovery-final.log` |
| `playwright test`，见下方复跑命令 | 7 条实际 Chrome 综合测试通过，2 分钟 | `browser-acceptance-final.log` |
| SQL / 文档 | sqlc 生成文件随 SQL 更新；`git diff --check` 通过 | 工作区差异及本记录 |

本地工具链位于 `.context/chat-report-ux-20261018/`，不作为产品改动提交。Go 测试需使用 `/private/tmp` 下独立临时目录及隔离 Git 配置；最初 macOS 临时路径别名与 Git 父目录发现造成的测试环境失败保留在早期日志中，最终没有为其放宽产品安全检查。

最初浏览器列表恢复测试错误地等待通用 `role=alert`，匹配了非目标告警、提前恢复网络；已改为等待实际 Retry 控件后再恢复，最终通过。早期失败截图/trace 留作诊断，不列为通过证据。

## 真实浏览器证据索引

以下路径均相对证据根目录；测试的步骤、逐步断言和数据清理可直接读对应 `e2e/*.spec.ts`。文件名索引只标识证据，不单独证明保存或权限；保存、身份、版本和拒绝状态均有测试断言。

| 索引 | 脚本与截图目录 | 关键证据 |
| --- | --- | --- |
| B-CHAT | `e2e/chat-unified.spec.ts`；`browser-acceptance-final/chat-unified-one-chat-list-51876-links-topics-and-navigation-chromium/` | `mixed-list-group-draft.png`、`pending-upload-returned-to-source-draft.png`、`mixed-list-topic-navigation.png`、`mixed-list-narrow.png`、`group-narrow-keyboard.png`、`group-create-failed-input-preserved.png`、`new-group-in-unified-list.png`、`group-list-failed.png` |
| B-REPORT | `e2e/report-feedback.spec.ts`；`browser-acceptance-final/report-feedback-generic-re-e738c-e-conflict-audit-and-revoke-chromium/` | `report-entry-visible.png`、`report-original-preview.png`、`report-pdf-original-preview.png`、`report-conflict-preserved-draft.png`、`report-with-feedback.png`、`report-with-audited-feedback.png`、`report-feedback-narrow.png`、`report-access-revoked.png` |
| B-SAFE | `e2e/report-action-security.spec.ts`；`browser-acceptance-final/report-action-security-rep-99daf-s-and-recover-from-failures-chromium/` | `restricted-original-denied.png`、`save-failure-preserved-input.png`、`save-retry-reopened.png`、`feedback-refresh-retry.png`、`feedback-refresh-preserved-draft.png`、`group-report-access-revoked.png` |
| B-LIMIT | `e2e/report-feedback-boundaries.spec.ts`；`browser-acceptance-final/report-feedback-boundaries-a875f-es-and-preserved-identities-chromium/` | `twenty-fields-edit.png`、`fifty-one-records-saved.png`、`narrow-fields-preserved-draft.png` |
| B-100 | `e2e/chat-batch-attachments.spec.ts`；`browser-acceptance-final/chat-batch-attachments-*` 三个目录 | 每条路径均有 `batch-draft.png`、`batch-sent.png`；校验并发不超过 3、101 个拒绝、100 个进入同一条消息、刷新后仍为 100 个 |

## 原生客户端操作记录

测试人 `Desktop reviewer` 使用本地合成数据，未调用 Agent。实际点击报告内链接、反馈行编辑、保存、历史、单聊/群聊切换；不是把网页装进截图冒充原生操作。

- 报告打开：反馈区自动展示，`Edit record` 可见。选择 `Follow up`，填写 `Reviewed in the native Mac client follow-up is shared and saved.` 并保存。
- 重开/读回：`Deliverable 17` 保留同一条记录的值；其他条目的空值仍为空。历史展开后显示修改人、时间、前后值。
- 原件：正文 Preview 在原生新标签打开 TXT，Download 调起 macOS 系统保存窗口。落盘 `交付证据 original-final.txt` 为 50 字节，SHA-256 `4079553951ca2d05371e9f01bc1afdc633de7d38a1d2fc74789524dab77a0215`，内容与测试原件一致。
- 截图：`native-feedback-edit-filled.jpg`、`native-feedback-saved.jpg`、`native-feedback-history-details.jpg`、`native-final-original-preview.jpg`、`native-final-feedback-readback.jpg`、`native-final-mixed-list.jpg`。
- 前一轮长时间开发热更新后曾观察到标题更新而正文停在旧页，当时仅重启隔离桌面实例重跑，未将重启算作修复。后续已定位协调器模块替换后的初始化、订阅释放和 router 宿主身份问题，新增 6 项回归并在扩展屏真实客户端反复热更新验证，已关闭该开发客户端缺陷；另区分了 macOS 后台 Space 旧截图的误判。详见[根因与复测证据](desktop-navigation-sync-acceptance-20261018.md)。签名安装包升级仍未验证。
- 干净启动后第二话题的 `Native second-topic draft stays separate`、第一话题的 `Native group draft stays with this group`、单聊的 `Native direct draft remains separate` 分别回读正确。截图：`native-final-topic-draft.jpg`、`native-final-restarted-readback.jpg`、`native-final-restarted-original.jpg`。
- 额外回归单聊 Pin → Archive → Archived → Unarchive → Unpin：恢复到混合列表，原草稿仍在，测试会话最后恢复为未置顶且未归档。截图：`native-final-archived-draft.jpg`、`native-final-restored-direct.jpg`。没有执行删除会话或发送给离线 Agent。
- 已人工检查关键结果截图，包括 PDF 实际渲染、撤权内容清除、混合列表/多话题、错误重试、双人冲突/修改历史、窄屏和原生读回。表格在较窄侧栏仍可水平滚动，首列编辑/原件操作保持可见；截图索引不表示全部平台状态已验收。

## 逐场景执行状态

未完成子项拆开列明；“通过”不覆盖同一行注明的未执行范围。测试预期见原计划，以下只记录本轮观察到的实际结果。

| 场景 | 状态 / 实际结果 | 证据与尚未完成项 |
| --- | --- | --- |
| C01 | 通过：单聊与两个群显示在同一列表，可识别群图标，旧分离入口移除 | B-CHAT；原生混合列表 |
| C02 | 逻辑层通过；完整交替收消息真机场景未执行 | helper 排序/置顶测试、真实群发送后摘要更新；未做真实 Agent 单聊收消息 |
| C03 | 建群成功与失败重试通过；全新单聊真实发送未执行 | B-CHAT 保留群名/成员、重试仅一群；禁止真实 Agent 运行 |
| C04 | 通过：群/单聊、群 A/B、多话题的文字与附件草稿保留；上传中切走，回原会话读到附件 | B-CHAT；draft-isolation 组件回归；原生草稿 |
| C05 | 通过：旧 `/chat/groups` 带会话/话题链接进入统一列表，刷新、后退、前进正确 | B-CHAT；原生关键返回流程 |
| C06 | 单默认话题、多话题显示及草稿导航通过；向第二话题发送新消息未执行 | B-CHAT；发送目标由组件回归覆盖 |
| C07 | 加载/空群/接口失败重试通过；实际删除/离群后的完整聊天页恢复未执行 | B-CHAT；报告撤权不替代聊天删除验收 |
| C08 | 逻辑/组件层通过；跨工作区延迟返回的真机操作未执行 | 显式 workspace header、查询 key、卸载回写作用域、导航组件测试 |
| C09 | 键盘进入会话、窄屏返回及无页面水平溢出通过；极长多语言名称完整键盘巡检未执行 | B-CHAT；可见选择态和截断截图 |
| C10 | 自动化及原生置顶/归档/恢复通过；真实运行取消、失败重试的真机回归未执行 | 前端完整测试；原生 archived/restored 截图；未运行真实 Agent |
| R01 | 通过：任务评论入口及群消息入口打开报告，文字反馈入口可见 | B-REPORT 评论；原生群消息报告 |
| R02 | TXT/PDF 正文原件预览通过，无下载事件；Office 原件转换真机未执行 | B-REPORT；PDF 实际页数与画面；原生 TXT |
| R03 | 通过：正文及反馈区下载得到正确名称；正文下载内容校验，原生落盘摘要一致 | B-REPORT、原生系统保存与哈希 |
| R04 | 通过：相对链接、中文文件名、嵌套 span、键盘 Enter；绝对 URL 由 helper/原生覆盖 | B-REPORT；iframe bridge helper 矩阵 |
| R05 | 无权原件和临时离线恢复通过；删除原件、独立登录过期真机未执行 | B-SAFE 404、空预览窗关闭、恢复下载；接口权限自动化 |
| R06 | 无配置不自动展开、有配置自动展开、主动收起通过；切换两份有配置报告的真机状态隔离未执行 | B-REPORT、B-SAFE；modal 组件 |
| R07 | 通过：文本/单选/日期等可操作，成功回读；保存失败保留输入并重试 | B-LIMIT、B-SAFE；原生编辑保存 |
| R08 | 通过：刷新/重开、独立协作者读取、服务端回读一致 | B-REPORT、B-SAFE；原生读回 |
| R09 | 通过：B 先保存，A 冲突且保留输入，放弃后读到 B 最新值 | B-REPORT；版本断言 |
| R10 | 通过：另一行更新和临时刷新失败均不丢正在输入的草稿；显式重试有效 | B-SAFE |
| R11 | 通过：真实修改人、时间、前后值；冲突没有成功写入 | B-REPORT；原生展开历史 |
| R12 | 关闭编辑时确认/Keep editing 通过；有编辑草稿时跨报告导航的完整流程未执行 | B-LIMIT；不将普通预览返回冒充带草稿跨报告验收 |
| R13 | 通过：质量检查与交付跟进使用相同字段/组件保存 | B-REPORT、B-LIMIT、原生交付报告；无 HR 专用逻辑 |
| R14 | 通过：20 字段、51 行、分页、6 类型、长内容编辑后保存；未改前 50 行 ID/值 | B-LIMIT；API readback |
| R15 | 本轮重开保持 ID/值与历史通过；旧安装版本升级前后比较未执行 | B-LIMIT、原生既有记录读回；未进行安装包升级 |
| R16 | 宽/窄屏、键盘下载、嵌套 Escape/Keep editing 通过；系统缩放完整矩阵未执行 | B-REPORT、B-LIMIT、B-CHAT |
| S01 | 通过：群撤权及工作区撤权隐藏内容，读取/保存/历史/原件拒绝 | B-SAFE、B-REPORT、Go 权限回归 |
| S02 | 更窄群权限的原件拒绝通过；有权跨工作区原件的完整原生导航未执行 | B-SAFE；既有 preview-location 单元/服务回归 |
| S03 | 通过：脚本自动点击、公共消息/假 port/旧 scope/外部 URL 不触发操作；三类沙箱访问均拒绝 | B-SAFE；bridge 单元测试 |
| S04 | 本表列明的浏览器关键流程通过；未执行子项仍保留 | 7 条最终真实 Chrome 用例，非模拟页面 |
| S05 | macOS 开发客户端关键流程通过；开发热更新标题/正文异常已定向修复并在扩展屏补验；完整升级/跨工作区/打包客户端未执行 | 原生截图与下载文件；最新证据见桌面同步补测记录，未以重启替代修复 |
| S06 | 阻断：没有实际 Windows 主机和本次构建 | 必须另接 Windows，不能使用 macOS/响应式网页结果代替 |

## 可复跑入口与后续门槛

使用已注册隔离环境和本轮工具路径，禁止读取生产连接覆盖本地配置。完整浏览器组：

```sh
pnpm exec playwright test e2e/chat-unified.spec.ts e2e/report-action-security.spec.ts e2e/report-feedback.spec.ts e2e/report-feedback-boundaries.spec.ts e2e/chat-batch-attachments.spec.ts --headed
```

后续必须分别处理：未执行子项、Windows 真机、Office 专用转换服务实测、历史安装包升级。Git 发布、原生打包、线上部署分别需要新的授权；不能因本地测试通过自动执行。
