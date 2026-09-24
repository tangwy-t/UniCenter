# UniCenter 发布手册（RELEASING.md）

> 本手册由 Plan 5（`uni_core` 发布前置）产出。它把「把 `uni_protocol` 发出去、让 `uni_core`
> 按发布形态依赖它」这件事**严格拆成三类**，**请不要混用**：
>
> | 类 | 含义 | 现在的状态 |
> |---|---|---|
> | **(A)** | 已固化、可执行、**本环境已实测验证** | 可以直接跑，输出见 §1 |
> | **(B)** | **必须等远端就绪**才能做 | 现在做**必然失败**，失败原文见 §2 |
> | **(C)** | 已知风险，发布时必须一并处理 | 见 §3 |
>
> **读这份手册只记住一件事**：(A) 类**不等于**发布完成。真正的发布是 (B) 类，
> 而 (B) 类**在这一整套环境里从头到尾没有成功过一次**。

---

## 0. 总声明：真发布形态未在本环境验证过

**「真发布形态」的定义**：`uni_core/go.mod` 里出现
`require github.com/tangwy-t/UniCenter/uni_protocol vX.Y.Z`，配套 `go.sum` 有对应条目，
**且构建时不再借助 `go.work`**（即 `GOWORK=off`）。

**本环境从未成功达成过这个形态。** 下面是把「发布形态所需的 `require` 行」真的写进
`go.mod`、然后脱工作区构建的结果——**实测原文，不是预测**：

```
$ (隔离副本 uni_core，go.mod 追加 require github.com/tangwy-t/UniCenter/uni_protocol v1.0.0，无 replace)
$ GOWORK=off go build ./...
internal/pkg/agentmetrics/downsample.go:6:2: missing go.sum entry for module providing package github.com/tangwy-t/UniCenter/uni_protocol (imported by github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub); to add:
	go get github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub
exit=1

$ GOFLAGS=-mod=mod GOWORK=off go build ./...      # 让 go 自己去解析/下载
go: downloading github.com/tangwy-t/UniCenter/uni_protocol v1.0.0
internal/pkg/agentmetrics/downsample.go:6:2: reading github.com/tangwy-t/UniCenter/uni_protocol/go.mod at revision uni_protocol/v1.0.0: git ls-remote -q --end-of-options origin in /home/workspace/UniCenter/.gomodcache/cache/vcs/ff8ae50511509b65bdcadaa86b600a550ab544cbc92608621b3eb0ec1edfe1b1: exit status 128:
	fatal: could not read Username for 'https://github.com': terminal prompts disabled
Confirm the import path was entered correctly.
If this is a private repository, see https://golang.org/doc/faq#git_https for additional information.
exit=1
```

**结论**：`github.com/tangwy-t/UniCenter/uni_protocol` 这个模块路径在当前环境**不可 fetch**
（远端不存在该仓库，或不可匿名访问）。因此**「发布形态能否构建」这件事至今没有被验证过一次**，
本仓库也**没有任何门禁**真的检验过它——只有 §1.2 那个**代理**。

同一失败的另一种触发方式（`go` 连「该模块由谁提供」都找不到，原文照录）：

```
go: finding module for package github.com/tangwy-t/UniCenter/uni_protocol
internal/pkg/agentmetrics/downsample.go:6:2: cannot find module providing package github.com/tangwy-t/UniCenter/uni_protocol: module github.com/tangwy-t/UniCenter/uni_protocol: git ls-remote -q --end-of-options origin in /home/workspace/UniCenter/.gomodcache/cache/vcs/ff8ae50511509b65bdcadaa86b600a550ab544cbc92608621b3eb0ec1edfe1b1: exit status 128:
	fatal: could not read Username for 'https://github.com': terminal prompts disabled
Confirm the import path was entered correctly.
If this is a private repository, see https://github.com/faq#git_https for additional information.
```

---

## 1. (A) 已固化、可执行、本环境已实测验证

以下每条命令都在本环境**真跑过**，输出为实测节选，并附退出码。

### 1.1 契约门禁：`make uni_protocol-contract`

