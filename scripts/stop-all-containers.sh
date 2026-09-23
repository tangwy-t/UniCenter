#!/usr/bin/env bash
# ============================================================================
# 停止宿主机上全部运行中的容器（8 个）
#
# ⚠️ 影响面：本脚本停的是**宿主机上所有运行中的容器**，不止 UniCenter。
#    当前宿主机上的容器分属 5 个项目：
#
#      unicenter-dev-console / unicenter-dev-core  本项目（UniCenter）
#      ruoyi-mysql / ruoyi-redis                   backend 项目
#      rental_web / rental_server                  ruoyi 项目
#      wmf-jaeger                                  web-manager-framework
#      postgres                                    独立实例
#
#    ⚠️ 注意跨项目依赖：UniCenter 的 core 容器连的是
#       ruoyi-mysql:3306（库 uni_center_dev）与 ruoyi-redis:6379（DB=3），
#       这两个实例属于 backend 项目。所以停容器会让 UniCenter 一并不可用 ——
#       这是本条依赖链的正常结果，不是故障。
#
# ## 为什么用 stop 而不是 rm
#
# `docker stop` 只是停进程：容器定义、网络、环境变量、挂载、端口映射
# 全部原样保留，`docker start` 即可原样复活，数据卷也从未被触碰。
# `docker rm` 会删掉容器本体，手工创建的容器（本项目这两个没有 compose
# project 标签、名字也与 docker-compose.yml 里的 uni-center-core/uni-center-console
# 不一致，**无法用 docker compose up 重建**）就得靠 inspect 定义手工复原。
#
# 恢复：scripts/start-all-containers.sh
#
# 用法：
#   scripts/stop-all-containers.sh --dry-run   # 只列出将停哪些，不动手
#   scripts/stop-all-containers.sh             # 真正停止
# ============================================================================
set -euo pipefail

# 停止顺序 = 启动顺序的逆序：先断前端/上层，再断后端，最后断数据库/缓存，
# 避免上层容器在等待已消失的依赖时刷错误日志或进入崩溃重启循环。
SERVICES=(
  unicenter-dev-console # 本项目：nginx 前端
  unicenter-dev-core    # 本项目：Go 后端
  rental_web            # ruoyi 项目：前端
  rental_server         # ruoyi 项目：后端
  wmf-jaeger            # web-manager-framework：追踪
  postgres              # 独立 Postgres
  ruoyi-redis           # backend 项目：Redis
  ruoyi-mysql           # backend 项目：MySQL
)

dry_run=0
[ "${1:-}" = "--dry-run" ] && dry_run=1

echo "═══ 待停止的容器 ═══"
targets=()
for c in "${SERVICES[@]}"; do
  if docker inspect -f '{{.State.Running}}' "$c" 2>/dev/null | grep -q true; then
    # restart 策略为 unless-stopped/always 的容器，停掉后不会被自动拉起
    # （unless-stopped 的语义正是「人为停止后不再重启」）。
    pol=$(docker inspect -f '{{.HostConfig.RestartPolicy.Name}}' "$c" 2>/dev/null)
    printf '  %-24s 运行中  (restart=%s)\n' "$c" "$pol"
    targets+=("$c")
  else
    printf '  %-24s 未运行，跳过\n' "$c"
  fi
done

if [ "${#targets[@]}" -eq 0 ]; then
  echo "  没有需要停止的容器。"
  exit 0
fi

echo
if [ "$dry_run" = 1 ]; then
  echo "  [dry-run] 将停止 ${#targets[@]} 个容器：${targets[*]}"
  exit 0
fi

echo "═══ 停止中（逆依赖顺序，每个超时 30s）═══"
failed=0
for c in "${targets[@]}"; do
  if docker stop -t 30 "$c" >/dev/null 2>&1; then
    printf '  %-24s 已停止\n' "$c"
  else
    printf '  %-24s 停止失败\n' "$c" >&2
    failed=1
  fi
done

echo
echo "═══ 停止后状态 ═══"
docker ps -a --format '{{.Names}}\t{{.Status}}' | while IFS=$'\t' read -r n s; do
  printf '  %-24s %s\n' "$n" "$s"
done

echo
if [ "$failed" = 1 ]; then
  echo "⚠️  有容器未能停止，请检查上面输出。" >&2
  exit 1
fi
echo "全部目标容器已停止。恢复：scripts/start-all-containers.sh"
