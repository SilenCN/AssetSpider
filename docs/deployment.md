# 自动部署

> 项目总览：[README](../Readme.md) · 相关：[配置](configuration.md)

**推送到 `main` 分支即可自动部署到 `140.238.43.173` 并启动服务**，全程无需登录服务器。

```
git push origin main
   └─ GitHub Actions (.github/workflows/deploy.yml)
        ├─ 检出代码，SSH 探测服务器 CPU 架构（amd64 / arm64 自适应）
        ├─ CGO_ENABLED=0 静态编译 Go 二进制 + npm ci && npm run build 构建 Vue
        ├─ 打成自包含发布包 tar.gz（二进制 / web/dist / 配置样例 / systemd 单元 / 部署脚本）
        ├─ scp 上传到服务器 /tmp
        └─ ssh 执行 deploy/deploy.sh
             ├─ 首次：建目录、生成 conf/config.yaml、安装并 enable systemd 服务
             ├─ 备份旧版本 → 原子替换二进制与前端 → 重启服务
             ├─ 轮询 /api/system/v1/health 探活（默认最多 600 秒，`HEALTH_TIMEOUT` 可调）
             └─ 失败自动回滚到上一版本；成功清理过期备份（保留 5 份）
```

设计要点：**构建发生在 CI runner 上**，服务器不需要 Go / Node，也不需要 `git pull` 访问 GitHub（避免国内服务器拉取不稳定）。SQLite 使用纯 Go 驱动，二进制静态链接，交叉编译到 amd64 / arm64 都能直接运行。发布包自包含，手动部署同样只需一个文件。`data/`（数据库）与 `conf/config.yaml`（用户配置）**永不被部署覆盖**。

## 一次性配置

**① 生成部署专用密钥，并把公钥放到服务器**（私钥只填到 GitHub，绝不要提交进仓库）：

```bash
ssh-keygen -t ed25519 -C "assetspider-deploy" -f ~/.ssh/assetspider_deploy -N ""
ssh-copy-id -i ~/.ssh/assetspider_deploy.pub ubuntu@140.238.43.173
cat ~/.ssh/assetspider_deploy        # 全文复制，填到 Secret DEPLOY_SSH_KEY
```

**② 在仓库 Settings → Secrets and variables → Actions 添加：**

| 类型 | 名称 | 必填 | 默认值 | 说明 |
| --- | --- | --- | --- | --- |
| Secret | `DEPLOY_SSH_KEY` | ✅ | — | 上面生成的**私钥全文**（含 BEGIN / END 行） |
| Secret | `DEPLOY_KNOWN_HOSTS` | 可选 | `deploy/known_hosts` | 服务器 SSH 主机公钥；换服务器时必须更新，用于防中间人 |
| Variable | `DEPLOY_HOST` | 可选 | `140.238.43.173` | 服务器 IP |
| Variable | `DEPLOY_USER` | 可选 | `ubuntu` | SSH 用户：OCI Ubuntu 为 `ubuntu`，Oracle Linux 为 `opc`，也可 `root` |
| Variable | `DEPLOY_PORT` | 可选 | `22` | SSH 端口 |
| Variable | `DEPLOY_PATH` | 可选 | `/opt/assetspider` | 部署根目录 |
| Variable | `DEPLOY_MODE` | 可选 | `auto` | `auto` / `systemd` / `process`（无 systemd 时降级为 nohup 进程） |
| Variable | `HEALTH_URL` | 可选 | 自动推导 | 改了服务端口时建议显式填写完整健康检查地址 |
| Variable | `HEALTH_TIMEOUT` | 可选 | `600` | 健康检查最长等待秒数。留足余量应对大库首次启动的一次性维护（历史数据后台回灌、统计信息更新等）；评分排序表达式索引移除后启动路径不再有同步 `CREATE INDEX`，实际探活通常秒级完成 |

> 未配置 `DEPLOY_SSH_KEY` 时，工作流**只做构建校验、跳过部署**（保持绿色并给出警告）；配好后可在 Actions 页面点 **Run workflow** 手动重跑，不必空推一次。纯文档改动（`**.md`）不触发部署，`workflow_dispatch` 可随时手动触发；多次 push 串行执行，不会并发写同一台服务器。

**③ 服务器前置条件**

- systemd（Ubuntu 20.04+，脚本用 `systemctl` 管理进程并设置开机自启）
- SSH 用户具备免密 `sudo`（OCI 镜像默认满足），或直接使用 root
- 云安全组与主机防火墙放行 **22**（GitHub Actions runner 入站）与 **8080**（对外访问页面）；Oracle Cloud 需在控制台 Security List / NSG **和**主机 iptables 两处都放行
- 已安装 `curl`、`tar`（Ubuntu 默认自带）

## 首次部署与验证

