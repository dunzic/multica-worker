# Feature 四方评审策略

每个 feature 及其可独立上线的 slice 都必须经过架构、产品、测试和 CEO 四个视角的评审。评审不是一次性会议，而是四个有证据要求的 Gate：策略、设计、证据和上线。

## 角色责任

| 角色 | 必须回答的问题 | 不得被平均掉的否决项 |
| --- | --- | --- |
| 架构专家 | 信任边界、数据所有权、并发线性化、租户隔离、扩容、降级和恢复是否成立 | 越权、数据丢失、跨租户、无法回滚、单点绕过 |
| 产品专家 | 用户问题、角色、状态机、默认值、错误恢复和验收标准是否明确 | 权限含糊、危险默认值、状态误导、不可完成的关键流程 |
| 测试专家 | 设计能否被自动验证，竞态、故障、滥用和兼容性是否有证据 | 无法测试的安全主张、重复副作用、额度超发、故障后状态不明 |
| CEO | 客户价值、范围、成本、合规和组织风险是否值得承担 | 无清晰价值、无限支持成本、无 owner、无生产退出机制 |

## Gate

### 1. 策略 Gate

在实现前确定客户问题、P0/P1 边界、风险预算、成功指标和明确不做的事项。输出 `GO_FOR_DESIGN`、`CONDITIONAL` 或 `NO_GO`。

### 2. 设计 Gate

在合并主体实现前审查 API、数据模型、权限矩阵、状态机、迁移、可观测性、兼容性和回滚。任何服务端安全边界都必须有代码和持久化约束，不能依赖 Prompt、UI 或 CLI 自律。

### 3. 证据 Gate

合并前提交自动化测试、故障注入、迁移往返、性能结果和安全检查。"代码看起来正确"不算证据。

### 4. 上线 Gate

在每个生产 cohort 前确认容量、告警、值班手册、kill switch、灰度范围和回滚结果。单元测试通过不等于允许上线。

## 评分

每个视角按 0-3 评分：

- `0`：方向错误或关键证据缺失；
- `1`：可继续设计，但存在生产阻断项；
- `2`：可在默认关闭的 feature flag 后合并；
- `3`：具备当前 cohort 的上线证据。

任一视角为 `0` 时不得合并，任一视角低于 `3` 时不得生产上线。安全、隐私、租户隔离、数据丢失、幂等和回滚阻断项不能用平均分抵消。

## 必需评审记录

每轮评审必须记录：

```text
Feature / slice:
Gate: strategy | design | evidence | rollout
Date:
Accountable decision maker:

Customer problem and measurable outcome:
Scope and explicit non-goals:
Architecture score, evidence and objections:
Product score, evidence and objections:
Test score, evidence and objections:
CEO score, evidence and objections:
Security/privacy/data-loss blockers:
Scale evidence:
Rollout and rollback decision:
Actions, owner and due date:
Final decision: NO_GO | CONDITIONAL | GO_FOR_DESIGN | GO_FOR_MERGE | GO_FOR_ROLLOUT
```

"无人反对"不等于批准。每个结论必须有可追踪的责任人、证据或未决事项。
