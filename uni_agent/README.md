# uni_agent

UniCenter 的设备侧采集代理。采集本机真实指标，经**一根外向 WebSocket** 上报给
`uni_core`（不开放任何入站端口，core 也不反向连接 agent）。

设计依据：`docs/superpowers/specs/2026-09-14-device-agent-design.md`；
协议契约：`uni_protocol`（包 `agentproto`）。

## 运行

```bash
go build -o uni_agent .

./uni_agent \
  -url ws://127.0.0.1:8088/api/v1/agent/ws \
  -enroll-token <enroll 令牌>
```

也可用环境变量：`UNI_AGENT_URL`、`UNI_AGENT_ENROLL_TOKEN`、
`UNI_AGENT_REPORT_INTERVAL`、`UNI_AGENT_HEARTBEAT_INTERVAL`、`UNI_AGENT_STATE_DIR`。

| 参数 | 默认 | 说明 |
|---|---|---|
| `-url` | 必填 | core 的 agent WebSocket 端点 |
| `-enroll-token` | — | 首次注册令牌（`sys.agent.enrollToken` 配置值） |
| `-interval` | `10s` | 上报周期；core 会在 `hello_ack` 里下发权威值 |
| `-heartbeat` | `30s` | 应用层心跳周期（另有 WS ping/pong 兜底） |
| `-state-dir` | `/var/lib/uni_agent` | 存 instance id 与已签发的 agent token |
| `-version` | `0.1.0` | 上报给 core 的 Agent 版本号 |

## 采集的指标

覆盖 `MetricsSample` 的全部字段，不含预留项：

- **CPU**：使用率、每核占用、iowait（`/proc/stat` 差量）
- **负载**：1/5/15 分钟
- **内存 / swap**：使用率与绝对量；swap 未配置时省略而非报 0
- **磁盘**：各挂载点容量、使用率、**inode 使用率**
- **磁盘 IO**：读/写速率、读/写 IOPS、**IO 繁忙度（%util）**
- **网卡**：收/发速率、包速率、错误、丢包
- **TCP/UDP**：总数、ESTABLISHED、LISTEN、TIME_WAIT、CLOSE_WAIT
- **其它**：进程数、温度传感器、开机时长
- **静态信息**（随 hello 上报）：主机名、平台、内核、CPU 型号/核数、内存总量、开机时间
- **自监控**（`AgentMetric`）：采集耗时、上报成功/失败数、重连次数、常驻内存、积压、丢弃数

## 几个容易踩的坑（都有回归测试）

1. **凭据必须恰好一个。** core 明确「歧义时不暗自取优先级」：`enroll_token`
   与 `agent_token` **同时存在**会被判为凭据歧义并返回 4001，而不是「两个都带更保险」。
   现象是**首次能连上、之后再也连不上** —— 因为首次注册时 `agent_token` 还是空的。
   见 `TestCurrentHelloExactlyOneCredential`。

2. **速率必须做差量。** `/proc` 里的 disk_io / 网卡是单调累计计数器，
   直接上报会把「开机至今的总流量」当成每秒速率。首帧只为播基线，不发 0
   （0 是「真的没流量」，不是「还没基线」）。见 `TestFirstFrameOmitsRates`。

3. **ping/pong 必须续期读期限。** gorilla 的 `ReadMessage` 只对**数据帧**返回，
   ping/pong 会被内部消化。只在循环开头设一次读期限，会让一条「长期只有 ping、
   没有数据」的健康链路被自己判死（实测每 90s 断一次，报 i/o timeout）。
   且覆盖 ping handler 后**必须自己回 pong**，否则 core 会认为链路已死。

4. **回环网卡要剔两层。** 内核 `FlagLoopback` 是权威依据，但**不保证覆盖所有回环**
   —— 实测 WSL 的 `loopback0` 上报 `ARPHRD_ETHER` 而非 LOOPBACK，
   带着真实流量混进列表。故还要按名字兜一层。

5. **分区与整盘共享计数器。** 都上报会让磁盘 IO 合计翻倍，故剔除
   `sda1` / `nvme0n1p1` 这类分区名。

6. **计数回绕要处理。** 机器重启、网卡重载会让计数器归零；Δ 为负会算出负速率，
   被协议层拒掉**整帧**。见 `TestRateCounterWrap`。

7. **`instance_id` 必须稳定。** 它变了 core 会当成一台新设备，
   设备列表不断堆出重复条目、历史指标脱钩。见 `TestInstanceIDStableAcrossRestarts`。

## 平台降级

以下情形保持 `nil`（前端显示「—」）而不是编 0，因为「采不到」与「真的是 0」是两件事：

| 指标 | 降级条件 |
|---|---|
| `cpu_iowait` | 非 Linux，或 `Iowait == 0`（Windows/Darwin 恒为 0） |
| swap | 未配置 swap（`Total == 0`） |
| `inodes_used_percent` | 文件系统无 inode 概念（FAT/exFAT、部分网络盘） |
| 温度 | 无传感器（WSL2 不向 guest 暴露温度，hwmon 只有 AC/BAT） |
| load | 非 Linux（契约要求非空，故填 0 作为「本平台不提供」的约定值） |

## 测试

```bash
go test ./...
go test -race ./...
```

覆盖：采集结果能通过协议校验、速率差量与回绕、过滤规则（伪文件系统/分区/虚拟网卡）、
sanitize 救回非法样本、首帧不产生速率、凭据恰好一个、积压满时 drop-oldest、
`instance_id` 跨重启稳定、未知消息类型不断连接。