守的是协议模块的**形状漂移、单源双向映射、未知字段容忍、golden 回环、零第三方依赖**。

```
$ make uni_protocol-contract
uni_protocol 零第三方依赖 ✓
cd uni_protocol && go test ./... -count=1 -v -run 'TestJSONTagConventions|TestUnknownFieldsAreTolerated|TestShapeDriftAdditiveOnly|TestGoldenFilesRoundTrip|TestRegistryIsCompleteAndConsistent|TestVersionsAreConsistent|TestCloseCodesAreExhaustivelyMapped|TestSnapshotDTORegistryCoversAllPayloads'
--- PASS: TestVersionsAreConsistent (0.00s)
--- PASS: TestCloseCodesAreExhaustivelyMapped (0.00s)
--- PASS: TestSnapshotDTORegistryCoversAllPayloads (0.00s)
--- PASS: TestShapeDriftAdditiveOnly (0.00s)
--- PASS: TestRegistryIsCompleteAndConsistent (0.00s)
--- PASS: TestJSONTagConventions (0.00s)
--- PASS: TestUnknownFieldsAreTolerated (0.00s)
--- PASS: TestGoldenFilesRoundTrip (0.00s)
    --- PASS: TestGoldenFilesRoundTrip/agent.heartbeat.json (0.00s)
    --- PASS: TestGoldenFilesRoundTrip/agent.hello.json (0.00s)
    --- PASS: TestGoldenFilesRoundTrip/agent.report.metrics.full.json (0.00s)
    --- PASS: TestGoldenFilesRoundTrip/agent.report.metrics.minimal.json (0.00s)
    --- PASS: TestGoldenFilesRoundTrip/core.hello_ack.json (0.00s)
PASS
ok  	github.com/tangwy-t/UniCenter/uni_protocol	0.007s
exit=0
```

### 1.2 脱工作区可构建：`make uni_protocol-release-precheck`（**是代理，不是发布形态**）

```
$ make uni_protocol-release-precheck
── 脱工作区可构建代理(隔离副本 + 本地 replace + GOWORK=off)──
隔离副本 go env GOWORK=off(必须为 off;否则副本被根 go.work 接管、代理无效)
?   	github.com/tangwy-t/UniCenter/uni_core/cmd/server	[no test files]
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/handler	2.330s
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub	0.894s
...
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/service	5.875s
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/wireup	2.140s
uni_core 脱工作区可构建(隔离副本 + 本地 replace)✓
exit=0
```

> ⚠️ **这条最容易被误读，必须写清楚：**
>
> - 该目标**自己往隔离副本的 `go.mod` 追加了 `require` 行和一行本地 `replace`**：
>   ```
>   require github.com/tangwy-t/UniCenter/uni_protocol v0.0.0
>   replace github.com/tangwy-t/UniCenter/uni_protocol => ../uni_protocol
>   ```
>   （见 `Makefile` 的 `uni_protocol-release-precheck` 目标。）
> - 所以它证明的只是：**「当存在一条解析路径（本地目录 `replace`）时，`uni_core` 能脱离 `go.work` 构建」**。
> - 它**没有**证明、也**无法**证明：**「这个模块已经具备发布形态」**。
>   发布形态要求的是「已发布的 tag + 远端可 fetch + `require` + `go.sum`」，而 `replace` 恰好把
>   这三件事**全部绕开**了。**代理变绿 ≠ 可以发布。**
> - 反向验证（证明它不是空转）：「无 `replace` + 仅 `require`」→ 必红于 `missing go.sum entry`（§0 原文）；
>   「无 `require` + `-mod=mod`」→ 必红于 `cannot find module providing package ... could not read Username`（§2 B5）。
>   **但那个「红」的原因是解析层（远端不可达），不是「隔离生效」** —— 两者要分清。

### 1.3 零第三方依赖：`make uni_protocol-deps`

```
$ make uni_protocol-deps
uni_protocol 零第三方依赖 ✓
exit=0
```

（`uni_protocol/go.mod` 中 `grep -c '^require'` = **0**；`go list -deps` 过滤自身后为空。）

