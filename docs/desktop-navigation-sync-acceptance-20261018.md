# 桌面端标题与正文同步：补测与修复计划

本轮承接[聊天与报告验收计划](chat-report-ux-acceptance-plan-20261018.md)及[执行记录](chat-report-ux-verification-20261018.md)。仅修复导航同步缺陷并补测，不提交、推送、打包或部署，不运行真实智能体。

## 需求与边界

- 用户目标：在统一聊天列表、群话题、单聊、报告、原件标签间切换时，当前标题、选中标签、工作区和正文必须一致，返回后仍能看到原有草稿及已保存反馈。
- 已知现象：旧开发会话曾出现标题更新、正文停留在报告；重新启动后恢复。根因尚待受控复现，不能把重启当修复。
- 排查假设：开发热更新可能重建路由模块，却没有重新绑定会话订阅。须同时检查正常运行、组件重挂载与重复初始化，避免只修复表面标题。
- 影响范围：macOS 原生 Electron 客户端优先；共享聊天与报告入口做浏览器回归。Windows 未提供主机，独立标记为未验证。
- 保持边界：单一会话状态源、现有导航适配器、工作区/群/附件权限、草稿归属、反馈 ID/值/审计历史均不变。不新增权限旁路、重复状态或兼容层。

## 显示器与环境前置条件

- 每次真机或有界面自动化之前检测显示器；窗口、新预览窗及系统对话框必须在扩展屏并核实边界。不能截图或定位时停止该层并说明。
- 本次检测：内屏 3024×1964，逻辑 1512×982；扩展屏 LS32D80xE 3840×2160，逻辑 1920×1080，比例 2，镜像关闭。扩展屏位于内屏右侧。
- 桌面测试窗口初始边界（Quartz 坐标）：x=1912、y=107、宽=1280、高=800，完全位于扩展屏 x=1512..3432、y=0..1080。窗口菜单也显示仅可“移到内置显示器”。未改变显示布局、主屏或缩放。
- 环境：`chat-report-ux-20261018`，API 19072、Web 13992、Electron 6166；专用合成数据库与既有原生测试账号。复用测试记录，不修改真实简历或生产反馈。

## 验收场景

各项记录步骤、预期/实际、平台、差异版本与截图/日志路径。自动化与真机分别记录，不相互替代。

| ID | 前置/操作 | 可观察预期 | 层级与证据 |
| --- | --- | --- | --- |
| D01 | 检测显示器，查看目标窗口和新窗口/保存对话框边界 | 仅本次测试窗口位于扩展屏，截图可查看 | 系统只读检测、原生菜单及截图 |
| D02 | 报告打开时进行受控开发热更新，再点击聊天/群/单聊 | 标题、选中态、正文一致；热更新前后无需重启，草稿不串位 | 失败回归测试、真实原生复现前/后截图、开发日志 |
| D03 | 重复初始化、释放再初始化同步协调器，随后导航/后退/前进 | 始终只有一套有效订阅；当前路由跟随当前会话，无旧实例写入 | 同步协调器与宿主组件测试 |
| D04 | 群 → 话题 2 → 话题 1 → 报告 → 原件新标签 → 聊天 → 单聊，重复两轮 | 每个入口正文对应当前标题/标签；三个草稿分别保持；原件只预览不下载 | macOS 真机 AX、关键截图 |
| D05 | 标签切换、后退/前进、重新加载当前标签 | 目标正文正确，无旧报告滞留；历史/标签数量不因内部同步增长 | 单元/集成回归、原生操作截图 |
| D06 | 重新打开既有报告，检查已保存反馈、修改历史并下载原件 | 原有 ID/人工值/记录保留；反馈入口可发现；原件名称/字节正确，系统窗口在扩展屏 | 原生截图与安全服务回读、下载文件校验 |
| D07 | 群/单聊混合列表、报告反馈及安全桥接回归 | 无共享 UI 回退，不放宽沙箱/权限，错误恢复和草稿仍有效 | 相关前端套件、类型/lint 检查；需要时浏览器真机 |
| D08 | 最终差异自审与风险复核 | 新文件、生命周期清理、重复订阅、跨工作区隔离和测试缺口均有结论 | 带位置的评审记录；Windows/发布构建未测则阻断对应结论 |

完成门槛：根因有可复现证据，修复前失败/修复后通过，macOS 对应真实用户路径和截图成立；剩余平台与历史计划缺口如实保留。不以更改标题掩盖正文不同步，不以刷新/重启作为用户长期解决方案。

## 执行结果与证据

本轮 macOS 开发客户端的目标缺陷已修复并完成定向真机回归。该结论不覆盖 Windows、签名安装包升级或原计划尚未执行的业务子场景；没有提交、推送、打包或部署。

### 代码版本、身份与显示器

