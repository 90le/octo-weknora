# Building octo-weknora on the deployment network

The first successful release used these network-only overrides:

- Debian packages: Tsinghua mirror; APT signature verification unchanged.
- Go modules: goproxy.cn, with direct fallback and `sum.golang.org` verification.
- Rust toolchains and Cargo registry: rsproxy.cn; toolchain/crate integrity checks unchanged.

These are optional deployment-network choices, not changes to the host VPN or global proxy. Do not disable anydoc to work around a slow Rust download.

From a complete, clean Linux checkout with Docker Buildx:

```sh
revision=$(git rev-parse HEAD)
python3 scripts/octo_build_recipe.py --output /tmp/octo-Dockerfile.app
docker build --network=host \
  --build-arg GOPROXY_ARG=https://goproxy.cn,direct \
  --build-arg GOSUMDB_ARG=sum.golang.org \
  --build-arg VERSION_ARG=0.8.0-octo \
  --build-arg COMMIT_ID_ARG="$revision" \
  -f /tmp/octo-Dockerfile.app -t "local/octo-weknora-app:${revision}" .
docker build --build-arg VITE_FRONTEND_COMMIT="$revision" \
  -f frontend/Dockerfile -t "local/octo-weknora-ui:${revision}" frontend
```

`--network=host` applies only to build networking. Use it only where permitted; it does not publish application ports. On networks where upstream downloads are fast, the original Dockerfile remains available.

Record both the application source commit and generated recipe SHA-256. A source commit alone does not capture network/build overrides or floating upstream tool versions. The generator fails if its expected Rust environment anchor changes, requiring review.

## Current release evidence

2026-09-14: app and frontend built from `3b1c23053eebb3dbeb118607a44e54da6013bf69` were deployed. The generated app recipe hash was `eb94e61da84d634d616d4767a62b06270a530fb63ecdbdde868438f52edcdd42`.

Verified: CI, restored-database migrations 91–95, isolated startup and authenticated KB access, running image identities, unchanged three-KB inventory, and one live hybrid search after switching. The search returned HTTP 200 with results in 1.09 seconds; this is one functional check, not a capacity benchmark.

Not claimed: post-release end-to-end Octo conversation acceptance, a new document parsing regression, or completion of the future Octo management module.

## 可选解析器缓存布局

`python scripts/octo_build_recipe.py --cache-parser --output /tmp/Dockerfile.octo` 将 Rust/Anydoc 构建放到完整应用源码与提交元数据之前，只依赖固定的解析器目录和构建脚本。后续业务修改可复用这部分 Docker 层，不关闭解析器，也不跳过网络校验。

结构检查已通过；当前发布仍使用先前验证的配方。首次采用这个新选项仍需验证完整镜像和解析功能，不能把结构检查当成实际缓存加速验收。
