#!/usr/bin/env python3
# ============================================================================
# 协议离线载荷工具（uni_core/.goproxy）
#
# ## 为什么必须有「check」这一半（一次静默型生产故障，2026-10-13）
#
# uni_core 的 Docker 构建**不走工作区**：uni_core/go.mod 是
#   `require github.com/tangwy-t/UniCenter/uni_protocol vX.Y.Z`
# 而 Dockerfile 里 GOPROXY 的第一顺位指向仓库内的离线载荷
#   uni_core/.goproxy/...
# 也就是说：**镜像里编进去的协议 = 载荷里的那个 zip，而不是工作区
# uni_protocol/ 的当前源码**。core 只调协议里的 `ValidateDockerCmdOptions`，
# 不直接引用新符号 —— 于是载荷陈旧时照样编译通过，行为却是旧的
# （现场表现：「载入镜像」传 `.tar.gz` 文件名被 core 以 400「指令参数不合法」
# 打回，而请求体完全合法），**所有构建门禁全绿也发现不了**。
#
# 这份脚本的 `check` 就是补上这个缺口：断言「载荷里最新版本的 zip 内容」与
# 「当前 uni_protocol 工作树源码」逐文件一致；不一致即非零退出，并给出可执行
# 的重烘步骤。`bake` 则把工作树烘成载荷的下一个补丁版本，两者共用同一套文件集
# 口径，避免口径漂移再制造同类静默故障。
#
# ## 载荷布局（历史版本不可变，只追加）
#
#   uni_core/.goproxy/github.com/tangwy-t/!uni!center/uni_protocol/@v/
#     <v>.info   {"Version": "<v>", "Time": "<RFC3339 UTC>"}（照 Go proxy 约定）
#     <v>.mod    = uni_protocol/go.mod 原文
#     <v>.zip    内部前缀 github.com/tangwy-t/UniCenter/uni_protocol@<v>/
#     list       每行一个已发布版本（升序）
#
# ## 用法
#
#   protocol-payload.py check    守卫（默认）：载荷最新版本 vs 工作树，逐文件 sha256
#   protocol-payload.py bake     重烘：把当前工作树烘成载荷的下一个补丁版本
#
# ## 取舍
#
# 比对口径：只逐文件比 sha256（按**相对路径**对齐）。zip 的整体字节、条目顺序、
# 时间戳一律不比 —— Go 的 dirhash 也只按「排序后的文件名 + 内容」算哈希，这些
# 元数据对 `go build` 完全无意义，比了只会假红。
#
# 文件集口径：用 `git ls-files --cached --others --exclude-standard`，即「已跟踪
# + 未被忽略的新增」。这既是「工作树源码」（而非磁盘上的一切），又自动排除
# .git/.mimosa 等忽略物 —— 实测该口径与历史载荷 v1.2.5 的 49 个条目完全一致。
#
# 为什么用 Python 而非 Makefile 里的 zip/Go：zip 的条目前缀、条目顺序、固定时间
# 戳与权限位必须逐字控制（bash 的 zip CLI 做不到），Python 标准库 zipfile 可精确
# 控制且零额外依赖。
# ============================================================================
import argparse
import hashlib
import json
import os
import re
import stat
import subprocess
import sys
import zipfile
from datetime import datetime, timezone
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parent.parent
PROTO_DIR = "uni_protocol"
MODULE = "github.com/tangwy-t/UniCenter/uni_protocol"
# Go proxy 的模块路径转义：大写字母 → '!' + 小写（UniCenter → !uni!center）。
PAYLOAD_REL = "uni_core/.goproxy/github.com/tangwy-t/!uni!center/uni_protocol/@v"
BAKE_HINT = """      → 协议已改，需重烘载荷并升版：
        1) python3 scripts/protocol-payload.py bake        # 烘出下一个补丁版本
        2) 把 uni_core/go.mod 与 uni_agent/go.mod 的 require 升到新版本并刷新 go.sum
        3) make release-precheck                           # 复验发布态可构建"""


def die(msg: str, code: int = 1) -> None:
    print(msg, file=sys.stderr)
    sys.exit(code)


# ── 文件集口径（bake 与 check 共用，务必只有这一处定义）────────────────────
def worktree_files() -> list[str]:
    """工作树里的协议模块文件集，返回相对 uni_protocol/ 的路径（已排序）。"""
    out = subprocess.run(
        ["git", "-C", str(REPO_ROOT), "ls-files", "--cached", "--others",
         "--exclude-standard", "-z", "--", PROTO_DIR],
        check=True, capture_output=True,
    ).stdout
    prefix = PROTO_DIR + "/"
    rels = [raw.decode() for raw in out.split(b"\0") if raw]
    return sorted(p[len(prefix):] for p in rels if p.startswith(prefix))


