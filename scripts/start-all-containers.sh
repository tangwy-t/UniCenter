#!/usr/bin/env bash
# ============================================================================
# 恢复被 scripts/stop-all-containers.sh 停掉的宿主机容器
#
# 配套：stop-all-containers.sh
#
# 本脚本只做一件事：把当时停掉的 8 个容器按原顺序 docker start 回来。
# 因为用的是 `docker stop`（不是 rm），容器定义、网络、环境变量、挂载、
# 端口映射全部原样保留 —— 不需要重建，数据卷（MySQL/Redis/Postgres/上传
# 目录）也从未被触碰。
#
# 用法：
#   scripts/start-all-containers.sh          # 启动全部
#   scripts/start-all-containers.sh --check  # 只做状态检查，不启动
# ============================================================================
set -euo pipefail

# 依赖顺序：数据库/缓存 → 后端 → 前端/追踪
# 反向停止时就按这个顺序倒过来，保证「先断上层、再断底层」。
SERVICES=(
  ruoyi-mysql      # backend 项目：MySQL（UniCenter 的库也在实例内）
  ruoyi-redis      # backend 项目：Redis
  postgres         # 独立 Postgres
  wmf-jaeger       # web-manager-framework：追踪
  rental_server    # ruoyi 项目：后端
  rental_web       # ruoyi 项目：前端
  unicenter-dev-core    # 本项目：Go 后端
  unicenter-dev-console # 本项目：nginx 前端
)

check_only=0
[ "${1:-}" = "--check" ] && check_only=1

echo "═══ 容器状态 ═══"
for c in "${SERVICES[@]}"; do
  if docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null | grep -q true; then
    printf '  %-24s %s\n' "$c" "运行中"
  else
    printf '  %-24s %s\n' "$c" "已停止"
  fi
done
[ "$check_only" = 1 ] && exit 0

echo
echo "═══ 启动（按依赖顺序）═══"
for c in "${SERVICES[@]}"; do
  if docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null | grep -q true; then
    printf '  %-24s 已在运行，跳过\n' "$c"
    continue
  fi
  if docker start "$c" >/dev/null 2>&1; then
    printf '  %-24s 已启动\n' "$c"
  else
    printf '  %-24s 启动失败\n' "$c" >&2
  fi
done

echo
echo "═══ 等待就绪并自检 ═══"
for i in $(seq 1 30); do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 http://127.0.0.1:18080/ 2>/dev/null || echo 000)
  [ "$code" = "200" ] && break
  sleep 2
done
echo "  UniCenter 前端 : HTTP $code"
echo "  登录接口      : HTTP $(curl -s -o /dev/null -w '%{http_code}' -X POST http://127.0.0.1:18080/api/v1/login -H 'Content-Type: application/json' -d '{"username":"admin","password":"admin123"}' --max-time 10 2>/dev/null || echo 000)"
echo
echo "  各端口："
for p in 18080 18088 8080 8081 16686 3306 6379 5432; do
  if ss -ltn 2>/dev/null | grep -q ":$p "; then echo "    $p ✓ 监听中"; else echo "    $p ─ 未监听"; fi
done
