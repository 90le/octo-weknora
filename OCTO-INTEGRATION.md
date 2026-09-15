# Octo 集成接口与当前边界

更新：2026-09-15。配置后端、原生管理页面与官方元数据接入已部署；尚未切换 Bot 查询链路。新增数据源设计标明待实现，进度以 [OCTO-STATUS](OCTO-STATUS.md) 为准。

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

连接、名称与角色诊断已接通；下一步扩展原生数据源及安全同步，再完成可信运行时请求身份与群内受限查询，之后迁入有效旧配置。不要给 OpenClaw 管理员登录凭据绕过身份层。预览配置不得被称为已经驱动群问答。

## 原生数据源扩展设计（2026-09-15，待实现）

### 核对结论与复用边界

本地核对基线 `0f73bbf`；证据是可执行注册与服务调用，不能把连接器元数据当成可用能力。

| 现有实现 | 复用或补齐 |
|---|---|
| `internal/container/container.go:initConnectorRegistry` | GitLab 已注册，GitHub 注册仍被注释；补实际 GitHub 连接器，首版仅仓库文件，不宣传尚未支持的 Issue／Wiki |
| `internal/datasource/connector.go` | 复用 Connector／StreamingConnector、资源选择与增量游标；新增目录快照能力采用可选接口，避免迫使飞书等来源改用文件系统 |
| `internal/types/datasource.go` | DataSource 继续归属一个原生 KB，沿用连接配置、同步策略、状态；新增配置须有兼容默认值 |
| `internal/handler/dto/datasource.go` | 沿用凭据独立写入与响应剥离机制；连接器声明是否需要凭据，公开 GitHub／本地目录无需伪造 Token |
| `internal/datasource/scheduler.go` | 复用 cron、Asynq、运行状态与任务去重；Webhook 将来只触发同一任务链，不新增同步守护进程 |
| `internal/application/service/datasource_service.go:ingestItem` | 当前默认走文档解析，更新先删后建；目录只读不能走此分支，安全替换需要补齐，不能按现状承诺失败回退 |
| `internal/types/knowledge.go:FolderPath` | 保留原生 KB 导航路径；它不是物理磁盘路径，也不作为访问授权条件 |
| `DataSourceSettings.vue`、`DataSourceEditorDialog.vue`、`DataSourceSyncLogs.vue` | 扩展已有入口与交互，沿用 SettingDrawer、datasource-surface.less 和主题变量；按类型拆小表单，避免继续堆大段条件模板 |

### 对象关系与用户入口

`工作区 → 知识库 → 数据源配置 → 同步批次／文件清单 → 入库资料或目录快照`。

群／子区仍多对多绑定知识库；不直接持有服务器路径、仓库凭据或第二套来源授权。一个 KB 可以有多份数据源配置。需要多群复用时优先共享 KB，暂不抽象新的跨租户全局来源资产中心。

用户从“知识库 → 设置 → 数据源 → 添加数据源”进入：

1. 选择 GitHub、GitLab 或服务器文件夹等来源，配置连接。
2. 选择仓库／分支／目录；服务器文件夹选择获准根目录与子路径。
3. 指定用途与范围：资料入库、目录搜索读取；高级设置才展开路径规则与排除规则。
4. 预览将读取和跳过的范围，设置同步频率，保存并执行首次同步。

列表优先展示来源名称、用途、可用状态、最近成功同步与错误；提交、文件变化、解析详情放在详情／原生日志中。关系管理继续位于 Octo 使用范围，不在数据源页重复配置群。

### 来源与处理方式分开

来源类型决定“从哪里取”，处理规则决定“如何使用”。例如一个仓库：

```text
README.md + docs/**  → 原生文档解析与 KB 已选择的索引策略
src/**              → 固定版本目录搜索、文本搜索、按行读取；不向量化
node_modules/**     → 排除
```

同一来源可配置多个范围；默认只展示一种用途，混合规则在高级设置开启。源码用途是目录读取的预设，而不是额外发明一个 KB 类型。路径规则须后端校验、显示冲突；显式排除优先。入库资料仍可读取对应原文快照，两种访问方式可以共存，但不能重复解析或把只读源码隐式送去向量化。既有数据源不填新规则时维持原有资料入库行为。

文件夹是来源容器，支持嵌套目录与不限于 Markdown 的格式。结构、README／目录页入口、文档相对链接应保留；链接只能解析到同一获准来源的允许文件。二进制资料走现有解析器，源码不执行；不可解析文件显示原因。RAG 与 Wiki 是原生 KB 的内容处理策略，不是来源类型；这次不强制调整旧库索引策略。

首版保持 KB 为知识共享边界：绑定某 KB 即可使用该 KB 中被允许公开问答的资料。若产品手册可外部读但源码不可外部读，应分为两个 KB 并分别绑定，不能把同一 KB 的目录隐藏当权限隔离。

