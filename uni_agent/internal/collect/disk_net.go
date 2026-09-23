package collect

import (
	"math"
	stdnet "net"
	"regexp"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v3/disk"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// 伪文件系统黑名单：这些挂载点不是「磁盘」，把它们算进磁盘合计会让
// 「磁盘使用率」变成一个没有物理意义的数。
//
// 按前缀/子串匹配（见 isPseudoFS）。
var pseudoFSPrefixes = []string{
	"tmpfs", "devtmpfs", "devfs", "proc", "sysfs", "cgroup", "cgroup2",
	"overlay", "squashfs", "ramfs", "autofs", "mqueue", "hugetlbfs",
	"debugfs", "tracefs", "securityfs", "pstore", "bpf", "configfs",
	"fusectl", "binfmt_misc", "rpc_pipefs", "nsfs", "fuse.gvfsd-fuse",
	"fuse.portal", "selinuxfs", "efivarfs", "shm",
}

// 伪挂载点路径黑名单：容器把宿主机的这些路径挂进来，它们不是「本机的盘」。
var pseudoMountPrefixes = []string{
	"/proc", "/sys", "/dev", "/run", "/snap", "/var/lib/docker",
	"/var/lib/kubelet", "/var/lib/containerd",
}

// 分区后缀：`sda1` / `nvme0n1p2`。物理盘与其分区共用同一套 IO 计数器，
// 一起上报会让磁盘 IO 合计（Σ read+write）**翻倍**。
var (
	// sd*/hd*/vd*/xvd* 后面的数字
	reSDNum = regexp.MustCompile(`^(sd|hd|vd|xvd)[a-z]+\d+$`)
	// nvme*/mmcblk*/md*/loop* 的 pN 分区后缀
	rePartP = regexp.MustCompile(`^(nvme\d+n\d+|mmcblk\d+|md\d+|loop\d+)p\d+$`)
	// 纯伪块设备
	reLoopRam = regexp.MustCompile(`^(loop|ram|zram|sr|fd|dm-|drbd)\d*`)
)

// collectDisk 采集各挂载点的容量与 inode。
//
// inodes 是「小文件写满磁盘」这类故障的唯一可见信号（空间还剩很多但 inode 耗尽），
// 故 spec 要求在 Linux 上采集；非 Linux 留 nil。
func (c *Collector) collectDisk(s *agentproto.MetricsSample) {
	parts, err := disk.Partitions(false)
	if err != nil {
		return
	}

	seen := make(map[string]bool, len(parts))
	out := make([]agentproto.DiskMetric, 0, len(parts))
	for _, p := range parts {
		if isPseudoFS(p.Fstype) || isPseudoMount(p.Mountpoint) {
			continue
		}
		// 按规范化后的设备名去重：同一设备被挂两次（bind mount）时
		// 容量会被重复计入合计（Σ 双计）。
		key := p.Device
		if key == "" {
			key = p.Mountpoint
		}
		if seen[key] {
			continue
		}

		u, err := disk.Usage(p.Mountpoint)
		if err != nil || u == nil || u.Total == 0 {
			continue
		}
		seen[key] = true

		const gb = 1024 * 1024 * 1024
		d := agentproto.DiskMetric{
			Mountpoint:  p.Mountpoint,
			FSType:      p.Fstype,
			TotalGB:     round2(float64(u.Total) / gb),
			UsedGB:      round2(float64(u.Used) / gb),
			UsedPercent: clampPercent(u.UsedPercent),
		}
		// used <= total 是协议硬约束（Validate 会拒），浮点舍入可能造出
		// used 比 total 大半分位的假象，这里夹一下而不是丢整个挂载点。
		if d.UsedGB > d.TotalGB {
			d.UsedGB = d.TotalGB
		}
		// inodes：Total == 0 表示该文件系统不提供 inode（FAT/exFAT、部分网络盘），
		// 此时省略而不是报 0%（0% 会被读成「inode 用得极少」，与「没有这个概念」不同）。
		if u.InodesTotal > 0 {
			d.InodesUsedPercent = round2(float64(u.InodesUsed) / float64(u.InodesTotal) * 100)
		}
		out = append(out, d)
	}

	// 稳定排序：mountpoint 字典序，保证同一批挂载点的顺序不随内核返回顺序抖动
	// （顺序抖动会让前端下钻列表与快照测试无谓地变化）。
	sortDisks(out)
	s.Disks = out
}

// collectIOAndNICs 采集磁盘 IO 与网卡速率。
//
// 这两者都必须在**这里**做差量（见 collector.go 的说明）：
// gopsutil 给出的 IoCounters / NetIOCounters 是累计值，
// 直接上报等于「把开机至今的总字节数当成每秒速率」。
func (c *Collector) collectIOAndNICs(s *agentproto.MetricsSample, now time.Time) {
	diskCur := make(map[string]ioCounters)
	nicCur := make(map[string]ioCounters)

	// ── 磁盘 IO ──
	if counters, err := disk.IOCounters(); err == nil {
		names := sortedNames(counters)
		ioOut := make([]agentproto.DiskIOMetric, 0, len(names))
		for _, name := range names {
			ct := counters[name]
			diskCur[name] = ioCounters{
				readBytes:  ct.ReadBytes,
				writeBytes: ct.WriteBytes,
				readOps:    ct.ReadCount,
				writeOps:   ct.WriteCount,
				ioTimeMs:   ct.IoTime,
			}
			// 剔除分区后缀：分区与整盘共享同一套计数器，都上报会让合计翻倍
			if isPartitionName(name) || reLoopRam.MatchString(name) {
				continue
			}
			ioOut = append(ioOut, agentproto.DiskIOMetric{Name: name})
		}

		// 差量：只在**同一设备**的基线存在且 dt > 0 时产出速率
		dt := c.elapsed(now)
		if dt > 0 {
			for i := range ioOut {
				prev, ok := c.prevDisk(ioOut[i].Name)
				if !ok {
					// 首帧无基线：整个条目的速率保持 0（下方 rate 只在 dt>0 且
					// 有基线时才写入），但条目本身要保留 —— 让 core 侧的
					// /resources 知道这台机器有这块盘。
					continue
				}
				curIO := diskCur[ioOut[i].Name]
				ioOut[i].ReadBytesPerSec = rate(curIO.readBytes, prev.readBytes, dt)
				ioOut[i].WriteBytesPerSec = rate(curIO.writeBytes, prev.writeBytes, dt)
				ioOut[i].ReadOpsPerSec = rate(curIO.readOps, prev.readOps, dt)
				ioOut[i].WriteOpsPerSec = rate(curIO.writeOps, prev.writeOps, dt)
				// %util = 设备忙碌毫秒差量 ÷ 经过毫秒 × 100（同 iostat -x 的 %util）。
				//
				// 这是判断「磁盘是否成为瓶颈」最直接的指标：吞吐量低但 %util
				// 接近 100 说明磁盘在排队（小 IO 随机读写），而吞吐量高但
				// %util 低说明是大块顺序读写、磁盘还很闲。
				//
				// ioTimeMs 单调递增，但**可能超过** dt×1000（多队列设备并发
				// 计入多个请求）或少于（设备空闲），故夹到 [0,100]。
				if curIO.ioTimeMs >= prev.ioTimeMs {
					busyMs := float64(curIO.ioTimeMs - prev.ioTimeMs)
					ioOut[i].IOTimePercent = clampPercent(busyMs / (dt * 1000) * 100)
				}
			}
		}
		sortDiskIO(ioOut)
		s.DiskIO = ioOut
	}

	// ── 网卡 ──
	if counters, err := netIOCounters(); err == nil {
		// loopback 集合由内核标志给出（一次系统调用，覆盖所有被标记的接口）。
		loopbacks := loopbackIfaces()
		names := make([]string, 0, len(counters))
		for _, n := range counters {
			if isVirtualNIC(n.Name, loopbacks) {
				continue
			}
			names = append(names, n.Name)
		}
		sortStrings(names)

		dt := c.elapsed(now)
		nicOut := make([]agentproto.NICMetric, 0, len(names))
		for _, name := range names {
			var ct netCounter
			for _, n := range counters {
				if n.Name == name {
					ct = netCounter{
						rxBytes: n.BytesRecv, txBytes: n.BytesSent,
						rxPackets: n.PacketsRecv, txPackets: n.PacketsSent,
						rxErrors: n.Errin, txErrors: n.Errout,
						rxDropped: n.Dropin,
					}
					break
				}
			}
			nicCur[name] = ioCounters{
				rxBytes: ct.rxBytes, txBytes: ct.txBytes,
				rxPackets: ct.rxPackets, txPackets: ct.txPackets,
				rxErrors: ct.rxErrors, txErrors: ct.txErrors,
				rxDropped: ct.rxDropped,
			}

			m := agentproto.NICMetric{Name: name}
			if dt > 0 {
				if prev, ok := c.prevNIC(name); ok {
					m.RXBytesPerSec = rate(ct.rxBytes, prev.rxBytes, dt)
					m.TXBytesPerSec = rate(ct.txBytes, prev.txBytes, dt)
					m.RXPacketsPerSec = rate(ct.rxPackets, prev.rxPackets, dt)
					m.TXPacketsPerSec = rate(ct.txPackets, prev.txPackets, dt)
					m.RXErrorsPerSec = rate(ct.rxErrors, prev.rxErrors, dt)
					m.TXErrorsPerSec = rate(ct.txErrors, prev.txErrors, dt)
					m.RXDroppedPerSec = rate(ct.rxDropped, prev.rxDropped, dt)
				}
			}
			nicOut = append(nicOut, m)
		}
		s.NICs = nicOut

		// 说明：协议里**没有** nic 合计字段，core 侧由 NICs 求和得到
		// nic_rx/tx_bytes_sec（见 latest.go projectLatest 与 downsample.go）。
		// 故这里只维护逐网卡条目，不本地累加 —— 累加值没有消费者。
	}

	// 保存本帧基线，供下一帧差量使用
	c.setBaselines(diskCur, nicCur, now)
}

// elapsed 返回距上次基线的秒数；无基线或非正值时返回 0（调用方据此跳过速率计算）。
func (c *Collector) elapsed(now time.Time) float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.prevTS.IsZero() {
		return 0
	}
	return secSince(c.prevTS, now)
}

