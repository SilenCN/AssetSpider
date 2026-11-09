#!/usr/bin/env bash
# AssetSpider 幂等部署脚本（在目标服务器上执行）
#
#   bash deploy.sh <release.tar.gz>
#
# 由 .github/workflows/deploy.yml 通过 SSH 调用，也可手动执行。
#  · 首次运行自动建目录、生成默认配置、安装并启用 systemd 服务
#  · data/（SQLite 数据库）与 conf/config.yaml（用户配置）永不被覆盖
#  · 部署后自动启动/重启服务并做健康检查，失败自动回滚到上一版本
#  · 无 systemd 的环境（容器 / 本地验证）自动降级为 nohup 进程模式
#
# 可用环境变量：DEPLOY_PATH / SERVICE_NAME / DEPLOY_MODE / HEALTH_URL /
#              KEEP_RELEASES / ASSETSPIDER_SERVER_ADDR（process 模式）
set -Eeuo pipefail

RELEASE_TAR="${1:-}"
DEPLOY_PATH="${DEPLOY_PATH:-/opt/assetspider}"
SERVICE_NAME="${SERVICE_NAME:-assetspider}"
DEPLOY_MODE="${DEPLOY_MODE:-auto}"
KEEP_RELEASES="${KEEP_RELEASES:-5}"
HEALTH_PATH="/api/system/v1/health"

log()  { printf '\033[1;34m[deploy]\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m[deploy]\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31m[deploy]\033[0m %s\n' "$*" >&2; exit 1; }

[ -n "$RELEASE_TAR" ] || die "用法: bash deploy.sh <release.tar.gz>"
[ -f "$RELEASE_TAR" ] || die "发布包不存在: $RELEASE_TAR"
command -v tar  >/dev/null 2>&1 || die "缺少 tar"
command -v curl >/dev/null 2>&1 || die "缺少 curl（健康检查需要）"
command -v cmp  >/dev/null 2>&1 || die "缺少 cmp"

OWNER="$(id -un)"
GROUP="$(id -gn)"
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    SUDO="sudo -n"
  fi
fi

if [ "$DEPLOY_MODE" = "auto" ]; then
  if [ -d /run/systemd/system ] && command -v systemctl >/dev/null 2>&1; then
    DEPLOY_MODE=systemd
  else
    DEPLOY_MODE=process
  fi
fi
case "$DEPLOY_MODE" in
  systemd|process) ;;
  *) die "不支持的 DEPLOY_MODE: $DEPLOY_MODE（可选 auto / systemd / process）" ;;
esac

if [ "$DEPLOY_MODE" = "systemd" ] && [ "$(id -u)" -ne 0 ] && [ -z "$SUDO" ]; then
  die "安装 systemd 服务需要 root 或免密 sudo；请改用 root 登录，或设置 DEPLOY_MODE=process"
fi

log "模式=$DEPLOY_MODE 目录=$DEPLOY_PATH 服务=$SERVICE_NAME 用户=$OWNER:$GROUP"

# ---------- 1. 目录骨架（data / conf 只创建，不清空） ----------
if [ -n "$SUDO" ]; then
  $SUDO mkdir -p "$DEPLOY_PATH"/bin "$DEPLOY_PATH"/web "$DEPLOY_PATH"/conf \
                 "$DEPLOY_PATH"/data "$DEPLOY_PATH"/releases "$DEPLOY_PATH"/logs
  $SUDO chown -R "$OWNER:$GROUP" "$DEPLOY_PATH"
else
  mkdir -p "$DEPLOY_PATH"/bin "$DEPLOY_PATH"/web "$DEPLOY_PATH"/conf \
           "$DEPLOY_PATH"/data "$DEPLOY_PATH"/releases "$DEPLOY_PATH"/logs
fi

# ---------- 2. 解压发布包到临时目录 ----------
STAGE="$(mktemp -d "$DEPLOY_PATH/releases/.stage.XXXXXX")"
trap 'rm -rf "$STAGE"' EXIT
tar -xzf "$RELEASE_TAR" -C "$STAGE"
[ -f "$STAGE/bin/assetspider" ]            || die "发布包缺少 bin/assetspider"
[ -d "$STAGE/web/dist" ]                   || die "发布包缺少 web/dist"
[ -f "$STAGE/deploy/assetspider.service" ] || die "发布包缺少 deploy/assetspider.service"
chmod 0755 "$STAGE/bin/assetspider"