- 基线：`1a3704d480f73f50f0dc66e6c6c9b5144616bff4`；分支 `codex/chat-100-attachments-20261018`，包含此前聊天/报告未提交差异。本轮新增产品改动仅为 `tab-content.tsx` 和 `tab-coordinator.ts`，新增宿主回归测试；没有改变服务端、权限、沙箱或数据模型。
- 客户端：macOS 27.0（26A428）、Electron 39.8.7 本地源码开发构建；浏览器 Chrome 154.0.8037.98。网页使用 `http://localhost:13992`，API 19072，renderer 6166。
- 身份与数据：既有 `Desktop reviewer` 合成账号、`desktop-review-1792288418999` 工作区、`Delivery review team` 群、两个话题、离线测试 Agent 的既有单聊，以及 17 条交付记录。不发送消息、不调用 Agent、不改生产人工反馈。
- 扩展屏复核：LS32D80xE，3840×2160，逻辑 1920×1080，2×；非镜像。原生主窗口最终为 `{x:1512,y:0,width:1920,height:1080}`；保存窗口 `{x:2032,y:316,width:880,height:448}`；专用 Chrome 无痕测试窗口 `{x:2141,y:78,width:1291,height:864}`。均位于扩展屏矩形内，新原件浏览器标签仍在该窗口。没有移动用户原有浏览器窗口，也没有改变显示设置。
- 本地证据目录：`.context/chat-report-ux-20261018/evidence/desktop-navigation-sync/`。以下日志和截图均相对该目录，忽略于 Git，不包含认证令牌、数据库凭据或真实简历。

### 根因与代码评审闭环

开发热更新可能保留 React 的 hook 状态，却替换同步协调器模块。旧实现只在 `useState` 初始化器里绑定协调器；替换后新路由未绑定当前会话。即便拿到新 router，`RouterProvider` 也不会仅因 prop 替换就重新初始化内部呈现状态。旧模块还缺少订阅释放。这些因素会使标题/会话状态前进而正文滞后。

| 问题 | 修复位置 | 验证 |
| --- | --- | --- |
| 宿主保留 hook 状态后不再初始化新协调器 | `apps/desktop/src/renderer/src/components/tab-content.tsx:39` | 每次渲染前检查幂等初始化；修复前真实 router/宿主测试失败，修复后通过；重复热更新后原生导航正常 |
| 新 router 仍沿用旧 `RouterProvider` 状态；或过度重挂载丢输入 | `apps/desktop/src/renderer/src/components/tab-content.tsx:44`、`:59` | router 身份变化才更换宿主 key；普通标题/URL 更新不强制重挂载；未保存编辑态回归通过 |
| 旧 router/store 监听残留，重新绑定后 QueryClient 引用缺失 | `apps/desktop/src/renderer/src/platform/tab-coordinator.ts:346`、`:364`；`tab-content.tsx:46` | dispose 解除两类监听并销毁路由；重新绑定失效当前页面查询一次；跨工作区、重复初始化、历史与标签切换回归通过 |

最终差异自审覆盖新增测试、订阅生命周期、平台边界、历史与草稿保持；依据 Web Interface Guidelines 检查导航一致性和用户上下文，不新增多余提示或业务专用控件。没有发现其他已确认的阻断问题。本轮没有另启独立架构师 Agent，属于自审而非独立评审。

### 自动化回归

| 检查 | 实际结果 | 日志 |
| --- | --- | --- |
| 修复前宿主回归 | 5 项中 2 失败：新 router 未定位到报告、旧 router 未 dispose；失败证据先于修复 | `regression-before.log` |
| 宿主与协调器定向回归 | 10 项通过，其中宿主新增 6 项；使用真实 router/store/coordinator，仅替换业务页面；最终无探针源码再次通过 | `regression-after.log`、`regression-targeted-clean.log` |
| 最终桌面全套 | 63 个文件、720 项通过，退出 0；清除临时诊断代码后再次执行 | `regression-final.log` |
| 根级前端 `pnpm test --concurrency=1 -- --maxWorkers=2` | 769 文件、9,561 项通过；core 2,333 / views 6,160 / web 286 / desktop 720 / ui-lab 46 / docs 16。6 个任务成功，其中 5 个复用缓存、桌面实际重跑 | `frontend-full.log` |
| `pnpm typecheck` | 10 个任务成功，其中 9 个缓存复用 | `typecheck.log` |
| `pnpm lint` | 7 个任务成功、0 error；全仓既有 27 个 warning（ui 3 / core 2 / views 21 / web 1），本次桌面无 warning | `lint.log` |
| `pnpm knip` / `git diff --check` | 通过，无 knip 发现，无空白错误 | `knip.log` / 最终差异检查 |

本轮未修改后端，没有重新运行 Go race；前一轮结果保留在原执行记录，不能称为本轮重跑。本轮浏览器为真实手动操作，不把此前 7 条 headed Playwright 的截图追认为符合新增扩展屏规则的证据。

### 真机场景结果