### 1.4 各模块自测：各自目录下 `go test ./... -count=1`

```
$ (cd uni_protocol && go test ./... -count=1)
ok  	github.com/tangwy-t/UniCenter/uni_protocol	0.007s
exit=0

$ (cd uni_core && go test ./... -count=1)
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/repository	0.182s
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/scheduler	0.011s
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/service	5.977s
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/task	0.004s
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/task/tasks	0.011s
ok  	github.com/tangwy-t/UniCenter/uni_core/internal/wireup	2.016s
ok  	github.com/tangwy-t/UniCenter/uni_core/tools/apigen	0.005s
exit=0

$ (cd uni_console && pnpm test)          # vitest
 ✓ src/modules/device/__tests__/resource-drill.test.ts (4 tests) 11ms
 ✓ scripts/check-permissions.test.mjs (6 tests) 12ms
 Test Files  22 passed (22)
      Tests  234 passed (234)
exit=0
```

> 三个模块 = `uni_protocol`（被发布的协议模块）、`uni_core`（Go 主模块）、
> `uni_console`（Vue3 前端，非 Go 模块但同属一次发布）。

### 1.5 聚合门禁：`make release-precheck`

聚合 `uni_protocol-contract` + `uni_protocol-release-precheck`：

```
$ make release-precheck
uni_protocol 零第三方依赖 ✓
--- PASS: TestVersionsAreConsistent (0.00s)
...
uni_core 脱工作区可构建(隔离副本 + 本地 replace)✓
exit=0
```

**发布前最小动作**：`make release-precheck && (cd uni_core && go test ./... -count=1)`。
跑完任何目标后 `git status --short` 必须为空（代理用的是工作区内 `.tmp-iso/`，由目标内 `trap` 清理）。

---

## 2. (B) 必须等远端就绪——每条现在都**必然失败**

以下五条是**真正的发布步骤**，全部依赖一个**可 fetch、可 push 的远端**。**当前没有这样的远端。**

先看硬事实：

```
$ git remote -v
upstream	https://github.com/tangwy-t/WebManagerFramework.git (fetch)
upstream	/nonexistent/NO_PUSH_TO_UPSTREAM_READONLY (push)
$ git tag
v1.1.0
$ grep -c uni_protocol uni_core/go.mod
0
$ grep -c uni_protocol uni_core/go.sum
0
```

### B1. 给 `uni_protocol` 打 tag

**现在不能做**：tag 命名方案本身就是个必须先解决的问题（§3.1 / §4）。
裸 `vX.Y.Z` 会被仓库里的**两个**子目录模块同时认领。等 §4 的方案定死之后才能打。

### B2. push 到远端

**现在不能做**：`git remote -v` 显示 **push URL 是 `/nonexistent/NO_PUSH_TO_UPSTREAM_READONLY`**
——一个**故意的只读哨兵路径**，不是仓库。实测：

```
$ git push --dry-run
fatal: '/nonexistent/NO_PUSH_TO_UPSTREAM_READONLY' does not appear to be a git repository
fatal: 无法读取远程仓库。
请确认您有正确的访问权限并且仓库存在。
exit=128
```

即便 tag 打好了，**也推不出去**。

### B3. 在 `uni_core/go.mod` 里加 `require`（预备补丁见 `uni_core/go.mod.release`）

**现在不能做**：`require` 里的**版本号必须指向一个真实存在的已发布 tag**。现在
`go list -m -versions github.com/tangwy-t/UniCenter/uni_protocol` **必红**（见 B5），
写进去只是一个**无人能解析的幻影版本**。

预备补丁文件已经写好（§5），远端就绪后追加那一行即可。

### B4. `go mod tidy` + 提交 `go.sum`

**现在不能做**：在 `uni_core` 里跑 `go mod tidy` **直接失败**，实测原文：

