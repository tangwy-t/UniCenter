#!/usr/bin/env bash
# ============================================================================
# 脱工作区可构建门禁（发布态）
#
# 「发布态」= uni_core/go.mod 自带 require + go.sum 有真校验和，
# 不再依赖仓库根的 go.work。本脚本断言这一点确实成立。
#
# 逻辑写在脚本里而不是 Makefile recipe 里，原因（实测）：
#   - make recipe 用 `\` 续行 + `@` 前缀，行内插入注释或含括号的 echo 会破坏
#     续行链，报 `未预期的记号 "(" 附近有语法错误`；
#   - 脚本可用 `set -euo pipefail` 让失败如实冒泡，而 recipe 里 `&&` 链的
#     左操作数会被 `set -e` 豁免、末尾 echo 又会吞掉退出码（假绿）。
#
# 两个关键点（均由实测确立，勿凭直觉改）：
#   1) 必须 GOWORK=off。`go env GOWORK` 是按**文件名**沿祖先链查找的，副本即便
#      建在仓库内的 .tmp-iso/ 也仍会命中根 go.work；此时 go build 报的是
#      **模块归属**错误（directory prefix . does not contain modules listed in
#      go.work），与 require/go.sum 无关 —— 门禁会在「解析路径根本没被检验」
#      的情况下变绿变红皆失真，成为假阳性。
#   2) GOPROXY 指向仓库内的离线载荷 uni_core/.goproxy（仅含 uni_protocol 一个
#      模块，约 72K），故本门禁**不需要网络**。该载荷是「已发布模块」的本地
#      镜像；远端 tag 就绪后可整体删除、改指向真实 proxy。
# ============================================================================
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ISO="$REPO_ROOT/.tmp-iso"
MOD="github.com/tangwy-t/UniCenter/uni_protocol"

cleanup() { rm -rf "$ISO"; }
trap cleanup EXIT

echo "── 脱工作区可构建（发布态：自带 require + 离线载荷 + GOWORK=off）──"

# 用绝对路径：脚本中途会 cd 进副本，相对路径的清理会失效并留下残留。
rm -rf "$ISO"
mkdir -p "$ISO"
cp -r "$REPO_ROOT/uni_core" "$ISO/uni_core"
rm -rf "$ISO/uni_core/.gocache"

# ── 断言 1：go.mod 必须自带 require，且不得含 replace ──────────────────────
# 发布态不容忍本地相对路径（replace）——那会让制品绑定本机目录布局。
if ! grep -q "^require $MOD " "$ISO/uni_core/go.mod"; then
  echo "FAIL: uni_core/go.mod 缺少 'require $MOD'"
  echo "      → 不是发布态（仍只能靠 go.work 解析，脱工作区必然构建失败）"
  exit 1
fi
if grep -q "replace $MOD" "$ISO/uni_core/go.mod"; then
  echo "FAIL: uni_core/go.mod 含 'replace $MOD'"
  echo "      → 制品绑定本机路径，不是发布态"
  exit 1
fi

# ─ 断言 2：go.sum 必须有该模块的校验和 ────────────────────────────────────
if ! grep -q "^$MOD " "$ISO/uni_core/go.sum"; then
  echo "FAIL: uni_core/go.sum 缺少 $MOD 的校验和 → 离线解析会失败"
  exit 1
fi

# ─ 断言 3：离线载荷存在 ──────────────────────────────────────────────────
# 实测教训（两条）：
#  a) **只断言「能构建」不够**：本地 GOMODCACHE 里若已有该模块，Go 根本不会走
#     GOPROXY，载荷缺了照样构建成功，断言就成了空转（我实测到过 exit=0）。
#  b) 在 `set -euo pipefail` 下，`x="$(find 不存在的目录 | wc -l)"` 会因 find
#     退出码 1 被 pipefail 传播、**赋值即终止脚本** —— 方向虽对（确实红了）却不带
#     任何诊断。故这里用 `|| true` 吞掉 find 的非零，再由下面的分支显式报错。
payload_zips="$(find "$ISO/uni_core/.goproxy" -name '*.zip' 2>/dev/null | wc -l | tr -d ' ' || true)"
payload_zips="${payload_zips:-0}"
if [ "$payload_zips" -lt 1 ]; then
  echo "FAIL: 离线载荷 uni_core/.goproxy 缺失（找不到任何 .zip）"
  echo "      → 本门禁不联网；module cache 为空时无法解析该模块"
  exit 1
fi
echo "离线载荷：$payload_zips 个模块 zip"

# ── 断言 4：隔离确实生效（GOWORK=off）─────────────────────────────────────
gw="$(cd "$ISO/uni_core" && GOWORK=off go env GOWORK)"
echo "隔离副本 go env GOWORK=$gw（必须为 off；否则副本被根 go.work 接管、门禁无效）"
if [ "$gw" != "off" ]; then
  echo "FAIL: GOWORK 未关闭 → 门禁无效，拒绝放行"
  exit 1
fi

# ── 断言 4：纯离线构建 + 测试 ─────────────────────────────────────────────
# GOPROXY 只给仓库内载荷，并显式以 off 兜底 —— 任何需要联网的情形都会失败。
cd "$ISO/uni_core"
GOWORK=off GOSUMDB=off GOFLAGS=-mod=mod GOPROXY="file://$ISO/uni_core/.goproxy,off" go build ./...
GOWORK=off GOSUMDB=off GOFLAGS=-mod=mod GOPROXY="file://$ISO/uni_core/.goproxy,off" go test ./... -count=1

echo "uni_core 脱工作区可构建（发布态：自带 require + 离线载荷）✓"