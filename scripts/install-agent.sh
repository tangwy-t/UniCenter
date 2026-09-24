#!/usr/bin/env bash
# 安装 / 升级 uni_agent 到本机（systemd 形态）。
#
# 用法：
#   scripts/install-agent.sh -e <enroll 令牌> [-u ws://host:8088/api/v1/agent/ws]
#   scripts/install-agent.sh -p <离线 tar 包> -e <enroll 令牌> [-u ...]
#
#   -e  enroll 令牌（必填；在控制台「系统配置」里取 sys.agent.enrollToken）
#   -u  上报地址（缺省 ws://127.0.0.1:8088/api/v1/agent/ws）
#   -p  离线包（内含 uni_agent 二进制；不给则用 uni_agent/bin/uni_agent 或 PATH 里的）
#   -y  跳过确认（无人值守；默认会在覆盖已有单元前问一次）
#
# ── 为什么需要它（而不是「拷个二进制 + 手写 unit」）──────────────────
#
# 1. **第一次必须手工**：设备上跑的旧版 agent 没有任何升级能力，所以它不可能
#    被远程升级到「支持升级」的版本 —— 引导安装只能发生在本机一次。之后所有
#    升级/回滚都走控制台。
# 2. **已有单元的冲突检测**：现场可能已经有一份手工写的 unit 在跑同一个 agent。
#    直接再装一份会变成两个进程抢同一个状态目录（instance_id 相同 → core 侧
#    「顶号」互相踢，现象是设备反复上下线）。故这里**先检测再问**。
# 3. **不写 -version**：unit 里写死版本会让自升级后的进程自称旧版本
#    （见 uni_agent/deploy/uni-agent.service 的注释）。
set -euo pipefail

UNIT_NAME="uni-agent"
UNIT_PATH="/etc/systemd/system/${UNIT_NAME}.service"
ENV_PATH="/etc/default/uni-agent"
BIN_PATH="/usr/local/bin/uni_agent"
STATE_DIR="/var/lib/uni_agent"

ENROLL_TOKEN=""
AGENT_URL="ws://127.0.0.1:8088/api/v1/agent/ws"
PACKAGE=""
ASSUME_YES=0

usage() {
  sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
  exit "${1:-0}"
}

while getopts ":e:u:p:yh" opt; do
  case "$opt" in
    e) ENROLL_TOKEN="$OPTARG" ;;
    u) AGENT_URL="$OPTARG" ;;
    p) PACKAGE="$OPTARG" ;;
    y) ASSUME_YES=1 ;;
    h) usage 0 ;;
    *) usage 1 ;;
  esac
done

if [[ -z "$ENROLL_TOKEN" ]]; then
  echo "错误：缺少 enroll 令牌（-e）。在控制台「系统配置」里取 sys.agent.enrollToken。" >&2
  exit 1
fi

if [[ "$(id -u)" -ne 0 ]]; then
  echo "错误：需要 root（要写 ${UNIT_PATH} 与 ${BIN_PATH}）。" >&2
  exit 1
fi

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ── 1. 取二进制（三种来源，优先级：离线包 > 本仓库构建产物）──────────────
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

if [[ -n "$PACKAGE" ]]; then
  echo "==> 解包 ${PACKAGE}"
  tar -xzf "$PACKAGE" -C "$tmpdir"
  src_bin="$(find "$tmpdir" -type f -name 'uni_agent*' ! -name '*.sha256' | head -1)"
elif [[ -x "${repo_root}/uni_agent/bin/uni_agent" ]]; then
  src_bin="${repo_root}/uni_agent/bin/uni_agent"
  echo "==> 使用本仓库构建产物 ${src_bin}"
else
  echo "错误：找不到 agent 二进制。先跑 'make uni_agent-build'（或 'make uni_agent-release'），" >&2
  echo "      或用 -p 指定离线包。" >&2
  exit 1
fi
[[ -n "${src_bin:-}" && -f "$src_bin" ]] || { echo "错误：包里没有找到 uni_agent 二进制" >&2; exit 1; }

# ── 2. 已有单元检测（G4：两个进程抢同一个状态目录会互相顶号）─────────────
if systemctl list-unit-files 2>/dev/null | grep -q "^${UNIT_NAME}\.service"; then
  echo "==> 检测到已有单元 ${UNIT_NAME}.service（本次将覆盖它的定义并重启）"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "    继续？[y/N] " ans
    [[ "$ans" =~ ^[Yy]$ ]] || { echo "    已取消。"; exit 0; }
  fi
elif pgrep -x uni_agent >/dev/null 2>&1; then
  # 单元不叫这个名字但进程已经在跑：可能是手工启动的，也可能是另一个 unit。
  echo "==> 警告：检测到正在运行的 uni_agent 进程（不是由 ${UNIT_NAME} 管理的）"
  echo "    两个进程共用 ${STATE_DIR} 会互相顶号（设备反复上下线）。"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "    仍然继续？[y/N] " ans
    [[ "$ans" =~ ^[Yy]$ ]] || { echo "    已取消。"; exit 0; }
  fi
fi

# ── 3. 安装 ──────────────────────────────────────────────────────────────
echo "==> 安装二进制到 ${BIN_PATH}"
install -m 0755 "$src_bin" "$BIN_PATH"

echo "==> 写入配置 ${ENV_PATH}"
mkdir -p "$(dirname "$ENV_PATH")"
cat >"$ENV_PATH" <<EOF
# uni_agent 的启动配置（由 scripts/install-agent.sh 生成）
UNI_AGENT_URL=${AGENT_URL}
UNI_AGENT_ENROLL_TOKEN=${ENROLL_TOKEN}
UNI_AGENT_STATE_DIR=${STATE_DIR}
# 不要在这里写版本：版本只有一个来源（编译期注入）。见 deploy/uni-agent.service。
EOF
chmod 0600 "$ENV_PATH"   # 含 enroll 令牌，收紧权限

echo "==> 安装 systemd 单元到 ${UNIT_PATH}"
install -m 0644 "${repo_root}/uni_agent/deploy/uni-agent.service" "$UNIT_PATH"

# 状态目录：升级事务与凭据都在这里，必须存在且可写。
mkdir -p "$STATE_DIR"
chmod 0755 "$STATE_DIR"

echo "==> 启动服务"
systemctl daemon-reload
systemctl enable --now "${UNIT_NAME}"
sleep 1
systemctl --no-pager --lines=0 status "${UNIT_NAME}" || true

cat <<EOF

==> 完成。后续检查：
    日志：   journalctl -u ${UNIT_NAME} -f
    自检：   ${BIN_PATH} -self-check     # 打印当前二进制自述的版本
    状态目录：${STATE_DIR}（含 instance_id / agent_token / upgrade.state）

    之后所有升级与回滚都在控制台「设备管理 → Agent 版本 / 升级任务」里做 ——
    本脚本只在**第一台**（以及自愈失败需要人工介入时）用得上。
EOF