def sha256_file(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def worktree_hashes() -> dict[str, str | None]:
    """相对路径 → sha256；文件在工作树中缺失时记 None（同样算「不一致」）。"""
    res: dict[str, str | None] = {}
    for rel in worktree_files():
        p = REPO_ROOT / PROTO_DIR / rel
        res[rel] = sha256_file(p) if p.is_file() else None
    return res


# ── 载荷读取 ────────────────────────────────────────────────────────────────
def verkey(v: str) -> tuple[int, int, int]:
    m = re.fullmatch(r"v(\d+)\.(\d+)\.(\d+)", v)
    if not m:
        die(f"FAIL: 载荷版本号非法：{v!r}（期望 vX.Y.Z）")
    return (int(m.group(1)), int(m.group(2)), int(m.group(3)))


def payload_versions(payload_dir: Path) -> list[str]:
    list_file = payload_dir / "list"
    if not list_file.is_file():
        die(f"FAIL: 载荷缺失 {list_file}（离线载荷未烘焙？）")
    return sorted({line.strip() for line in list_file.read_text().splitlines() if line.strip()}, key=verkey)


def latest_version(payload_dir: Path) -> str:
    versions = payload_versions(payload_dir)
    if not versions:
        die(f"FAIL: 载荷 {payload_dir}/list 为空")
    return versions[-1]


def zip_hashes(zip_path: Path, version: str) -> dict[str, str]:
    prefix = f"{MODULE}@{version}/"
    res: dict[str, str] = {}
    with zipfile.ZipFile(zip_path) as z:
        for name in z.namelist():
            if name.endswith("/"):
                continue
            if not name.startswith(prefix):
                die(f"FAIL: 载荷条目前缀异常（应为 {prefix}）：{name}")
            res[name[len(prefix):]] = hashlib.sha256(z.read(name)).hexdigest()
    return res


# ── check：守卫 ─────────────────────────────────────────────────────────────
def cmd_check(args: argparse.Namespace) -> int:
    payload_dir = REPO_ROOT / PAYLOAD_REL
    version = latest_version(payload_dir)
    zip_path = payload_dir / f"{version}.zip"
    if not zip_path.is_file():
        die(f"FAIL: 载荷最新版本 {version} 的 zip 缺失：{zip_path}")

    print(f"── 协议载荷守卫：载荷最新版本 {version} vs 工作树 {PROTO_DIR}/ ──", flush=True)
    payload = zip_hashes(zip_path, version)
    worktree = worktree_hashes()

    only_payload = sorted(set(payload) - set(worktree))
    only_worktree = sorted(set(worktree) - set(payload))
    content_diff = sorted(r for r in set(payload) & set(worktree) if payload[r] != worktree[r])

    if not (only_payload or only_worktree or content_diff):
        print(f"载荷 {version}.zip 与 {PROTO_DIR}/ 工作树一致（{len(payload)} 个文件，逐文件 sha256）✓")
        return 0

    print(f"FAIL: 离线载荷 {version} 与 {PROTO_DIR} 工作树**不一致** —— 载荷陈旧。", file=sys.stderr)
    if content_diff:
        print(f"  内容不同（{len(content_diff)}）：" + ", ".join(content_diff), file=sys.stderr)
    if only_payload:
        print(f"  仅载荷有（工作树已删，{len(only_payload)}）：" + ", ".join(only_payload), file=sys.stderr)
    if only_worktree:
        print(f"  仅工作树有（载荷缺失，{len(only_worktree)}）：" + ", ".join(only_worktree), file=sys.stderr)
    print(BAKE_HINT, file=sys.stderr)
    return 1


# ── bake：重烘 ──────────────────────────────────────────────────────────────
def cmd_bake(args: argparse.Namespace) -> int:
    payload_dir = REPO_ROOT / PAYLOAD_REL
    if args.version:
        version = args.version
        verkey(version)  # 校验形态
        if version in payload_versions(payload_dir):
            die(f"FAIL: 载荷已存在 {version}（历史版本不可变，只追加；请换个版本号）")
    else:
        major, minor, patch = verkey(latest_version(payload_dir))
        version = f"v{major}.{minor}.{patch + 1}"

    now = datetime.now(timezone.utc)
    # .info 的 Time 照历史惯例取当日 00:00:00Z（RFC3339 UTC，Go proxy 约定）。
    info = json.dumps({"Version": version, "Time": now.strftime("%Y-%m-%dT00:00:00Z")})
    (payload_dir / f"{version}.info").write_text(info + "\n")
    # .mod 必须与 uni_protocol/go.mod 逐字节相同。
    (payload_dir / f"{version}.mod").write_bytes((REPO_ROOT / PROTO_DIR / "go.mod").read_bytes())

    # zip：条目前缀 module@version/，顺序按相对路径排序（与 dirhash 排序口径一致），
    # 权限位沿用源文件 st_mode（含 S_IFREG，与历史载荷同款），时间戳取当日 00:00:00。
    date_time = (now.year, now.month, now.day, 0, 0, 0)
    prefix = f"{MODULE}@{version}/"
    files = worktree_files()
    with zipfile.ZipFile(payload_dir / f"{version}.zip", "w",
                         zipfile.ZIP_DEFLATED, compresslevel=9) as z:
        for rel in files:
            src = REPO_ROOT / PROTO_DIR / rel
            zi = zipfile.ZipInfo(prefix + rel, date_time=date_time)
            zi.compress_type = zipfile.ZIP_DEFLATED
            zi.create_system = 3  # unix
            zi.external_attr = os.stat(src).st_mode << 16
            z.writestr(zi, src.read_bytes())

    versions = sorted(set(payload_versions(payload_dir)) | {version}, key=verkey)
    (payload_dir / "list").write_text("".join(v + "\n" for v in versions))

    print(f"已烘出载荷 {version}：{version}.info / {version}.mod / {version}.zip（{len(files)} 个文件）+ list")
    print(f"下一步：把 uni_core/go.mod 与 uni_agent/go.mod 的 require 升到 {version} 并刷新 go.sum")
    return 0


def main() -> int:
    parser = argparse.ArgumentParser(description="协议离线载荷工具（uni_core/.goproxy）")
    sub = parser.add_subparsers(dest="cmd")
    sub.add_parser("check", help="守卫：载荷最新版本 vs 协议工作树（默认）")
    bake = sub.add_parser("bake", help="重烘：把工作树烘成载荷的下一个补丁版本")
    bake.add_argument("--version", help="显式指定版本（缺省=最新版本的下一个补丁）")
    args = parser.parse_args()
    if args.cmd == "bake":
        return cmd_bake(args)
    return cmd_check(args)


if __name__ == "__main__":
    sys.exit(main())