配置完成后 `git push origin main`，或到 Actions →「自动部署到生产服务器」→ **Run workflow**。工作流成功即表示服务已启动并通过健康检查。

```bash
ssh ubuntu@140.238.43.173
sudo systemctl status assetspider            # active (running)
cat /opt/assetspider/VERSION                 # 当前部署的 commit sha
curl http://127.0.0.1:8080/api/system/v1/health
# 浏览器访问 http://140.238.43.173:8080 ，第一个登录的账号自动成为管理员
```

部署后的目录：

```
/opt/assetspider/
├── bin/assetspider              # 静态二进制
├── web/dist/                    # Vue 前端
├── conf/config.yaml             # 运行配置（仅首次生成，之后不会被覆盖）
├── data/assetspider.db          # SQLite 数据库（永不覆盖）
├── logs/assetspider.log         # 服务日志
├── releases/backup-*            # 最近 5 个可回滚版本
├── VERSION                      # 当前 commit sha
└── run.pid                      # 仅 process 模式
/etc/systemd/system/assetspider.service
/etc/assetspider.env             # 端口等运行参数，改完 restart 生效（不会被部署覆盖）
```

## 日常运维

```bash
sudo systemctl status assetspider                  # 状态
sudo systemctl restart assetspider                # 重启
sudo systemctl stop assetspider                   # 停止
tail -f /opt/assetspider/logs/assetspider.log     # 实时日志
sudo journalctl -u assetspider -n 100 --no-pager  # systemd 日志
cat /opt/assetspider/VERSION                      # 当前版本

# 改端口：编辑 env（部署不会覆盖它），再重启；记得同步 Variable HEALTH_URL 与防火墙
echo 'ASSETSPIDER_SERVER_ADDR=:9090' | sudo tee /etc/assetspider.env
sudo systemctl restart assetspider

# 手动回滚：从备份恢复二进制后重启
ls -1t /opt/assetspider/releases/
sudo cp /opt/assetspider/releases/backup-XXXX/assetspider /opt/assetspider/bin/assetspider
sudo systemctl restart assetspider
```

**不走 GitHub 的手动部署**：按 `deploy.yml` 里「构建自包含发布包」步骤本地打一个 tar.gz（或直接使用 CI 产物），上传到服务器执行：

```bash
scp assetspider-release.tar.gz ubuntu@140.238.43.173:/tmp/
ssh ubuntu@140.238.43.173
mkdir -p /tmp/as && tar -xzf /tmp/assetspider-release.tar.gz -C /tmp/as
bash /tmp/as/deploy/deploy.sh /tmp/assetspider-release.tar.gz
```

## 故障排查

| 现象 | 排查方向 |
| --- | --- |
| 工作流跳过部署，日志有 warning | 未配置 Secret `DEPLOY_SSH_KEY`，或变量名写错 |
| `tar: deploy/deploy.sh: Not found in archive` | 老版本工作流按成员名从发布包里挑单个文件，归档成员带 `./` 前缀导致匹配不到；已改为全量解包。若手动打包，请勿用 `tar -C <dir> .`，改用显式列出 `bin web conf deploy VERSION` |
| `Permission denied (publickey)` | 公钥没进服务器 `~/.ssh/authorized_keys`；`DEPLOY_USER` 与公钥所属用户不一致；私钥粘贴时缺 BEGIN / END 行 |
| `Host key verification failed` | 服务器重装或换 IP：更新 Secret `DEPLOY_KNOWN_HOSTS` 或仓库 `deploy/known_hosts` |
| `Connection timed out` | 云安全组 / iptables 未对 GitHub runner 放行 22 端口 |
| 部署成功但页面打不开 | 8080 未在云控制台与主机防火墙双向放行 |
| 健康检查失败并自动回滚 | 看 `logs/assetspider.log`；常见是 8080 被占用、`/etc/assetspider.env` 端口与 `HEALTH_URL` 不一致，或老库首次启动正在做数据迁移（耗时较长，可调大 `HEALTH_TIMEOUT`）。**若新版本启动特别慢**：检查是否把一次性回灌 / 迁移放进了 `repository.Open` 的同步路径——服务迟迟不监听，健康检查超时后回滚又 kill 掉迁移，会形成「每次部署都失败、迁移永远做不完」的死循环。一次性重活必须放到服务启动之后的后台任务，并写入可续跑的进度（参见[配置](configuration.md)的 2 字索引回灌） |
| `sudo: a password is required` | 部署用户没有免密 sudo：改用 root，或设置 `DEPLOY_MODE=process` |
| 服务能启动但采集异常 | 检查 `conf/config.yaml` 与 `data/` 权限；两者不会被部署覆盖 |

> 安全建议：为部署单独生成密钥对并限制权限，不要复用个人私钥。`deploy/known_hosts` 是公开的主机公钥（非敏感信息）；私钥、`.env` 与数据库均已被 `.gitignore` 排除，切勿提交。
