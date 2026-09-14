# Octo 集成接口与当前边界

更新：2026-09-14。配置后端、原生管理页面与官方元数据接入已部署；尚未切换 Bot 查询链路。进度以 [OCTO-STATUS](OCTO-STATUS.md) 为准。

## 数据与授权

- `octo_scopes`：工作区（原生 tenant）、Bot 账号、群 ID、可选子区 ID；ID 全部使用字符串。新建时从官方接口核对名称，`name_source=octo`；旧人工记录仍标记为 configured。平台名称不能在本页改写，修改应在 Octo 完成后刷新。
- `octo_scope_bindings`：引用原生 KB ID，只表示查询选择，不保存写权限或资产所有权。绑定同工作区资产，跨工作区共享映射尚未开放。
- 子区显式继承同账号、同群的主区；未配置不回退全库。直接与继承命中同一 KB 时合并，以直接绑定优先；解除直接绑定后仍可能通过主区继承生效。
- 页面和接口仅供工作区管理员配置。复用原生角色和 KB 读写门禁，严格执行角色校验，不继承旧 RBAC 只记录不拦截模式。配置 API 未授权 API Key 调用。
- 变更与原生 `audit_logs` 同事务提交；日志失败回滚。解除绑定不会删除 KB。平台名称刷新和角色诊断已接入；群主管理入口与公共 Bot 检索接口仍待实现。

## API（原生登录会话；统一前缀 `/api/v1/octo`）

| 方法与路径 | 用途 |
|---|---|
| GET `/scopes?offset=0` | 分页读取当前工作区区域，每页最多 100 条 |
| POST `/scopes` | 使用已配置连接核验并创建区域；子区要求同账号同群主区已存在 |
| GET `/connections` | 返回连接标识和更新时间，不返回密钥 |
| PUT `/connections/:account_id/credentials` | 使用原生 SYSTEM_AES_KEY 加密存储 User Bot Token；缺少主密钥拒绝保存 |
| POST `/scopes/:scope_id/sync` | 服务器向官方接口刷新名称；失败保留上次名称和验证时间并记录错误状态 |
| GET `/scopes/:scope_id/members/:uid/role` | 管理员诊断原生群角色，不授予 KB 权限 |
| PUT `/scopes/:scope_id` | 设置显示名称与继承开关，不改变身份 |
| GET `/scopes/:scope_id/effective-bindings` | 管理配置预览，不是用户查询授权接口 |
| PUT `/scopes/:scope_id/knowledge-bases/:id` | 幂等绑定；执行原生 KB 写权限与同工作区检查 |
| DELETE `/scopes/:scope_id/knowledge-bases/:id` | 解除直接绑定，不修改资产 |
| GET `/knowledge-bases/:id/scopes` | 反查直接绑定的区域；不含子区继承展开 |

创建字段：`account_id`、`group_id`、`subarea_id`（主区为空）、`inherit_parent`。名称从 Octo 返回，客户端 display_name 不用于创建。租户、内部 ID 和名称可信标记由服务端决定。更新字段：`display_name`、`inherit_parent`（必须显式传入）。

## 页面入口

- 设置 → Octo 群与子区：区域列表、登记、设置继承、查看最终生效库、绑定和解绑。
- 文档知识库标题栏 → Octo 使用范围：反查直接绑定区域，跳转群与子区管理。FAQ 专用页面尚未加入该入口。
- 原生文档管理、解析和 KB 身份继续复用；不恢复旧 React 管理后台。

## 数据库与验证

PostgreSQL 迁移 `000096_octo_scopes`、`000097_octo_connections`；SQLite 迁移 `000017_octo_scopes`、`000018_octo_connections`，包含 down 文件。未来跟随上游升级时需检查迁移编号冲突，不能覆盖已执行迁移。

专项测试覆盖工作区／Bot 隔离、子区继承、重复绑定、已删除库过滤、解绑保留资产、超大字符串 ID、重复区域、事务审计回滚、RBAC 严格模式及 API Key 默认拒绝。PostgreSQL DDL 已在隔离副本事务内执行并回滚；之后已随本次发布应用于在线服务。

## 下一步

连接、名称与角色诊断已接通；下一步完成可信运行时请求身份和群内受限查询；不要给 OpenClaw 管理员登录凭据绕过身份层。继续复用数据源框架实现 Git／本地目录登记与只读检索，之后迁入有效旧配置。预览配置不得被称为已经驱动群问答。

## 官方协议依据

网关固定为官方服务，不接受浏览器传入任意地址；客户端拒绝重定向，校验返回 group_no、short_id、status 和非空名称，不把任意响应当作平台证据。

官方 [群接口](https://github.com/Mininglamp-OSS/octo-server/blob/main/modules/bot_api/groups.go)、[子区接口](https://github.com/Mininglamp-OSS/octo-server/blob/main/modules/bot_api/threads.go) 和 [群角色常量](https://github.com/Mininglamp-OSS/octo-server/blob/main/modules/group/const.go) 已核对。真人 role 1/2 分别对应群主/管理员；Bot 必须有显式 bot_admin 证据，缺失时不以真人 role 替代。

连接轮换后已有区域标记 needs_refresh；进行中的旧凭据响应不能覆盖新状态。refresh 仅更新元数据，不修改 Octo 群，也不发送消息。角色诊断只返回指定 UID 的判断，不展示全体成员名单。