// setBaselines 保存本帧两个计数器表的读数。
//
// 保留旧值中「本帧没出现的设备」：热插拔/临时卸载后重新出现时，
// 如果基线被清掉，那台设备又要等一帧才有速率。
func (c *Collector) setBaselines(disk, nic map[string]ioCounters, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, v := range disk {
		c.prevIO[k] = v
	}
	for k, v := range nic {
		c.prevNet[k] = v
	}
	c.prevTS = now
}

// prevDisk / prevNIC 读取上一帧基线（锁内访问，故走方法而不是直接读字段）。
func (c *Collector) prevDisk(name string) (ioCounters, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.prevIO[name]
	return v, ok
}

func (c *Collector) prevNIC(name string) (ioCounters, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.prevNet[name]
	return v, ok
}

// rate 由累计计数器差量求每秒速率。
//
// 返回 0 表示「本帧无有效速率」，与「速率为 0」在协议里无法区分（非指针字段），
// 故对**首帧/回绕**这类「不知道」的情形，调用方选择不填该字段（保持 0）
// 而不是硬造一个值 —— 这是 spec 「首帧整体省略该条目」的落地。
func rate(cur, prev uint64, dt float64) float64 {
	if dt <= 0 {
		return 0
	}
	// 计数器回绕/重置（重启、网卡重载）：Δ 为负说明 baseline 失效，
	// 此时应重置基线而不是给出一个巨大的负数速率。
	if cur < prev {
		return 0
	}
	delta := float64(cur - prev)
	v := delta / dt
	if v < 0 || v > 1e15 { // 明显不合理的值（如刚重启后的首次差量）一律归 0
		return 0
	}
	return round2(v)
}

