# Octo 隔离沙箱的文档依赖

本目录的 Dockerfile 扩展现有 WeKnora 沙箱基础镜像，提供 Python 的 Word、Excel、PPT 和 PDF 文档库。它不包含平台凭据、知识库数据、Skill 包或联网授权。基础镜像固定到摘要，第三方 wheel 的版本和摘要由 `sandbox-artifacts-requirements.lock` 锁定；锁定清单不应在未经核对时批量更新。

下面的命令在能够连接镜像源和 PyPI 的 **Linux 构建环境**中执行；运行环境仍可保持无网络。构建目录必须是独立的临时目录，不要将凭据、文档或宿主机目录放进去：

```sh
mkdir -p /tmp/octo-sandbox-build/wheels
cp deploy/octo/sandbox-artifacts.Dockerfile deploy/octo/sandbox-artifacts-requirements.lock /tmp/octo-sandbox-build/
docker run --rm --entrypoint python3 \
  -v /tmp/octo-sandbox-build:/work -w /work \
  wechatopenai/weknora-sandbox@sha256:5e1cca768f35724a5bdee18e2191d37c0d4a3c78e703a5beaccc3fb4a204a5d9 \
  -m pip download --no-deps --only-binary=:all: --require-hashes \
  -r sandbox-artifacts-requirements.lock -d wheels
docker build --network none -f /tmp/octo-sandbox-build/sandbox-artifacts.Dockerfile \
  -t local/octo-sandbox:documents /tmp/octo-sandbox-build
docker image inspect local/octo-sandbox:documents --format '{{.Id}}'
```

构建阶段需要外网，固定摘要的离线安装阶段及实际执行沙箱不需要外网。启用前应独立核对基础镜像和产物没有凭据、宿主机挂载或不应公开的 Skill；记录镜像 ID、目标沙箱引擎及回退镜像。管理员改变镜像后，应重新验证每轮授权、隔离、取消、文件发送及资源限制。不要因此更改 Web 用户已有的会话文件或生产知识数据。

2026-11-03 候选镜像在独立沙箱 VM 上用合成资料完成了 Word、Excel、PPT、PDF 的生成和重新读取；1 CPU／1 GiB 与 2 CPU／2 GiB 的单容器样本均通过。这个结果不证明大文件、长期负载或多容器并发容量；实际运行限额和生产部署状态见 [项目状态](../../OCTO-STATUS.md) 与本机运维资料。

同日另用独立容器测试了只读根文件系统及有容量限制的 `/workspace`、`/tmp`：生成并读取四类合成文档成功，向只读根目录写入及超出工作目录容量均被拒绝。这是镜像兼容性实验，不代表生产执行路径已经采用该配置。若采用 tmpfs，应明确容器停止后工作目录不会保留；需要保留的成品必须先经授权登记和持久化。