| ID | 实际操作与结果 | 本轮证据 |
| --- | --- | --- |
| D01 | 通过。检测内屏/非镜像扩展屏，核实原生、专用网页、新标签及系统保存窗口位置；最终另核实原生为前台可见窗口 | 只读显示器/窗口记录；`native-save-dialog-extended-display.jpg`；本节几何信息 |
| D02 | 通过。报告打开时替换协调器模块，点击 Chat 正文正确；两次受控热更新后再做两次单模块更新，移除测试标记后仍无需重启。测试标记/诊断日志代码均已移除 | `before-fix-title-chat-body-report.jpg`、`after-hmr1-chat-body-synchronized.jpg`、`after-hmr2-chat-body-synchronized.jpg`、`final-hmr-single-module-chat.jpg`、`final-hmr-clean-source-chat.jpg`；修复前/后测试日志 |
| D03 | 通过（自动化层）。幂等初始化、旧实例释放、重新绑定、跨工作区、三次替换后的导航与历史均正常 | `tab-content.test.tsx` 六项及 `regression-final.log` |
| D04 | 通过（macOS 开发客户端）。两轮群/话题/报告/原件/聊天/单聊操作；最终在确认前台后再次连续执行。群话题 1、话题 2、单聊各自草稿原文保持；原件预览显示 TXT 内容，不触发保存窗口 | `final-topic2-draft-foreground.jpg`、`group-topic1-draft.jpg`、`final-original-preview-foreground.jpg`、`final-direct-draft-foreground.jpg` |
| D05 | 通过已执行范围。原生后退/前进、标签切换正确；通过 View → Reload 明确刷新 renderer，原会话和草稿恢复。`reloadActiveTab` 的精确当前页查询失效及历史不增长由自动化覆盖，不混称为同一个刷新操作 | `native-menu-reload-readback.jpg`；宿主历史/重载测试 |
| D06 | 通过。报告重开后 Deliverable 17 的 Follow up 和既有备注保留；历史仍列原修改人/时间；下载调起扩展屏系统窗口，落盘 50 字节正确内容 | `report-feedback-after-hmr.jpg`、`feedback-history-after-hmr.jpg`、`native-save-dialog-extended-display.jpg`、`交付证据 original-navigation.txt` |
| D07 | 通过已执行范围。独立 Chrome 无痕窗口登录同一合成账号，混合列表 → 群话题 1 → 报告，读到原生已保存反馈；正文 Preview 打开第二标签的实际 TXT；关闭原件、浏览器后退回到原群/话题 | `web-unified-list-extended.jpg`、`web-report-feedback-extended.jpg`、`web-original-preview-extended.jpg`、`web-return-to-group-extended.jpg`；前端全套 |
| D08 | 最终自审完成，未发现新增安全/数据丢失阻断问题；Windows 与发布构建验证仍阻断 | 上述评审闭环、版本摘要及范围声明 |

原件 SHA-256：`4079553951ca2d05371e9f01bc1afdc633de7d38a1d2fc74789524dab77a0215`。与测试源内容一致；不是仅验证文件存在。截图已人工查看，关键预览、正文/标题、草稿、反馈读回与保存对话框可辨识。

### 补测中的环境误判记录

切换到网页后，一次后台原生窗口捕获呈现“标题 Chat、正文报告”，最初按失败暂停验收并保留 `second-round-title-chat-body-report-failure.jpg`。随后发现该全屏窗口不在当前可见 macOS Space：应用路由日志已经是 Chat，而 AX/截图仍是旧画面。通过系统应用切换回真正前台后，正文立即显示 Chat；只读系统查询确认前台为本次 Canary 且窗口在扩展屏。该旧画面不作为前台导航失败或通过的依据，最终在前台重跑了完整路径及单模块热更新，结果正常。`foreground-chat-after-space-switch.jpg` 保留复核结果。诊断面板关闭，临时探针移除。

后续执行屏幕规则时，除了边界，还应核实窗口确实在当前可见桌面/Space；仅调用 Raise 或取得后台截图不证明已经在前台操作。已把这条可复用约束补入 `AGENTS.md` 的显示器规则，未将事件细节写入共享规则。网页首次误用 `127.0.0.1` 时遇到 Next 开发源限制，改用环境配置的 `localhost` 后正常，没有放宽来源检查。

### 最终源码指纹与未验证范围

| 文件 | SHA-256 |
| --- | --- |
| `apps/desktop/src/renderer/src/components/tab-content.tsx` | `084ec6cc1bcf804fef2acaef23f6af82408f1c9e4f81ceeaf9208d5a56e36656` |
| `apps/desktop/src/renderer/src/platform/tab-coordinator.ts` | `83181bbe027ddc1baaaba6ced85607591f45e68bde195471ddd616997db8cafa` |
| `apps/desktop/src/renderer/src/components/tab-content.test.tsx` | `fb654ba8c8fda4d66da256a43de91b9ac7c147a4620d4877ea1ce47aa1e26df8` |

Windows 主机/本次构建、签名安装包升级、Office 转换服务完整实测、跨工作区延迟导航的完整真机路径，以及原计划列明的真实 Agent 执行等缺口仍未验证。没有用本轮 macOS 结果将其关闭。后续发布需按原计划补齐相应门槛，另行取得 Git/部署授权。
