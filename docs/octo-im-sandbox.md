# Octo IM 沙箱执行与文件读取

Octo 渠道的 `allow_sandbox_shell`、`allow_sandbox_skills` 和 `allow_file_delivery` 是独立的显式布尔开关，缺省均为 `false`。旧 `allow_generated_files` 只保留原 Markdown 授权，不授权终端、Skill、联网或通用文件投递。Agent 还必须选择同租户的 Docker 沙箱；Skill 仅限 Agent 选择的已安装、已启用、摘要可验证的包。shell-only 不要求 Skill 镜像或已安装 Skill。

## 镜像与部署

候选实现创建只读 rootfs、96 MiB `/workspace` 和 64 MiB `/tmp` tmpfs，并核对实际容器配置。成功命令保持容器运行，同轮收集目标产物后停止；跨轮临时文件不保证保留。独立 VM 的 Docker 容器已验证连续命令共享工作区、停止后 tmpfs 清空及文档生成；当前代码路径的真实 Octo 收发仍待验收。

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
- 操作前、执行中及返回前重新核验授权。执行中约每秒复核，单次复核最多等待 2 秒；撤权并非瞬时生效，轮询间隔内仍可能缓冲有限输出。命令正常结束后保持容器直到轮次结束；出错、超时或取消时停止同一容器并确认状态。Docker 不可达或停止未确认时，不能声称进程已终止。

## 调用方与锁

持有会话锁后，在产物快照前调用 `PrepareIMSandboxTurn`，确认旧容器停止并将空工作区状态绑定到本轮；未经过该准备的空基线必须同时核验 SQL pin 和底层绑定。实际读取前复核同一会话和沙箱，将读取限定到 `/workspace/output/`，通过 `ReadSessionFileBounded` 和组件级 NOFOLLOW 读取；发送前再次核验文件投递授权。

整轮处理结束前封闭新操作、等待进行中的操作结束，并确认受影响容器停止；失败时持锁重试。文件投递集成必须在整条产物收集路径结束后才调用 `types.SealIMSandboxTurn`，成功后释放锁。锁失租或进程崩溃后，下一持有者执行前必须先确认旧容器停止；内存屏障本身不能证明跨进程故障下的锁隔离。

## 已有会话恢复

若重新授权且 Agent、沙箱配置、Skill 集和干净镜像的指纹完全恢复原值，可重新使用相同的停止容器，但停止后 tmpfs 工作区内容不保证保留。若指纹变化，旧容器被拒绝，不能自动覆盖、删除或启动带有旧 Skill 的容器。旧版本持久工作区应先保全，不能靠清除 pin 或删除旧容器恢复。

候选文档镜像已在独立 VM 做过 96 MiB/64 MiB 合成测试；该历史测试并非本次创建代码的验证。tmpfs 在容器停止时会消失。集成文件投递时必须在同一会话锁内完成目标产物持久化，然后才停止容器；取消时未完成的临时工作允许丢弃。旧会话文件必须先安全保全，真实端到端流程尚未验收。
