# Feature: 支持私有化部署

## 定位

“支持私有化部署”让组织在自有网络和基础设施中运行 Multica，并让桌面客户端在登录前选择该组织的服务地址。身份认证和用户准入由部署方控制，不依赖 Google OAuth。

该 feature 吸收 `/Users/wells/Downloads/projects/aiwork` 中可复用的私有化部署实践，但不吸收 Agent 群聊、团队沟通、Conversations 平台和相关 feature flags。

## 能力边界

| 能力 | 实现 | 状态 |
| --- | --- | --- |
| Docker Compose 私有部署 | PostgreSQL、backend、frontend、持久化上传卷、健康检查 | 已有，纳入本 feature |
| Kubernetes 私有部署 | Helm chart、外部 PostgreSQL、Secret/ConfigMap | 已有，纳入本 feature |
| 私有化模式 | `MULTICA_PRIVATE_DEPLOYMENT=true`，通过 `/api/config` 向客户端声明 | 本次新增 |
| 非 Google 登录 | 私有化模式不下发 Google Client ID，服务端拒绝 `/auth/google`；桌面端移除 Google 入口 | 本次新增 |
| 邮箱验证码 | SMTP、Resend 或受控日志取码 | 已有，纳入本 feature |
| 桌面端目标地址 | 登录前配置 API 地址和可选 Web 地址，派生 WebSocket 地址，原子写入 `~/.multica/desktop.json` 后重启 | 本次新增 |
| 用户准入 | `ALLOW_SIGNUP`、`ALLOWED_EMAILS`、`ALLOWED_EMAIL_DOMAINS` | 已有，纳入本 feature |
| 邀请制空间 | `DISABLE_WORKSPACE_CREATION=true` 后仅允许用户通过邀请进入既有空间 | 已有，纳入本 feature |
| Agent 群聊/团队沟通 | Conversations、群聊 UI、群聊通知与相关迁移 | 明确不在本 feature 范围 |

## 核心实现逻辑

1. 部署入口默认设置 `MULTICA_PRIVATE_DEPLOYMENT=true`。
2. `/api/config` 返回 `private_deployment=true`，并在该模式下省略 `google_client_id`。
3. 即使环境中残留 Google 凭据，`/auth/google` 也会在任何外部请求前返回 403。
4. 桌面端在登录卡片提供私域目标配置。API 地址必须使用 HTTP/HTTPS；WebSocket 地址从 API 地址派生。分域部署可以单独配置 Web 地址。
5. 目标配置以 `0600` 权限原子写入 `~/.multica/desktop.json`，随后重启客户端，使 API、WebSocket、分享链接和 Desktop 专属 daemon profile 同步切换到同一环境。
6. 新用户在发送验证码和验证验证码两个入口都经过服务端准入检查；已有用户不受关闭注册影响。

## 推荐准入策略

### 企业域名准入

```dotenv
MULTICA_PRIVATE_DEPLOYMENT=true
ALLOW_SIGNUP=false
ALLOWED_EMAIL_DOMAINS=example.com
DISABLE_WORKSPACE_CREATION=true
```

### 精确名单准入

```dotenv
MULTICA_PRIVATE_DEPLOYMENT=true
ALLOW_SIGNUP=false
ALLOWED_EMAILS=admin@example.com,user@example.com
DISABLE_WORKSPACE_CREATION=true
```

首次部署时先临时设置 `DISABLE_WORKSPACE_CREATION=false`，由管理员创建共享空间；完成后改为 `true` 并重启 backend。

## 验收标准

- Compose 与 Helm 渲染结果均包含 `MULTICA_PRIVATE_DEPLOYMENT=true`。
- 私有化模式的 `/api/config` 不包含可用的 Google Client ID。
- 私有化模式调用 `/auth/google` 返回 403，且不会请求 Google。
- Desktop 登录页不显示 Google 登录按钮。
- Desktop 可保存同域或分域私有目标；非法 scheme 被拒绝。
- 保存后本地配置重新加载结果与输入目标一致，WebSocket 地址正确派生。
- 邮箱白名单、域名白名单、关闭注册和关闭空间创建的既有测试继续通过。
- 本 feature 的变更列表不包含 Conversations、Agent 群聊或团队沟通文件。

## 后续演进

- 保存前增加 `/healthz` 与 `/api/config` 连通性测试，并展示服务版本和证书错误。
- 支持多私域 profile 切换，而不是每次覆盖单一 `desktop.json`。
- 对接企业 OIDC/SAML/LDAP，仍复用统一准入策略和审计事件。
- 增加离线安装包、镜像签名、SBOM、备份恢复和升级回滚的一键化运行手册。
