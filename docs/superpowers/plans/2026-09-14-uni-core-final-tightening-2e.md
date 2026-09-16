# uni_core 收尾收敛（Plan 2E）Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 收掉 Plan 2D 结束时如实留下的两条：① **1h 空洞不自愈**——`rollup` 对「当时没有 5m 行」的小时是「跳过并推进水位」，若那些 5m 行稍后（超过 `emptyHourGrace`）才落库，该小时的 1h 行**永久缺失**；② **P1 修复留下的读放大**——`flush` 逐桶调用 `Bucket`，积压后首轮 288 个桶最坏各读上万个点（约 2.5M 条 JSON）。

**Architecture:** 两处都是**改既有实现的算法**，不引入新概念：给 `rollup` 加一条「按需重扫」路径（用两条集合查询做差，只补真正缺的小时），把 `flush` 的读取从「逐桶 N 次」改成「每设备 1 次 + 内存切桶」。

**Tech Stack:** Go 1.26、GORM v1.31、`redis/go-redis/v9`、`miniredis`（测试）。

## Global Constraints

- **Go 工具链**：`/root/go/bin/go`（**不在 PATH**）。每条 `go` 命令前：
  ```bash
  export PATH="$PATH:/root/go/bin"
  export GOCACHE=/home/workspace/UniCenter/.gocache GOMODCACHE=/home/workspace/UniCenter/.gomodcache
  ```
  沙箱不允许写 `/root/.cache/go-build`。**不要**设置 `GOFLAGS`。
- **不要动** `go.work` 与 `uni_core/go.mod`/`go.sum`；**不要**替换 `go.work` 的 `use` 列表。
- **沙箱坑**：`/tmp` 在两次 bash 调用之间会被清空；**不要用裸 `git checkout -- <path>` 还原未提交的改动**（前面有任务因此清掉过成果）——备份写工作区内文件并用它还原，**跑完把备份目录删掉**。
- **四条纪律**：① 外部 API 先 `go doc` 核实；② **每条修复都要有断言，且做反向验证**（临时改回旧行为 → 断言**变红** → 还原 → 复绿，贴红灯原文）；**不得**用编译失败冒充断言触发；③ 不得降低断言强度或改既有测试来凑绿；④ 不得留占位符（`TODO`/`_ = xxx` 死赋值）。
- **口径不得改**：均值类按均值且 `nil` 跳过、单调量 LAST、温度 max、整机合计由明细 Σ、比值由 Σ 重算、1h 按 `samples` 加权、5min-only 列在 1h 为 NULL。
- **两段提交不可合并**：`_5m` 与 `_1h` 是两个独立事务。
- **水位语义不得放宽**：`cursor_5m`/`cursor_1h` **只在全成功后才前移**，且写入走既有的**乐观 CAS**（`CursorStore.Advance`：当前值必须仍等于轮初读到的值 且 新值更大）。
- **测试栈**：sqlite `file:"+t.Name()+"?mode=memory&cache=shared"`（**必须带 `cache=shared`**——裸 `:memory:` 在连接池开第二条连接时会看到空库）+ `database.NewCallbacks(node).Register(db)` + 显式 `AutoMigrate`；`_1h` 表必须 `db.Table(entity.TableNameMetric1h).AutoMigrate(...)` 显式建；Redis 用 `miniredis.RunT(t)`；日志用 `logger.NewNop()`。
- **不要动** `uni_protocol/`、`uni_console/`、`.github/`；`docs/` 只在明确要求时改。

---

## File Structure

| 文件 | 职责 |
|---|---|
| `internal/pkg/agentmetrics/raw.go` | **改**：新增 `BucketRange`（读**一次**覆盖整段范围），`Bucket` 改为它的薄封装 |
| `internal/pkg/agentmetrics/raw_test.go` | **改**：`BucketRange` 的断言 + 读次数断言 |
| `internal/service/agent_metrics_flush.go` | **改**：`flushDevice` 改为「读一次 + 内存切桶」 |
| `internal/service/agent_metrics_flush_test.go` | **改**：读次数与行为等价断言 |
| `internal/repository/device_metric.go` | **改**：新增两个「已有桶集合」查询（供重扫做差） |
| `internal/repository/device_metric_test.go` | **改** |
| `internal/service/agent_metrics_rollup.go` | **改**：新增「按需重扫」路径 |
| `internal/service/agent_metrics_rollup_test.go` | **改** |

---

### Task 1: `BucketRange` + flush 改为「每设备读一次 + 内存切桶」