```
$ (cd uni_core && go mod tidy)
go: finding module for package github.com/tangwy-t/UniCenter/uni_protocol
go: github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub imports
	github.com/tangwy-t/UniCenter/uni_protocol: cannot find module providing package github.com/tangwy-t/UniCenter/uni_protocol: module github.com/tangwy-t/UniCenter/uni_protocol: git ls-remote -q --end-of-options origin in /home/workspace/UniCenter/.gomodcache/cache/vcs/ff8ae50511509b65bdcadaa86b600a550ab544cbc92608621b3eb0ec1edfe1b1: exit status 128:
	fatal: could not read Username for 'https://github.com': terminal prompts disabled
Confirm the import path was entered correctly.
If this is a private repository, see https://golang.org/doc/faq#git_https for additional information.
exit=1
```

> **`uni_core` 目前无法独立 `tidy`** —— 因为 `internal/pkg/agenthub` 在生产代码里
> `import "github.com/tangwy-t/UniCenter/uni_protocol"`，而该模块路径不可 fetch。
> 这也是为什么 `go.sum` 里**至今没有** `uni_protocol` 条目：`tidy` 根本跑不完。
> **发布后**，`tidy` 才会写入条目，届时**必须一并提交 `go.sum`**。

### B5. 真发布形态构建验证（**这一步从未成功过**）

**现在不能做**：附录原文见 §0。最直接的失败证据：

```
$ go list -m -versions github.com/tangwy-t/UniCenter/uni_protocol
go: module github.com/tangwy-t/UniCenter/uni_protocol: git ls-remote -q --end-of-options origin in /home/workspace/UniCenter/.gomodcache/cache/vcs/ff8ae50511509b65bdcadaa86b600a550ab544cbc92608621b3eb0ec1edfe1b1: exit status 128:
	fatal: could not read Username for 'https://github.com': terminal prompts disabled
Confirm the import path was entered correctly.
If this is a private repository, see https://golang.org/doc/faq#git_https for additional information.
exit=1
```

以及**在隔离副本里追加 `require` 后脱工作区构建**（发布形态的忠实模拟）：

```
$ GOWORK=off go build ./...           # 仅 require，无 replace
internal/pkg/agentmetrics/downsample.go:6:2: missing go.sum entry for module providing package github.com/tangwy-t/UniCenter/uni_protocol (imported by github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub); to add:
	go get github.com/tangwy-t/UniCenter/uni_core/internal/pkg/agenthub
exit=1

$ GOFLAGS=-mod=mod GOWORK=off go build ./...   # 让 go 自己解析
go: downloading github.com/tangwy-t/UniCenter/uni_protocol v1.0.0
internal/pkg/agentmetrics/downsample.go:6:2: reading github.com/tangwy-t/UniCenter/uni_protocol/go.mod at revision uni_protocol/v1.0.0: git ls-remote -q --end-of-options origin in /home/workspace/UniCenter/.gomodcache/cache/vcs/ff8ae50511509b65bdcadaa86b600a550ab544cbc92608621b3eb0ec1edfe1b1: exit status 128:
	fatal: could not read Username for 'https://github.com': terminal prompts disabled
Confirm the import path was entered correctly.
If this is a private repository, see https://golang.org/doc/faq#git_https for additional information.
exit=1
```

注意第二段里 `at revision uni_protocol/v1.0.0`：`go` **就是在按 §4 的目录前缀规则找 tag**，
只是远端连不上。

---

## 3. (C) 已知风险

### 3.1 tag 命名空间冲突（**发布前必须解决**）

`uni_protocol` 与 `uni_core` **同在 `github.com/tangwy-t/UniCenter` 这一个仓库**，
且**两者都不在仓库根目录**（根目录没有 `go.mod`）。后果：

- **裸 `vX.Y.Z` 会被「仓库根」认领**，而根不是一个模块；
- 两个子目录模块**都需要带目录前缀的 tag**，否则 `go` 会报
  `invalid version: unknown revision <module-subdir>/<ver>`（§4 有本仓库实测）。

**解法（已定，见 §4）**：目录前缀 tag —— `uni_protocol/vX.Y.Z` **且** `uni_core/vX.Y.Z`。

### 3.2 `v1.1.0` 的归属

仓库里现存的**唯一** tag 是 `v1.1.0`，它是**裸 tag，打在整个仓库上**。