# ---------- 3. 首次生成配置，绝不覆盖已有配置 ----------
if [ ! -f "$DEPLOY_PATH/conf/config.yaml" ]; then
  cp "$STAGE/conf/config.yaml.example" "$DEPLOY_PATH/conf/config.yaml"
  log "已生成默认配置 conf/config.yaml"
fi

# ---------- 4. 安装 systemd 单元与环境变量文件 ----------
ENV_FILE="/etc/$SERVICE_NAME.env"
if [ "$DEPLOY_MODE" = "systemd" ]; then
  UNIT_DST="/etc/systemd/system/$SERVICE_NAME.service"
  RENDERED="$STAGE/deploy/assetspider.service.rendered"
  sed -e "s|__USER__|$OWNER|g" \
      -e "s|__GROUP__|$GROUP|g" \
      -e "s|__DEPLOY_PATH__|$DEPLOY_PATH|g" \
      -e "s|__SERVICE_NAME__|$SERVICE_NAME|g" \
      "$STAGE/deploy/assetspider.service" > "$RENDERED"
  if [ ! -f "$UNIT_DST" ] || ! cmp -s "$RENDERED" "$UNIT_DST"; then
    $SUDO cp "$RENDERED" "$UNIT_DST"
    $SUDO chmod 0644 "$UNIT_DST"
    $SUDO systemctl daemon-reload
    log "已安装/更新 systemd 单元 $UNIT_DST"
  fi
  if [ ! -f "$ENV_FILE" ]; then
    $SUDO tee "$ENV_FILE" >/dev/null <<EOF
# AssetSpider 运行参数；本文件不会被部署覆盖，改完执行 systemctl restart $SERVICE_NAME 生效。
ASSETSPIDER_SERVER_ADDR=:8080
EOF
    $SUDO chmod 0644 "$ENV_FILE"
    log "已生成 $ENV_FILE"
  fi
  $SUDO systemctl enable "$SERVICE_NAME" >/dev/null 2>&1 \
    || warn "systemctl enable 未成功（restart 仍会启动服务，可稍后手动 enable）"
fi

# ---------- 5. 端口与健康检查地址 ----------
PORT=8080
if [ -f "$ENV_FILE" ]; then
  ADDR="$(grep -E '^ASSETSPIDER_SERVER_ADDR=' "$ENV_FILE" | tail -n 1 \
          | cut -d= -f2- | tr -d "\"'[:space:]" || true)"
  P="$(printf '%s' "$ADDR" | sed -nE 's/.*:([0-9]+)$/\1/p' || true)"
  [ -n "$P" ] && PORT="$P"
fi
if [ -z "${HEALTH_URL:-}" ]; then
  HEALTH_URL="http://127.0.0.1:$PORT$HEALTH_PATH"
fi
log "健康检查: $HEALTH_URL"

# ---------- 6. 备份当前版本 ----------
BACKUP=""
if [ -f "$DEPLOY_PATH/bin/assetspider" ]; then
  BACKUP="$DEPLOY_PATH/releases/backup-$(date +%Y%m%d%H%M%S)-$$"
  mkdir -p "$BACKUP"
  cp -a "$DEPLOY_PATH/bin/assetspider" "$BACKUP/assetspider"
  if [ -d "$DEPLOY_PATH/web/dist" ]; then
    cp -a "$DEPLOY_PATH/web/dist" "$BACKUP/dist"
  fi
  log "已备份旧版本: $BACKUP"
fi