**问题**：`flushDevice` 的 `for b := from; b < upper; b += bucketSec` 里**每个桶各调一次 `Bucket`**。P1 修复后 `fetch` 由「最新点离 `fromMs` 多远」推出，于是**积压越久、每桶读得越多**：首轮 288 个桶最坏各读 `MaxPoints`（10368）条 → 约 **2.5M 条 JSON** 读取与解析，且其中绝大多数点被重复读取 288 遍。

**Files:**
- Modify: `uni_core/internal/pkg/agentmetrics/raw.go` + `raw_test.go`
- Modify: `uni_core/internal/service/agent_metrics_flush.go` + `agent_metrics_flush_test.go`

**Interfaces:**
- Produces: `agentmetrics.RawStore.BucketRange(ctx, deviceID uint64, fromMs, toMs int64) ([]agentproto.MetricsSample, error)` —— 读**一次**覆盖 `[fromMs, toMs)` 的原始点，按 `T` 升序返回。`Bucket` 保留为它的薄封装（`BucketRange(ctx, id, from, to)`），**签名不变**（查询服务的资源下钻在用）。

- [ ] **Step 1: 写失败的测试**

1. **等价性**：对同一 `[from,to)`，`BucketRange` 与 `Bucket` 返回**逐条相同**的点（同样的左闭右开、同样的升序、同样的脏数据跳过）；
2. **读次数**：覆盖 288 个桶的一次 `BucketRange` 调用，miniredis 的 `CommandCount()` 增量必须是**常数级**（1 次 `LIndex` + 1 次 `LRange`），**不是** 288 次 —— 这条直接钉住「读放大被消除」；
3. **flush 端**：积压 24h（288 个桶）时，`flushDevice` 对 `raw` 的读取调用次数为 **1**（用一个记录调用次数的替身断言），且**落库结果与修复前逐桶读取完全一致**（用同一批数据跑两遍、比对写出的行）；
4. 既有 `Bucket` 的 5 条断言（多点多桶可见、边界、极老窗口有界、空窗单次调用、脏数据跳过）**一条都不许改**。

- [ ] **Step 2: 跑测试确认失败**

```bash
export PATH="$PATH:/root/go/bin"
export GOCACHE=/home/workspace/UniCenter/.gocache GOMODCACHE=/home/workspace/UniCenter/.gomodcache
cd /home/workspace/UniCenter/uni_core && go test ./internal/pkg/agentmetrics/ ./internal/service/ -run 'TestBucketRange|TestFlushReadsWindowOnce' 2>&1 | head -20
```
Expected: FAIL —— `undefined: BucketRange`。

- [ ] **Step 3: 实现**

**(a) `BucketRange`**：把现有 `Bucket` 的实现改名为 `BucketRange`（内部完全不变：`LIndex(0)` 取最新点 T → `fetch = (newestT-fromMs)/Step*2+2` 夹到 `[1,MaxPoints]` → `LRange` → 按 `[fromMs,toMs)` 过滤 → 升序）。`Bucket` 变成一行委托。
**(b) `flushDevice`**：
```go
// 读一次覆盖整段待处理区间的原始点，再在内存里切桶。
// 为什么不能逐桶读：见 Plan 2D 的 P1 修复 —— fetch 由「最新点离 fromMs 多远」推出，
// 逐桶读会让积压 24h 的首轮把同一批点重复读 288 遍（约 2.5M 条 JSON）。
pts, rerr := s.raw.BucketRange(ctx, deviceID, from*1000, upper*1000)
if rerr != nil { return ... }              // 读失败立即返回，水位留在轮初
byBucket := groupByBucket(pts, bucketSec)  // map[int64][]MetricsSample，按 T/bucketSec 归组
for b := from; b < upper; b += bucketSec {
    wide, subs, ok := agentmetrics.Downsample(deviceID, b, byBucket[b])
    // ...以下与既有逐桶逻辑**逐字相同**（空桶照常推进、失败立即返回）
}
```
`groupByBucket` 写成**未导出的纯函数**（便于单测）：`T/1000/bucketSec*bucketSec` 对齐；**落在请求区间之外的点由 `BucketRange` 已过滤**，不需二次判界。
**(c) 接口**：`s.raw` 的消费方窄接口从 `Bucket` 换成 `BucketRange`（若查询服务另有消费面则保持不动——它用 `Bucket`，两处接口各自独立）。

- [ ] **Step 4: 反向验证 + 跑绿**

