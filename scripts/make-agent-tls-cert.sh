#!/usr/bin/env bash
# ============================================================================
# 生成 agent 通道的 wss/TLS 自签证书（离线环境，内网没有 CA 可签发）
#
# 为什么必须自签：agent 侧不提供任何「跳过校验」的选项，唯一的信任来源
# 就是这里生成的证书本体 —— 部署时把它作为 -ca-file / UNI_AGENT_CA_FILE
# 分发给每台 agent 主机；console 侧的 nginx 用同一对 crt/key 做 TLS 终端。
#
# SAN 里**必须**含 console 主机的 IP（默认 192.168.12.105）：agent 用
#   wss://192.168.12.105:20443/api/v1/agent/ws
# 连接，证书没有对应 IP SAN 时 Go 会以
#   x509: cannot validate certificate for 192.168.12.105 ...
# 拒绝 —— 只有 DNS SAN 是不够的。
#
# 产物（**不进仓库**，根 .gitignore 已忽略 uni_console/certs/）：
#   uni_console/certs/uni-center.crt   证书（也分发给两台 agent 作 CA）
#   uni_console/certs/uni-center.key   私钥（权限 600，只放 console 主机）
#
# 用法：
#   scripts/make-agent-tls-cert.sh           # 证书已存在时报错，不覆盖
#   scripts/make-agent-tls-cert.sh --force   # 重签（已部署后重签必须把新 crt
#                                            #   重新分发到两台 agent，否则它们拒连）
#
# 环境变量：
#   AGENT_TLS_IP  证书 SAN 里的 IP（默认 192.168.12.105，即 console 部署机）
#   CERT_DIR      产物目录（默认 uni_console/certs）
# ============================================================================
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root"

CERT_DIR="${CERT_DIR:-uni_console/certs}"
IP="${AGENT_TLS_IP:-192.168.12.105}"
FORCE=0

for arg in "$@"; do
  case "$arg" in
    --force) FORCE=1 ;;
    -h|--help) sed -n '2,30p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $arg（见 --help）" >&2; exit 2 ;;
  esac
done

if [[ -f "$CERT_DIR/uni-center.crt" && $FORCE -ne 1 ]]; then
  echo "证书已存在：$CERT_DIR/uni-center.crt" >&2
  echo "如需重签请加 --force；重签后必须把新 crt 分发到每台 agent，否则它们的信任链会对不上。" >&2
  exit 1
fi

mkdir -p "$CERT_DIR"
# 私钥从落盘那一刻起就不给同机其他用户读（openssl 按 umask 建文件）。
umask 077

openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
  -keyout "$CERT_DIR/uni-center.key" -out "$CERT_DIR/uni-center.crt" \
  -subj "/CN=uni-center-agent" \
  -addext "subjectAltName=IP:${IP},DNS:bogon,DNS:uni-center" \
  -addext "keyUsage=digitalSignature,keyEncipherment" \
  -addext "extendedKeyUsage=serverAuth"

chmod 600 "$CERT_DIR/uni-center.key"
chmod 644 "$CERT_DIR/uni-center.crt"

# 生成后立刻回读 SAN：缺 IP SAN 的证书在 agent 侧表现为「连不上」，
# 这行输出就是「证书到底签了什么」的直接证据（部署前先看它）。
echo "== 证书 SAN（必须含 IP Address:${IP}）=="
openssl x509 -in "$CERT_DIR/uni-center.crt" -noout -text | grep -A1 "Subject Alternative Name"
echo "== 完成：$CERT_DIR/uni-center.crt / $CERT_DIR/uni-center.key =="