# ---------- 7. 原子替换（先写 .new 再 mv，避免 Text file busy） ----------
install_version() {
  local src_bin="$1" src_dist="$2"
  install -m 0755 "$src_bin" "$DEPLOY_PATH/bin/assetspider.new"
  mv -f "$DEPLOY_PATH/bin/assetspider.new" "$DEPLOY_PATH/bin/assetspider"
  if [ -n "$src_dist" ] && [ -d "$src_dist" ]; then
    rm -rf "$DEPLOY_PATH/web/dist.new" "$DEPLOY_PATH/web/dist.old"
    cp -a "$src_dist" "$DEPLOY_PATH/web/dist.new"
    if [ -d "$DEPLOY_PATH/web/dist" ]; then
      mv "$DEPLOY_PATH/web/dist" "$DEPLOY_PATH/web/dist.old"
    fi
    mv "$DEPLOY_PATH/web/dist.new" "$DEPLOY_PATH/web/dist"
    rm -rf "$DEPLOY_PATH/web/dist.old"
  fi
}
install_version "$STAGE/bin/assetspider" "$STAGE/web/dist"
[ -f "$STAGE/VERSION" ] && cp "$STAGE/VERSION" "$DEPLOY_PATH/VERSION"
log "已更新二进制与前端资源"

# ---------- 8. 启动 / 重启 ----------
start_service() {
  if [ "$DEPLOY_MODE" = "systemd" ]; then
    $SUDO systemctl restart "$SERVICE_NAME"
  else
    local pidfile="$DEPLOY_PATH/run.pid"
    if [ -f "$pidfile" ]; then
      local oldpid
      oldpid="$(cat "$pidfile" 2>/dev/null || true)"
      if [ -n "$oldpid" ] && kill -0 "$oldpid" 2>/dev/null; then
        kill "$oldpid" 2>/dev/null || true
        for _ in $(seq 1 20); do
          kill -0 "$oldpid" 2>/dev/null || break
          sleep 0.2
        done
      fi
    fi
    (
      cd "$DEPLOY_PATH"
      export ASSETSPIDER_DB_PATH="$DEPLOY_PATH/data/assetspider.db"
      export ASSETSPIDER_WEB_DIR="$DEPLOY_PATH/web/dist"
      export ASSETSPIDER_SERVER_ADDR="${ASSETSPIDER_SERVER_ADDR:-:$PORT}"
      nohup "$DEPLOY_PATH/bin/assetspider" -config "$DEPLOY_PATH/conf/config.yaml" \
        >> "$DEPLOY_PATH/logs/assetspider.log" 2>&1 &
      echo "$!" > "$pidfile"
    )
  fi
}

wait_health() {
  local i
  # HEALTH_RETRIES 可用环境变量覆盖，便于慢启动机器或本地快速验证。
  for i in $(seq 1 "${HEALTH_RETRIES:-30}"); do
    if [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 "$HEALTH_URL" || true)" = "200" ]; then
      return 0
    fi
    sleep 1
  done
  return 1
}

rollback() {
  warn "新版本健康检查失败，开始回滚"
  if [ -z "$BACKUP" ] || [ ! -f "$BACKUP/assetspider" ]; then
    die "无可用备份，无法回滚。日志: $DEPLOY_PATH/logs/assetspider.log"
  fi
  local old_dist=""
  [ -d "$BACKUP/dist" ] && old_dist="$BACKUP/dist"
  install_version "$BACKUP/assetspider" "$old_dist"
  start_service
  if wait_health; then
    die "新版本启动失败，已回滚到上一版本；日志: $DEPLOY_PATH/logs/assetspider.log"
  fi
  die "回滚后服务仍不可用，请人工介入。日志: $DEPLOY_PATH/logs/assetspider.log"
}

start_service
if ! wait_health; then
  rollback
fi

# ---------- 9. 收尾 ----------
if [ "$DEPLOY_MODE" = "systemd" ]; then
  $SUDO systemctl is-active --quiet "$SERVICE_NAME" || die "服务未处于运行状态"
  log "服务状态: $($SUDO systemctl is-active "$SERVICE_NAME")（已设置开机自启）"
else
  log "进程 PID: $(cat "$DEPLOY_PATH/run.pid")（日志: $DEPLOY_PATH/logs/assetspider.log）"
fi
ls -1dt "$DEPLOY_PATH/releases"/backup-* 2>/dev/null \
  | tail -n +$((KEEP_RELEASES + 1)) | xargs -r rm -rf || true
log "部署完成 ✅ 版本 $(cat "$DEPLOY_PATH/VERSION" 2>/dev/null || echo unknown)"
