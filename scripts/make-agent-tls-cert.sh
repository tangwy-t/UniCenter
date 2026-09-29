#!/usr/bin/env bash
# ============================================================================
# 生成 agent 通道的 wss/TLS 内部 CA 与**服务端证书**（离线环境，内网没有 CA 可签发）
#
# ── 为什么是「CA + 服务端证书」两件套，而不是一张自签证书 ──────────────
#
# 第一版用 `openssl req -x509` 直接签了一张自签证书，结果 curl 侧报
#   curl: (60) Certificate type not approved for application
# 原因：`-x509` 默认给证书带上 `basicConstraints=CA:TRUE` —— 它是一张 **CA 证书**，
# 被当作服务端证书使用时会被 OpenSSL 3.x 的用途检查（X509_check_purpose）拒掉。
# 标准做法是签一张真正的 CA（CA:TRUE）再用它签一张**服务端证书**（CA:FALSE +
# EKU serverAuth + IP SAN）：
#   - nginx 用服务端证书（uni-center.crt/.key）；
#   - 每台 agent 只信 CA（ca.crt）—— 于是将来换服务端证书不必再分发到 agent。
#
# SAN 里**必须**含 console 主机的 IP（默认 192.168.12.105）：agent 用
#   wss://192.168.12.105:20443/api/v1/agent/ws
# 连接，证书没有对应 IP SAN 时 Go 会以
#   x509: cannot validate certificate for 192.168.12.105 ...
# 拒绝 —— 只有 DNS SAN 是不够的。
#
# 产物（**不进仓库**，根 .gitignore 已忽略 uni_console/certs/）：
#   uni_console/certs/uni-center-ca.crt   内部 CA 证书（分发给每台 agent 作信任锚）
#   uni_console/certs/uni-center-ca.key   CA 私钥（**只留在签发机上**，不部署）
#   uni_console/certs/uni-center.crt      服务端证书（nginx 用；含 fullchain 便于 curl）
#   uni_console/certs/uni-center.key      服务端私钥（600，只放 console 主机）
#
# 用法：
#   scripts/make-agent-tls-cert.sh           # 已存在时报错，不覆盖
#   scripts/make-agent-tls-cert.sh --force   # 重签（重签后必须把新的 ca.crt
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
    -h|--help) sed -n '2,40p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
    *) echo "未知参数: $arg（见 --help）" >&2; exit 2 ;;
  esac
done

if [[ -f "$CERT_DIR/uni-center-ca.crt" && $FORCE -ne 1 ]]; then
  echo "证书已存在：$CERT_DIR/uni-center-ca.crt" >&2
  echo "如需重签请加 --force；重签后必须把新的 ca.crt 分发到每台 agent，否则它们的信任链会对不上。" >&2
  exit 1
fi

mkdir -p "$CERT_DIR"
# 私钥从落盘那一刻起就不给同机其他用户读（openssl 按 umask 建文件）。
umask 077

# ── 1. 内部 CA（CA:TRUE，只用来签服务端证书；不部署到任何主机）─────────────
openssl req -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
  -keyout "$CERT_DIR/uni-center-ca.key" -out "$CERT_DIR/uni-center-ca.crt" \
  -subj "/CN=uni-center-internal-ca" \
  -addext "basicConstraints=critical,CA:TRUE,pathlen:0" \
  -addext "keyUsage=critical,keyCertSign,cRLSign"

# ── 2. 服务端证书（CA:FALSE + serverAuth + IP SAN）─────────────────────────
openssl req -newkey rsa:2048 -sha256 -nodes \
  -keyout "$CERT_DIR/uni-center.key" -out "$CERT_DIR/uni-center.csr" \
  -subj "/CN=uni-center-agent"

cat > "$CERT_DIR/server-ext.cnf" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature,keyEncipherment
extendedKeyUsage=serverAuth
subjectAltName=IP:${IP},DNS:bogon,DNS:uni-center
EOF

openssl x509 -req -in "$CERT_DIR/uni-center.csr" -sha256 -days 3650 \
  -CA "$CERT_DIR/uni-center-ca.crt" -CAkey "$CERT_DIR/uni-center-ca.key" -CAcreateserial \
  -extfile "$CERT_DIR/server-ext.cnf" -out "$CERT_DIR/uni-center.crt"

chmod 600 "$CERT_DIR/uni-center-ca.key" "$CERT_DIR/uni-center.key"
chmod 644 "$CERT_DIR/uni-center-ca.crt" "$CERT_DIR/uni-center.crt"
rm -f "$CERT_DIR/uni-center.csr" "$CERT_DIR/server-ext.cnf" "$CERT_DIR/uni-center-ca.srl"

# 生成后立刻回读事实：CA 与用途是 curl/Go 两边的判据，SAN 是 agent 侧的判据。
echo "== CA（应 CA:TRUE）=="
openssl x509 -in "$CERT_DIR/uni-center-ca.crt" -noout -text | grep -A1 "Basic Constraints" | tail -1
echo "== 服务端证书（应 CA:FALSE + 含 serverAuth 与 IP SAN）=="
openssl x509 -in "$CERT_DIR/uni-center.crt" -noout -text \
  | grep -A1 "Basic Constraints" | tail -1
openssl x509 -in "$CERT_DIR/uni-center.crt" -noout -text \
  | grep -A1 "Extended Key Usage" | tail -1
openssl x509 -in "$CERT_DIR/uni-center.crt" -noout -text \
  | grep -A1 "Subject Alternative Name" | tail -1
echo "== 完成：$(ls "$CERT_DIR" | tr '\n' ' ')=="