// ── 过滤规则 ───────────────────────────────────────────

// isPseudoFS 判断文件系统类型是否属于「不是磁盘」的那类。
func isPseudoFS(fstype string) bool {
	if fstype == "" {
		return false
	}
	low := strings.ToLower(fstype)
	for _, p := range pseudoFSPrefixes {
		if strings.HasPrefix(low, p) {
			return true
		}
	}
	return false
}

// isPseudoMount 判断挂载点是否属于运行时/容器目录。
func isPseudoMount(mount string) bool {
	for _, p := range pseudoMountPrefixes {
		if mount == p || strings.HasPrefix(mount, p+"/") {
			return true
		}
	}
	return false
}

// isPartitionName 判断块设备名是不是分区（而非整盘）。
func isPartitionName(name string) bool {
	return reSDNum.MatchString(name) || rePartP.MatchString(name)
}

// isVirtualNIC 判断网卡是否是虚拟/回环接口。
//
// 回环必须剔除：它承载所有本机进程间流量，把它算进「网卡接收速率」会让
// 一台完全空闲的机器显示出可观的流量。
//
// 两层判断缺一不可：
//
//   - **内核标志**（loopbacks，来自 net.Interfaces 的 FlagLoopback）是权威依据，
//     能覆盖被人为改名的回环接口。
//   - **名字兜底**：内核并不保证把回环语义的设备都标记出来 ——
//     实测 WSL 的 `loopback0` 上报 ARPHRD_ETHER（type=1）而非 LOOPBACK，
//     于是它带着少量真实流量混进列表，成为纯噪音。按名字再兜一层。
func isVirtualNIC(name string, loopbacks map[string]bool) bool {
	if loopbacks[name] {
		return true
	}
	if name == "lo" || strings.HasPrefix(name, "lo:") ||
		strings.HasPrefix(name, "loopback") {
		return true
	}
	for _, p := range []string{
		"docker", "veth", "br-", "virbr", "tun", "tap", "wg", "vnet",
		"flannel", "cni", "cali", "kube", "dummy", "sit", "ip6tnl", "bond",
	} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// loopbackIfaces 返回内核标记为 LOOPBACK 的接口名集合。
// 取不到时返回空集合（退化为只按名字过滤，不会误杀）。
func loopbackIfaces() map[string]bool {
	out := make(map[string]bool)
	ifaces, err := stdnet.Interfaces()
	if err != nil {
		return out
	}
	for _, i := range ifaces {
		if i.Flags&stdnet.FlagLoopback != 0 {
			out[i.Name] = true
		}
	}
	return out
}

// round2 保留两位小数；非有限值与负数归 0。
//
// 用 math.Round 而不是 `int64(v*100+0.5)`：后者在 v 很大时
// v*100 会**溢出 int64**，转换结果是实现相关的（amd64 上会变成负数），
// 于是「一个巨大的速率」会被舍入成负数 —— 而负速率会被 Validate 拒掉整帧。
// math.Round 全程走浮点，不会溢出。
//
// 已知精度边界：恰好落在 .xx5 上的值（如 1.005）因 float64 无法精确表示，
// 可能舍入到下一位（1.00 而非 1.01）。对本场景（百分比/GB/速率）
// 影响在末位千分之一以内，不值得引入十进制库。
func round2(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return 0
	}
	return math.Round(v*100) / 100
}