- 它**不是** `uni_protocol` 的版本，也**不是** `uni_core` 的版本；
- 按 §4 的实测结论，它**无法**被 `uni_protocol` 解析（`go` 只会去找 `uni_protocol/v1.1.0`）；
- 它目前是**仓库级的里程碑标记**（`make` 的 `VERSION` 用它做 `git describe`）。
- **发布时不要复用/移动它**：tag 一旦创建就不应改写（Go 会对篡改过的 tag 报安全错误）。

### 3.3 其它

- 仓库存着 `go.work`（`use ./uni_core ./uni_protocol`）：**它会让本地/CI 都走到工作区解析路径，
  从而掩盖「发布形态缺失」这件事**。发布验证必须显式 `GOWORK=off`，否则门禁会失真
  （`go env GOWORK` 是按**文件名**沿祖先链查找的，副本放进仓库里也会被根 `go.work` 接管）。
- `uni_core/go.mod` 里的 `require ... uni_protocol` 一旦加上，**跨模块 import 会被 CI 立刻检验**；
  在此之前任何新增跨模块 import 都是**无人看守**的。

---

## 4. tag 命名方案（结论）

**方案：目录前缀 tag。`uni_protocol` / `uni_core` / `uni_agent` 三个模块的 tag 都必须带自己的目录前缀。**

（`uni_agent` 是后加的：它虽然不进 Go 的模块依赖图（core 不 import 它），
但它的**产物版本**要参与升级判定（semver 比对），故同样按 `uni_agent/vX.Y.Z` 打 tag，
并由 `make uni_agent-release` 用 `AGENT_VERSION` 注入 —— 缺省值从该前缀 tag 提取，
发版时显式传 `AGENT_VERSION=0.2.0` 最稳妥。）

软硬件依据分两层：**本仓库本地实测**（主证据） + **Go 官方文档原文**（旁证）。

### 4.1 本仓库本地实测（主证据，完全离线）

做法：用 `git url.insteadOf` 把 `https://github.com/tangwy-t/UniCenter`
**重写到本仓库的本地路径**，于是 `go` 会走**真实的 vcs 版本解析代码路径**（`ls-remote` + 前缀 tag 解析），
却**完全不联网** —— 这是在无可推远端的环境里能拿到的、最接近发布形态的证据。

一次性实验仓库（已用完删除）与消费者：

```
$ git config --file vcs/cfg --add url."file:///home/workspace/UniCenter".insteadOf "https://github.com/tangwy-t/UniCenter"
$ export GIT_CONFIG_GLOBAL=.../vcs/cfg GOPROXY=direct GOSUMDB=off GONOSUMDB='*' GOPRIVATE='github.com/tangwy-t/*'

$ go list -m -versions github.com/tangwy-t/UniCenter/uni_protocol
github.com/tangwy-t/UniCenter/uni_protocol          # 版本列表为空
exit=0

$ go mod download github.com/tangwy-t/UniCenter/uni_protocol@v1.1.0      # 裸 tag 形态
go: github.com/tangwy-t/UniCenter/uni_protocol@v1.1.0: invalid version: unknown revision uni_protocol/v1.1.0
exit=1
```

> **这一条就是结论**：仓库里**明明存在** `v1.1.0`（`git ls-remote` 可见 `refs/tags/v1.1.0`），
> 但 `go` **拒绝**用它给 `uni_protocol` 赋版本，并且明确告诉你它去找的是
> `uni_protocol/v1.1.0`。**裸 tag 不被子目录模块认领。**

对照组（一次性仓库，同样离线）：

```
# repo: 两个子目录模块 mod_a / mod_b；同时打裸 tag v1.0.0 与目录前缀 tag mod_b/v1.1.0
$ go list -m -versions github.com/tagcheck/modrepo/mod_b
github.com/tagcheck/modrepo/mod_b v1.1.0                    # 只认带前缀的
exit=0

$ go mod download github.com/tagcheck/modrepo/mod_b@v1.0.0  # 试裸 tag
go: github.com/tagcheck/modrepo/mod_b@v1.0.0: invalid version: unknown revision mod_b/v1.0.0
exit=1

$ go mod download -json github.com/tagcheck/modrepo/mod_b@v1.1.0
	"Version": "v1.1.0",
	"Dir": "...../mod_b@v1.1.0"
exit=0

# 对照: 模块在仓库根的 repo，裸 tag v1.0.0 直接被认领
$ go list -m -versions github.com/tagcheck/rootrepo
github.com/tagcheck/rootrepo v1.0.0
exit=0
```