### 同步、版本与安全替换

- GitHub 首版优先使用官方 API，解析分支到 commit 后再读取 tree/blob，形成受管只读快照；GitLab 对齐相同的固定版本契约。文件版本以 blob／内容哈希判断，不把“分支名未变”当作内容未变。可采用 API 快照，无需为了读取源码先安装完整 Git 执行服务。
- GitHub 树接口可能截断，需逐子树读取或明确失败；不能把不完整清单当完整同步后批量删除资料。私有仓库使用只读权限，沿用原生密钥加密和 HTTP 安全策略，凭据不得随重定向发往其他站点。
- 新增的快照／文件清单附属于原生 DataSource；仅补版本、相对路径、哈希和激活状态等必要记录，不另建业务配置库。文件通过原生存储抽象或受管快照目录保存，数据库不塞整个仓库内容。
- 同源同步互斥，新版文件准备完成才切换目录快照；一轮目录读取固定 snapshot ID。资料入库复用原生解析流程，但需增加新版就绪后替换旧版的机制，保留失败时旧可用版本，不继续复制先删后建的路径。目录快照完成与解析／索引完成分开记录。
- 不承诺多数据源、整库 RAG 原子一致性；回答要记录实际使用的版本。暂停、失败、未完成、旧版仍可用与最新状态应能区分。
- 定时检查与手动同步先交付；具体频率可选，遵守平台限流并避免任务重叠。没有 Webhook 不声称即时同步；Webhook 后续补签名和重复事件处理，仍复用原队列。

### 服务器文件夹与内容归属

“本地”指 WeKnora 服务进程可访问的服务器目录。用户电脑上的 C: 路径不能由远端服务器直接读取；需一次性上传、受控复制到服务器或以后另加同步方式。

运维先把允许的根目录只读挂载进服务；平台管理员批准目录资源。群管理者仅能使用获授权的目录，不得通过表单任意读取主机路径。接口使用根目录标识＋相对路径，实际读取再次校验路径、符号链接及敏感文件；快照写入独立受管位置。文件扫描发现中途变化时重试或保留旧版，不伪造一致性；绝不自动 git pull 到用户正在编辑的本地工作树。

同步内容以来源为准：在外部仓库修改，下次同步更新；平台内需补充时另存独立资料或草稿并关联来源。AI 可帮助生成解释或修订草稿，记录所依据提交；自动同步不等于自动创作、审核或发布知识。

区分三个操作：移除来源配置、按策略同步源端删除、删除 KB。删除检测只针对该来源拥有且成功枚举确认的项；凭据失效、路径权限失败、API 截断和过滤规则改变不能被当成源文件删除。共享 KB 的影响沿用原生使用范围检查。

### 统一检索与权限

可信 Octo 请求先解析获准 KB，再调用 KB 下的原生 RAG 检索和受限目录接口（列目录、搜索、读取）。由 OpenClaw 理解问题并选择工具，WeKnora 后端决定可访问范围。不得把文件路径或模型提交的 UID 当作授权证据。

来源引用返回 source ID、快照／commit、相对路径和行号。GitHub 使用固定提交的原生代码链接，显示短文件名即可，不新增第三方短链服务；服务器文件用受保护预览。资料检索引用实际使用的片段版本，不能给旧索引内容挂最新分支链接。

官方参考：[WeKnora 数据源 API](https://github.com/Tencent/WeKnora/blob/main/website-docs/04-api/02-api-infra.md)、[GitHub Trees API](https://docs.github.com/en/rest/git/trees)、[GitHub Contents API](https://docs.github.com/en/rest/repos/contents)。实现与验收顺序以 [计划](OCTO-PLAN.md) 为准。本节是选定设计，不表示这些扩展已部署。

## 官方协议依据

网关固定为官方服务，不接受浏览器传入任意地址；客户端拒绝重定向，校验返回 group_no、short_id、status 和非空名称，不把任意响应当作平台证据。

官方 [群接口](https://github.com/Mininglamp-OSS/octo-server/blob/main/modules/bot_api/groups.go)、[子区接口](https://github.com/Mininglamp-OSS/octo-server/blob/main/modules/bot_api/threads.go) 和 [群角色常量](https://github.com/Mininglamp-OSS/octo-server/blob/main/modules/group/const.go) 已核对。真人 role 1/2 分别对应群主/管理员；Bot 必须有显式 bot_admin 证据，缺失时不以真人 role 替代。

连接轮换后已有区域标记 needs_refresh；进行中的旧凭据响应不能覆盖新状态。refresh 仅更新元数据，不修改 Octo 群，也不发送消息。角色诊断只返回指定 UID 的判断，不展示全体成员名单。
