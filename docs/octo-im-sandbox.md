# Octo IM 沙箱执行与文件读取

Octo 渠道的 `allow_sandbox_shell`、`allow_sandbox_skills` 和 `allow_file_delivery` 是独立的显式布尔开关，缺省均为 `false`。旧 `allow_generated_files` 只保留原 Markdown 授权，不授权终端、Skill、联网或通用文件投递。Agent 还必须选择同租户的 Docker 沙箱；Skill 仅限 Agent 选择的已安装、已启用、摘要可验证的包。shell-only 不要求 Skill 镜像或已安装 Skill。

## 镜像与部署

**当前阻塞：本分支没有接入可验证的持久磁盘配额实现。** Docker 可写 overlay 可能耗尽 VM 磁盘，因此 Octo shell/Skill 在创建容器前返回 `ErrIMDiskLimitUnavailable`；只设置下列环境变量不会开启执行，不得用于生产放行。后续需实现并接线受信的 `IMSandboxDiskVerifier`，验证实际容器及持久工作区的硬配额；测试中的 verifier 只是模拟。

在隔离 Docker 守护进程上准备并审计干净基础镜像：不得含未授权 Skill、用户或宿主凭据、知识库数据库或 Docker socket。必须具备 `bash`、`timeout`、`python3`，且 `/workspace` 可写。镜像摘要只能固定身份，不能证明文件内容安全。Python 文档库可放在基础镜像内，不等同于授权 Skill。

将镜像的本地不可变 ID 配置到 **WeKnora 进程环境**，并保留已有 Docker 后端总开关和管理员选择的 Docker 配置：

```text
WEKNORA_SANDBOX_DOCKER_ENABLED=true
OCTO_IM_SANDBOX_CLEAN_IMAGE_ID=sha256:<64位本地镜像ID>
```

例如在隔离 Docker VM 上用 `docker image inspect local/octo-sandbox:documents-651dadee --format '{{.Id}}'` 取得该标签当前的本地 ID；将输出填入 WeKnora 的部署环境，再按既有发布流程重启服务。不要把标签本身填入该变量。目标守护进程必须已有该镜像，IM 路径不会自动从仓库拉取；未配置、未审计或不可用时拒绝执行。此文档不代表生产已配置或验收。

## 边界与限制

- 每容器 CPU 上限固定为 1 核、内存上限固定为 1 GiB、PIDs 上限固定为 128；管理员配置更低的值会保留，调高配置不能突破这些上限。此变更没有新增跨容器的 CPU、内存或并发总量护栏。
- Docker 强制 `network=none`、丢弃全部 capabilities 且不添加 capabilities，不注入租户环境变量、`system-{tenant}` 用户密钥或 Skill 凭据，不使用宿主/Lite 后备，不挂载用户配置的卷。实际隔离还依赖可信的 Docker 守护进程、经过审计的镜像与真实挂载检查。
- 命令最长 120 秒；stdout 和 stderr 各最多 128 KiB，IM 不发送实时 shell 输出。目录枚举最多 1000 个文件及目录、深度 16、编码结果约 120 KiB、运行 10 秒；受控文件投递单文件上限 20 MiB。
- 仅从经过摘要核验的包暂存获准 Skill，复用前检查文件内容；单个 Skill 资源超过 256 KiB 时完整性复核会拒绝。Skill-only 不开放通用写文件/编辑工具。已批准的 Skill 是可信代码，仍可能自行生成代码或执行任意副作用，不能把名称白名单当作程序行为隔离。
- 操作前、执行中及返回前重新核验授权。执行中约每秒复核，单次复核最多等待 2 秒；撤权并非瞬时生效，轮询间隔内仍可能缓冲有限输出。命令正常结束、出错或取消时停止同一容器并确认状态；Docker 不可达或停止未确认时，不能声称进程已终止。

## 调用方与锁

持有并续租会话锁后，在产物 begin 前调用 `PrepareIMSandboxContext(ctx, true)`，并在整轮保留返回的上下文；然后调用 `ReconcileIMSandboxBeforeTurn` 确认旧绑定容器已停止。只有 `ValidateIMSandboxEmptyBaseline` 确认 SQL pin 和底层 binding 都不存在，才能使用空基线；binding 已存在或读取失败时必须拒绝或安全快照。实际读取前使用 `PrepareIMSandboxContext(ctx, false)` 重新核验、确认 `EnsureIMSession(..., false)` 返回同一容器，并限定到 `/workspace/output/`，通过 `ReadSessionFileBounded` 和组件级 NOFOLLOW 读取。受控 `ReadSessionFileBounded` 自行设置必要的原始流预算；直接使用底层 Docker reader 时，输出大于默认 256 KiB 需要传递适当的 `WithIMDockerOutputLimit`，但不得超过 20 MiB；发送前还须重新核验文件投递授权。

`runQA` 和 `handleMessageStream` 返回前会封闭本轮新操作、等待进行中的操作结束，并确认受影响容器停止。失败时处理函数保持不返回并重试。调用方的可续租会话锁必须在整条产物收集路径结束后调用 `types.SealIMSandboxTurn`，仅在成功时释放；锁失租或进程崩溃时还须有持久隔离/恢复机制，下一持有者必须先确认旧容器停止。本分支的内存屏障不能单独证明跨进程崩溃安全。

## 已有会话恢复

在已接入真实磁盘隔离验证器后，若重新授权且 Agent、沙箱配置、Skill 集和干净镜像的指纹完全恢复原值，可重新使用相同的停止容器与文件；普通的 stale 标记不触发删除。若指纹确实变化，旧容器被拒绝，不能自动覆盖、删除或启动带有旧 Skill 的容器。先由有权限的运维人员在原隔离环境保全并核验旧文件，再开启新会话使用干净容器；现阶段没有实现可证明安全的同会话无损迁移，不得靠清除 pin 或删除旧容器来恢复。

只读 rootfs 加有大小限制的 `/workspace`、`/tmp` tmpfs 可限制写入；候选文档镜像已在独立 VM 做过 96 MiB/64 MiB 合成测试。tmpfs 在容器停止时会消失，不能直接替换当前持久工作区：需要先完成并核验成品持久化，再停止容器；旧会话文件必须先安全保全。该完整生命周期及真实集成尚未实现或验收。