**读法**：**模块在仓库根 → 裸 tag；模块在子目录 → 前缀 tag。**

### 4.2 Go 官方文档原文（旁证）

来源：Go Modules Reference，`Mapping versions to commits`
（<https://go.dev/ref/mod#vcs-version>，本地抓取副本 `.tmp-tagcheck/mod.md` 第 3322–3342 行）：

> If a module is defined in the repository root directory or in a major version
> subdirectory of the root directory, then each version tag name is equal to the
> corresponding version. ...
>
> If a module is defined in a subdirectory within the repository, that is, the
> module subdirectory portion of the module path is not empty, then **each tag
> name must be prefixed with the module subdirectory, followed by a slash**. For
> example, the module `golang.org/x/tools/gopls` is defined in the `gopls`
> subdirectory of the repository with root path `golang.org/x/tools`. The version
> `v0.4.0` of that module must have the tag named `gopls/v0.4.0` in that repository.

### 4.3 结论落定

| 模块 | 模块路径 | 在仓库中的位置 | **tag 形态** | 示例 |
|---|---|---|---|---|
| `uni_protocol` | `.../UniCenter/uni_protocol` | 子目录 `uni_protocol/` | **必须带前缀** | `uni_protocol/v1.0.0` |
| `uni_core` | `.../UniCenter/uni_core` | 子目录 `uni_core/` | **必须带前缀** | `uni_core/v1.2.0` |
| （仓库根） | 无 `go.mod` | 根 | 裸 tag 只归属「根」，不可用于任一模块 | `v1.1.0`（现状，见 §3.2） |

**明确回答「`uni_core` 的 tag 形态是什么」**：**`uni_core` 也是子目录模块，它的 tag 同样必须是
`uni_core/vX.Y.Z`**。**不能**给它用裸 tag —— 这一点容易被想当然搞错（"`uni_core` 是主模块，
所以用裸 tag"），但**仓库根没有 `go.mod`，所以根根本不是模块**，裸 tag 谁也认领不了。

> **候选 (b)：拆独立仓库** —— 把 `uni_protocol` 拆到自己的 Git 仓库，就能用裸 `vX.Y.Z`，
> 彻底绕过命名空间冲突。**这是重构，不在本计划范围内**，长期可选项，此处仅登记。

---

## 5. 远端就绪后的发布清单（照做）

前置：有一个**可 push 且可匿名 fetch** 的远端，且 `git remote -v` 的 push URL 不再是哨兵路径。

1. **打 tag**（§4 结论，两个模块各自前缀）：
   ```bash
   git tag uni_protocol/v1.0.0        # 版本号 = uni_protocol 自身的版本
   git tag uni_core/v1.2.0            # uni_core 的版本,独立命名空间
   ```
2. **push 分支与 tag**：
   ```bash
   git push <对的目标> main --follow-tags      # 或分别 push 两个 tag
   ```
3. **让 `uni_core` 依赖它**：把 `uni_core/go.mod.release` 里的 `require` 行
   **原样追加**到 `uni_core/go.mod`（版本号必须与步骤 1 的 tag **完全一致**）。
4. **收敛依赖并提交**：
   ```bash
   cd uni_core && go mod tidy && git add go.mod go.sum
   ```
   （`go.sum` 在这一步之前**不会有** `uni_protocol` 条目，这是正常的。）
5. **验证真发布形态**（**必须脱工作区**，否则验的是工作区）：
   ```bash
   cd uni_core && GOWORK=off go build ./... && GOWORK=off go test ./... -count=1
   ```
   期望：**绿**。这一步成功之前，本手册 §0 的免责声明一直有效。
6. 复核 `make release-precheck` 仍绿，`git status --short` 为空。