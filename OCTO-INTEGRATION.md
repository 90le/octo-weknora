# Octo 集成接口与当前边界

更新：2026-09-14。配置后端和首个原生管理页面已实现；未部署，未切换 Bot 查询链路。进度以 [OCTO-STATUS](OCTO-STATUS.md) 为准。

## 数据与授权

- `octo_scopes`：工作区（原生 tenant）、Bot 账号、群 ID、可选子区 ID；ID 全部使用字符串。名称当前为管理员配置，`name_source=configured`，不冒充 Octo 实时名称。
- `octo_scope_bindings`：引用原生 KB ID，只表示查询选择，不保存写权限或资产所有权。绑定同工作区资产，跨工作区共享映射尚未开放。
- 子区显式继承同账号、同群的主区；未配置不回退全库。直接与继承命中同一 KB 时合并，以直接绑定优先；解除直接绑定后仍可能通过主区继承生效。
- 页面和接口仅供工作区管理员配置。复用原生角色和 KB 读写门禁，严格执行角色校验，不继承旧 RBAC 只记录不拦截模式。配置 API 未授权 API Key 调用。
- 变更与原生 `audit_logs` 同事务提交；日志失败回滚。解除绑定不会删除 KB。平台身份同步、群主管理入口与公共 Bot 检索接口仍待实现。

## API（原生登录会话；统一前缀 `/api/v1/octo`）

| 方法与路径 | 用途 |
|---|---|
| GET `/scopes?offset=0` | 分页读取当前工作区区域，每页最多 100 条 |
| POST `/scopes` | 登记区域；子区要求同账号同群主区已存在 |
| PUT `/scopes/:scope_id` | 设置显示名称与继承开关，不改变身份 |
| GET `/scopes/:scope_id/effective-bindings` | 管理配置预览，不是用户查询授权接口 |
| PUT `/scopes/:scope_id/knowledge-bases/:id` | 幂等绑定；执行原生 KB 写权限与同工作区检查 |
| DELETE `/scopes/:scope_id/knowledge-bases/:id` | 解除直接绑定，不修改资产 |
| GET `/knowledge-bases/:id/scopes` | 反查直接绑定的区域；不含子区继承展开 |

创建字段：`account_id`、`group_id`、`subarea_id`（主区为空）、`display_name`、`inherit_parent`。租户、内部 ID 和名称可信标记由服务端决定。更新字段：`display_name`、`inherit_parent`（必须显式传入）。

## 页面入口

- 设置 → Octo 群与子区：区域列表、登记、设置继承、查看最终生效库、绑定和解绑。
- 文档知识库标题栏 → Octo 使用范围：反查直接绑定区域，跳转群与子区管理。FAQ 专用页面尚未加入该入口。
- 原生文档管理、解析和 KB 身份继续复用；不恢复旧 React 管理后台。

## 数据库与验证

PostgreSQL 迁移 `000096_octo_scopes`；SQLite 迁移 `000017_octo_scopes`，包含 down 文件。未来跟随上游升级时需检查迁移编号冲突，不能覆盖已执行迁移。

专项测试覆盖工作区／Bot 隔离、子区继承、重复绑定、已删除库过滤、解绑保留资产、超大字符串 ID、重复区域、事务审计回滚、RBAC 严格模式及 API Key 默认拒绝。PostgreSQL DDL 已在隔离副本事务内执行并回滚；未修改在线业务库。

## 下一步

先完成可信 Octo 连接和群名称／角色同步，再接群内受限查询；不要给 OpenClaw 管理员登录凭据绕过身份层。继续复用数据源框架实现 Git／本地目录登记与只读检索，之后迁入有效旧配置。预览配置不得被称为已经驱动群问答。