**反向验证**：把 `flushDevice` 改回「逐桶调用」（**可编译**）→ 断言 3（读取调用次数为 1）必须变红，贴红灯原文；还原后复绿。

```bash
cd /home/workspace/UniCenter/uni_core && gofmt -w internal/ && gofmt -l internal/ && go vet ./... && \
  go test ./internal/pkg/agentmetrics/ ./internal/service/ ./internal/wireup/ -count=1 2>&1 | tail -10
```
**注意**：`internal/wireup` 的 E2E 依赖 flush 的落库结果，**必须仍然绿**（这是「行为等价」的最强证据）。

- [ ] **Step 5: 提交**

```bash
cd /home/workspace/UniCenter
git add uni_core/internal/pkg/agentmetrics/ uni_core/internal/service/agent_metrics_flush.go \
        uni_core/internal/service/agent_metrics_flush_test.go
git commit -m "perf(agent-flush): 每设备只读一次原始窗并内存切桶，消除积压期的读放大"
```

---

### Task 2: rollup 的「按需重扫」，让 1h 空洞能自愈

**问题**：`rollupDevice` 对「没有 5m 行」的小时：窗口内 → 扣住水位重试（好）；**超出 `emptyHourGrace`** → **跳过并推进水位**（必要，否则离线设备会把水位永久卡死）。但若那些 5m 行**后来才落库**（比如 flush 因故停顿数小时、或原始点积压后首轮才补上，且晚于 2 小时的等待窗口），该小时的 **1h 行永久缺失**——而 >30 天的窗口**只有 1h 表可查**。repair 集合兜不住它（repair 只装「写出过残缺行」的小时）。

**Files:**
- Modify: `uni_core/internal/repository/device_metric.go` + `device_metric_test.go`（新增两个「已有桶集合」查询）
- Modify: `uni_core/internal/service/agent_metrics_rollup.go` + `agent_metrics_rollup_test.go`

**Interfaces:**
- Produces:
  - `repository.DeviceMetricRepo.ExistingBucketTimestamps(ctx, table string, deviceID uint64, from, to int64) ([]int64, error)` —— 返回该设备在该表上 `[from,to]` 内**已有行的 `bucket_ts` 集合**（升序）。**一条 `SELECT bucket_ts` 查询**，不做 `COUNT(*)`、不读整行。
  - **重扫做成 `RollupOnce` 内的第 3 步**（不新增导出方法）：任务层与 wireup 因此**不需要任何改动**，重扫与游标区间、repair 在同一轮里完成。
  - 配置键 `sys.agent.rollupRescanHours`（默认 **24**）—— 重扫窗口小时数

- [ ] **Step 1: 写失败的测试**

1. **核心**：某小时**先无 5m 行**（rollup 越过它、水位前进）→ **随后补上该小时的 5m 行**（模拟迟到落库）→ 下一轮 `RollupOnce` **必须把该小时的 1h 行补出来**（断言该 1h 行存在且值正确）。**这条在修前必须红。**
2. **代价有界**：一轮里「1h 行已齐全」的小时**不得**被重写（用写路径替身断言 `WriteHour` 的调用次数 = 仅缺失的那些小时数）；
3. **窗口**：超出 `rollupRescanHours` 的更老小时**不重扫**（造一个 25 小时前的缺失小时 → 断言本轮不补；这是明确的能力边界，要被钉住而不是被当作 bug）；
4. **不推进水位**：重扫**不得**改动 `cursor_1h`（它只补行；水位由普通区间与 repair 管）；
5. **幂等**：连跑两轮，第二轮不重复写（第一轮补齐后，第二轮按集合差算出「无缺失」）；
6. **与 repair 的关系**：repair 集合里的小时**也要**参与（两者结果去重，同小时只写一次）；
7. 既有 rollup 断言（加权、5min-only NULL、repair 重算、有界、空小时等待窗口、游标语义）**一条都不许改**。

- [ ] **Step 2: 跑测试确认失败**

```bash
cd /home/workspace/UniCenter/uni_core && go test ./internal/service/ -run 'TestRollupRescan|TestRollupLateRows' 2>&1 | head -20
```
Expected: FAIL。

- [ ] **Step 3: 实现**

