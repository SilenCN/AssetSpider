# 多平台打包与镜像发布

**任意分支 push**（或 Actions 页面手动触发 `workflow_dispatch`）都会运行 `.github/workflows/package.yml`，一次性产出：

1. **4 个平台的可执行包**（压缩包 + 裸二进制），上传为本次运行的 Artifacts（保留 14 天）；
2. **amd64 + arm64 多架构 Docker 镜像**，推送到 GHCR（GitHub Container Registry）；
3. **滚动更新的 `latest` Release**：把 4 个平台的压缩包、裸二进制以及 `SHA256SUMS.txt` 校验和作为资产上传，公开可下载。每次 push 删除旧 Release 及其 `latest` tag 后重建，资产始终指向最新构建，不会堆积历史版本。

它与现有 `[自动部署](deployment.md)` 流水线互不干扰：一个负责发版产物，一个负责更新生产服务器。

> 裸二进制（如 `assetspider-linux-amd64`、`assetspider-windows-amd64.exe`）是不带前端/配置的单个可执行文件，适合已有部署目录时只替换主程序；压缩包则自包含前端与配置模板，适合全新部署。

> 下载固定地址：`https://github.com/<owner>/AssetSpider/releases/latest`，每次 push 后其中的资产自动更新为最新版本；如需按提交追溯，资产标题里带有 commit 短哈希。

## 平台矩阵

SQLite 使用纯 Go 驱动（`modernc.org/sqlite`），`CGO_ENABLED=0` 静态编译，产物不依赖目标机 libc。

| 目标 | GOOS/GOARCH | 产物 | 适用 |
| --- | --- | --- | --- |
| 飞牛 x86-64 / 通用 Linux 64 位 | `linux/amd64` | `assetspider-linux-amd64.tar.gz` | x86 飞牛 NAS、普通 PC/服务器 |
| 飞牛 ARM | `linux/arm64` | `assetspider-linux-arm64.tar.gz` | 飞牛 ARM 版（RK3588/3399、S905x 等 aarch64） |
| Linux 32 位 | `linux/386` | `assetspider-linux-386.tar.gz` | 32 位 x86 设备 |
| Windows 64 位 | `windows/amd64` | `assetspider-windows-amd64.zip` | Windows |

> 飞牛 ARM 版设备均为 64 位 aarch64，故用 `linux/arm64` 包；32 位 ARM（armv7）不在当前矩阵内，如需可自行用 `GOARCH=arm GOARM=7` 编译。

## 发布包结构

每个压缩包解压后自包含，保持程序默认读取路径（`conf/config.yaml`、`web/dist`）：

```
assetspider-<os>-<arch>/
├── assetspider          # Windows 为 assetspider.exe
├── conf/config.yaml     # 配置模板（可改）
├── web/dist/            # 内置前端
├── VERSION              # 构建版本（提交 SHA）
└── README.txt           # 快速启动说明
```

解压后在本目录执行：

```bash
./assetspider -config conf/config.yaml     # Linux
```

```
assetspider.exe -config conf\config.yaml   # Windows
```

数据库默认生成在 `data/assetspider.db`，日志写入 `logs/`（见[配置](configuration.md) 的 `log` 段）。首次访问 `http://<主机>:8080`，第一个注册的账号自动成为管理员。

除压缩包外，每个平台还单独提供裸二进制，位于发布目录 / Release 资产中：

```
assetspider-linux-amd64      # Linux 64 位
assetspider-linux-arm64      # 飞牛 ARM
assetspider-linux-386        # Linux 32 位
assetspider-windows-amd64.exe
```

裸二进制可直接替换已有部署目录里的同名主程序，需自备 `conf/config.yaml` 与 `web/dist`；全新部署请用上面的压缩包。

## Docker 镜像（飞牛应用中心）

镜像地址：`ghcr.io/<owner>/assetspider`，标签 `latest` 与 `sha-<提交>`，同时含 `linux/amd64`、`linux/arm64` 两个架构，飞牛会按本机架构自动拉取对应层。

在飞牛 Docker / 应用中心创建容器时映射：

- 端口：宿主 `8080` → 容器 `8080`
- 数据持久化：宿主目录 → 容器 `/app/data`（数据库）
- 日志持久化：宿主目录 → 容器 `/app/logs`
- 自定义配置（可选）：宿主 `config.yaml` → 容器 `/app/conf/config.yaml`

容器内已设 `GIN_MODE=release`，前端内置于 `/app/web/dist`。

命令行方式：

```bash
docker run -d --name assetspider -p 8080:8080 \
  -v /vol1/1000/assetspider/data:/app/data \
  -v /vol1/1000/assetspider/logs:/app/logs \
  ghcr.io/<owner>/assetspider:latest
```

> 私有仓库需先在飞牛上配置 GHCR 凭据；公开仓库可直接拉取。若推送镜像失败，检查仓库 **Settings → Actions → General → Workflow permissions** 是否允许 `packages: write`。

## 本地打包

脚本 `scripts/package.sh` 本地与 CI 共用：

```bash
./scripts/package.sh                 # 构建前端 + 打全部平台包，输出到 release/
./scripts/package.sh linux/arm64     # 只打单个平台
SKIP_WEB=1 ./scripts/package.sh      # 复用已有 web/dist（已构建前端时）
VERSION=v0.4.0 ./scripts/package.sh  # 指定版本号
```

可用环境变量：`PLATFORM`（单目标）、`SKIP_WEB`、`OUT_DIR`（默认 `release/`）、`VERSION`（默认 git 短 SHA）。`release/` 已在 `.gitignore` 中，不会误提交。

本地构建镜像：

```bash
# 单架构（当前平台）
docker build -t assetspider:local .
# 多架构（需 buildx，产出不推送用 --load 仅支持单架构，推送到仓库用 --push）
docker buildx build --platform linux/amd64,linux/arm64 \
  -t ghcr.io/<owner>/assetspider:local --push .
```
