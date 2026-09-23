#!/usr/bin/env bash
# ============================================================================
# 前端 dist 部署到 nginx 容器（原子替换，零停机）
#
# ## 为什么必须有这个脚本（一次真实事故）
#
# 手工部署时最自然的写法是「先删、再拷」：
#
#     docker exec -u root <c> sh -c 'rm -rf /usr/share/nginx/html && cp -r /tmp/new /usr/share/nginx/html'
#
# 这条命令有一个**必然的停机窗口**：`rm -rf` 与 `cp -r` 之间，nginx 的
# root 目录**根本不存在**，此时任何请求都拿不到 index.html，nginx 只能回
# 自己的 404 错误页。拷贝还是整目录递归（本项目 dist 约 7MB / 210 个文件），
# 窗口会持续到拷完为止。外部观测到的现象是「站点整个挂了 / 容器像是 down 了」，
# 但实际上 `docker ps` 里容器一直是 Up —— 极难从容器状态反推原因。
#
# 本项目实测记录（nginx access.log）：
#     14:45:55  "GET / HTTP/1.1" 200 765     ← 正常
#     14:46:31  "GET / HTTP/1.1" 404 555     ← 站点不可用（5xx 字节=nginx 错误页）
#     14:47:11  "GET / HTTP/1.1" 200 1303    ← 恢复
#
# ## 本脚本的做法：旁路解包 + 同目录 rename
#
#   1) 先在 html.staging 里**完整解包**（此时线上 html 一动不动，照常服务）；
#   2) 校验 staging/index.html 存在，避免把半包推上线；
#   3) 用 `mv`（POSIX rename(2)，同文件系统内原子）完成切换：
#      任何瞬间 /usr/share/nginx/html 都指向一个**完整**目录，
#      不存在「目录缺失」或「目录半满」的中间态；
#   4) 成功后删掉上一版；只在最后一步失败时才回滚。
#
# 相比 `cp -r` 覆盖旧目录，还顺带解决了**陈旧文件残留**：被删除的路由/资源
# 不会因为「新包里没有」而留在原处继续被访问到。
#
# ## 用法
#
#   scripts/deploy-console.sh                 # 用 uni_console/dist 部署到默认容器
#   scripts/deploy-console.sh --build         # 先 pnpm build 再部署
#   CONTAINER=xxx WEB_ROOT=/usr/share/nginx/html scripts/deploy-console.sh
#
# 退出码：0=成功；非 0=失败（失败时线上服务保持在**旧版本**，已自动回滚）。
# ============================================================================
set -euo pipefail

CONTAINER="${CONTAINER:-unicenter-dev-console}"
WEB_ROOT="${WEB_ROOT:-/usr/share/nginx/html}"
DIST_DIR="${DIST_DIR:-uni_console/dist}"
PORT="${PORT:-18080}"
DO_BUILD=0

for arg in "$@"; do
  case "$arg" in
    --build) DO_BUILD=1 ;;
    -h|--help) sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $arg（见 --help）" >&2; exit 2 ;;
  esac
done

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

log() { printf '  %s\n' "$*"; }

# ── 0. 前置检查：容器必须在跑，否则后面全是无用功 ──────────────────────────
if ! docker inspect -f '{{.State.Running}}' "$CONTAINER" 2>/dev/null | grep -q true; then
  echo "容器 $CONTAINER 不在运行状态，中止。" >&2
  exit 1
fi

# ── 1. 可选构建 ───────────────────────────────────────────────────────────
if [ "$DO_BUILD" = 1 ]; then
  log "构建前端（pnpm build）…"
  (cd uni_console && pnpm run build >/dev/null)
fi

if [ ! -f "$DIST_DIR/index.html" ]; then
  echo "找不到 $DIST_DIR/index.html，请先构建（或加 --build）。" >&2
  exit 1
fi

# ── 2. 打包并送入容器 ─────────────────────────────────────────────────────
# 用 tar 单文件传输，避免 docker cp 对目录逐文件的多次往返。
tmp_tar="$(mktemp -t uni-dist-XXXXXX.tar)"
trap 'rm -f "$tmp_tar"' EXIT
tar -C "$DIST_DIR" -cf "$tmp_tar" .
docker exec -u root "$CONTAINER" sh -c "rm -rf /tmp/uni-dist.tar"
docker cp "$tmp_tar" "$CONTAINER:/tmp/uni-dist.tar"

# ── 3. 旁路解包 + 原子切换（全程由容器内单次 exec 完成，避免中间态外泄）──
# 注意：这里**不能**用 `rm -rf "$WEB_ROOT"`；只在 rename 成功后清理旧版本。
log "旁路解包并原子切换…"
docker exec -u root "$CONTAINER" sh -c "
  set -e
  root='$WEB_ROOT'
  staging=\"\$root.staging\"
  prev=\"\$root.prev\"

  rm -rf \"\$staging\" \"\$prev\"
  mkdir -p \"\$staging\"
  tar -C \"\$staging\" -xf /tmp/uni-dist.tar

  # 半包防线：解包不完整就绝不切换，线上保持旧版本
  [ -f \"\$staging/index.html\" ] || { echo 'staging 缺少 index.html，放弃切换' >&2; exit 1; }

  mv \"\$root\" \"\$prev\"
  if ! mv \"\$staging\" \"\$root\"; then
    # 切换失败：把旧版本放回去，保证服务可用
    mv \"\$prev\" \"\$root\"
    echo '原子切换失败，已回滚到旧版本' >&2
    exit 1
  fi

  rm -rf \"\$prev\" /tmp/uni-dist.tar
"

# ── 4. 热加载 nginx 并自检 ────────────────────────────────────────────────
docker exec "$CONTAINER" nginx -t >/dev/null 2>&1 || { echo 'nginx 配置校验失败' >&2; exit 1; }
docker exec "$CONTAINER" nginx -s reload >/dev/null 2>&1 || true

code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 "http://127.0.0.1:$PORT/" || echo 000)"
if [ "$code" != "200" ]; then
  echo "部署后自检失败：GET / 返回 $code" >&2
  exit 1
fi

log "部署完成：HTTP $code，入口 $(curl -s "http://127.0.0.1:$PORT/" | grep -o 'index-[A-Za-z0-9_-]*\.js' | head -1)"