`RollupOnce` 在既有两步之后加**第 3 步**：
1. 计算重扫窗口 `[alignDown(now,3600) − rescanHours×3600, alignDown(now,3600))`（**不含正在填充的当前小时**）；
2. 每设备 **两条集合查询**：`ExistingBucketTimestamps(_5m, ...)` 与 `ExistingBucketTimestamps(_1h, ...)`；
3. **差集** = 有 5m 行但没有 1h 行的小时 → **只对这些小时**调 `rollupHour`（内部已有 `ReadWideRows → RollupToHour → WriteHour`，UPSERT 幂等）；
4. 与 repair 集合的小时**去重**（同一小时只写一次）；
5. **不碰水位**；
6. 新增统计字段（照既有 `RollupStats` 注释体例）：`HoursRescanned`、`HoursBackfilled`（**加字段要同时更新 `RollupStats` 的文档注释**，并让任务层日志带上它们）。

**为什么用「集合差」而不是「无脑重扫 K 小时」**：无脑重扫会每轮对每设备写 K 个 1h 行（500 设备 × 24 小时 = 12000 次 UPSERT / 5 分钟），而集合差只需 **2 次读**、且只为**真正缺失**的小时写（正常情况下为 0 次写）。

- [ ] **Step 4: 反向验证 + 跑绿**

**反向验证**（两条**可编译**的变异，贴红灯原文 + 还原复绿）：
1. **删掉第 3 步**（重扫整段不执行）→ 断言 1 必须变红；
2. **把差集改成「无脑重扫」**（对窗口内所有小时都写）→ 断言 2（不重写已齐全的小时）必须变红。

```bash
cd /home/workspace/UniCenter/uni_core && gofmt -w internal/ && gofmt -l internal/ && go vet ./... && \
  go test ./internal/... -count=1 2>&1 | tail -10
```
Expected：全绿（含 `internal/wireup` 的 E2E）。

- [ ] **Step 5: 提交**

```bash
cd /home/workspace/UniCenter
git add uni_core/internal/repository/device_metric.go uni_core/internal/repository/device_metric_test.go \
        uni_core/internal/service/agent_metrics_rollup.go uni_core/internal/service/agent_metrics_rollup_test.go
git commit -m "fix(agent-rollup): 加入按需重扫，让迟到落库的 5m 行能补出 1h 空洞"
```

---

## 完成标准（Plan 2E Definition of Done）

- [ ] `cd uni_core && go build ./...` 通过；`go test ./internal/...` 全绿；`gofmt -l internal` 与 `go vet ./...` 无输出。
- [ ] **读放大已消除**：积压 24h 时 flush 对原始窗的读取次数为 **1**（有断言），且**落库结果与逐桶读取逐行一致**（行为等价有断言）。
- [ ] `Bucket` 的 5 条既有断言全绿（它是 `BucketRange` 的薄封装，语义未变）。
- [ ] **1h 空洞能自愈**：迟到落库的 5m 行会在下一轮补出 1h 行（有断言，且该断言修前是红的）。
- [ ] 重扫**只为缺失的小时写**（已齐全的不重写，有断言）；窗口边界（超出 `rollupRescanHours` 的不补）被断言钉住；**不改动水位**。
- [ ] `RollupStats` 新字段有文档注释；任务层日志带上它们。
- [ ] `go.work` / `uni_core/go.mod` / `go.sum` 未改；`uni_protocol/`、`uni_console/`、`.github/`、`docs/` 未触碰。

## 后续（不在本计划范围内）

| 项 | 说明 |
|---|---|
| **Plan 4** | `uni_console` 前端：设备列表/详情、ECharts 趋势（`t` 定位 + 稀疏桶 + `available_metrics` + `resolution_seconds` 驱动轴刻度）、资源 drill 下拉（含 `stale`） |
| **发布打包** | `uni_protocol` 打独立 tag + `uni_core/go.mod` 加 `require`（**不要** `replace`）+ 脱工作区构建验证 |
| **已知能力边界（非缺陷，已断言钉住）** | 5m 空洞：原始点超出 24h 窗 → 重放读不到数据，只能等新数据（5m 是唯一真值来源，没有可推导的替代）；1h 空洞：`rollupRescanHours`（默认 24h）之内由 rollup 的按需重扫自愈，**24h 之外由 `agent-metrics-backfill` 的 `{"hours":N,"resolution":"1h"}` 回退 `cursor_1h` 后由下一轮 rollup 补出**（1h 的重算读的是库里的 5m 行、不依赖原始点，故自愈范围 = 5m 保留期，默认 30 天）；**超出 30 天则永久缺失**（重算的输入已被保留期回收），不再需要手工改 Redis 键 |