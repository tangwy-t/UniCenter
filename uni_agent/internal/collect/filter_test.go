package collect

import (
	"math"
	"testing"
)

// TestIsPseudoFS 验证伪文件系统被识别。
//
// 这些不是「磁盘」：把 tmpfs/overlay 算进磁盘合计，
// 会让「磁盘使用率」变成一个没有物理意义的数（例如容器里 overlay
// 往往是数十 GB 的镜像层，会把真实磁盘的占用比例彻底冲淡）。
func TestIsPseudoFS(t *testing.T) {
	pseudo := []string{
		"tmpfs", "devtmpfs", "proc", "sysfs", "cgroup", "cgroup2",
		"overlay", "squashfs", "ramfs", "mqueue", "debugfs", "tracefs",
		"securityfs", "nsfs", "fuse.portal", "shm", "binfmt_misc",
		// 大小写不应影响判定
		"TMPFS", "Overlay",
	}
	for _, fs := range pseudo {
		if !isPseudoFS(fs) {
			t.Errorf("isPseudoFS(%q) = false, 期望 true", fs)
		}
	}

	real := []string{"ext4", "xfs", "btrfs", "zfs", "apfs", "ntfs", "vfat", "nfs", ""}
	for _, fs := range real {
		if isPseudoFS(fs) {
			t.Errorf("isPseudoFS(%q) = true, 期望 false（这是真实文件系统）", fs)
		}
	}
}

// TestIsPseudoMount 验证运行时/容器挂载点被识别。
func TestIsPseudoMount(t *testing.T) {
	pseudo := []string{
		"/proc", "/proc/sys/fs/binfmt_misc", "/sys", "/sys/fs/cgroup",
		"/dev", "/dev/shm", "/run", "/run/lock", "/snap/core20/1974",
		"/var/lib/docker", "/var/lib/docker/overlay2",
		"/var/lib/kubelet/pods", "/var/lib/containerd/io.containerd.runtime",
	}
	for _, m := range pseudo {
		if !isPseudoMount(m) {
			t.Errorf("isPseudoMount(%q) = false, 期望 true", m)
		}
	}

	real := []string{"/", "/home", "/var/log", "/data", "/mnt/data", "/srv/www"}
	for _, m := range real {
		if isPseudoMount(m) {
			t.Errorf("isPseudoMount(%q) = true, 期望 false（这是真实挂载点）", m)
		}
	}
}

// TestIsPartitionName 验证分区被识别为「非整盘」。
//
// 分区与整盘**共享同一套 IO 计数器**，若都上报，
// 磁盘 IO 合计（Σ read + Σ write）会凭空翻倍 —— 页面上的吞吐量直接错一倍。
func TestIsPartitionName(t *testing.T) {
	partitions := []string{
		"sda1", "sda2", "sdb12", "hda1", "vda1", "xvda1",
		"nvme0n1p1", "nvme0n1p2", "mmcblk0p1", "md0p1", "loop0p1",
	}
	for _, name := range partitions {
		if !isPartitionName(name) {
			t.Errorf("isPartitionName(%q) = false, 期望 true（这是分区）", name)
		}
	}

	whole := []string{
		"sda", "sdb", "hda", "vda", "xvda",
		"nvme0n1", "mmcblk0", "md0", "dm-0",
	}
	for _, name := range whole {
		if isPartitionName(name) {
			t.Errorf("isPartitionName(%q) = true, 期望 false（这是整盘）", name)
		}
	}
}

// TestIsVirtualNIC 验证虚拟/回环网卡被识别。
//
// 回环最关键：它承载所有本机进程间通信，一台完全空闲的机器
// 也会因为服务间调用产生可观的「网卡流量」。不剔除会让人误判网络负载。
func TestIsVirtualNIC(t *testing.T) {
	none := map[string]bool{}
	virtual := []string{
		"lo", "loopback0", "docker0", "veth034c537", "br-14ef6e108176",
		"virbr0", "tun0", "tap0", "wg0", "vnet0", "flannel.1",
		"cni0", "cali1234", "kube-ipvs0", "dummy0",
	}
	for _, name := range virtual {
		if !isVirtualNIC(name, none) {
			t.Errorf("isVirtualNIC(%q) = false, 期望 true", name)
		}
	}

	real := []string{"eth0", "eth1", "ens33", "enp0s3", "wlan0", "en0"}
	for _, name := range real {
		if isVirtualNIC(name, none) {
			t.Errorf("isVirtualNIC(%q) = true, 期望 false（这是真实网卡）", name)
		}
	}
}

// TestIsVirtualNICRespectsKernelFlag 验证内核 LOOPBACK 标志优先。
//
// 名字可以被改（`ip link set lo name myloop`），故必须信任内核标志，
// 不能只靠名字黑名单。
func TestIsVirtualNICRespectsKernelFlag(t *testing.T) {
	// 一个名字看起来完全正常的接口，但内核标记它是回环
	loopbacks := map[string]bool{"weird-name": true}
	if !isVirtualNIC("weird-name", loopbacks) {
		t.Error("内核标记为 LOOPBACK 的接口应被剔除（名字不可信）")
	}
	// 未标记的正常接口不受影响
	if isVirtualNIC("eth0", loopbacks) {
		t.Error("未标记 LOOPBACK 的 eth0 不应被剔除")
	}
}

// TestRound2 验证两位小数舍入、负数归零、以及大数不溢出。
//
// 大数那条最关键：早期的 `int64(v*100+0.5)` 写法在 v 极大时
// v*100 溢出 int64，转换结果是实现相关的（amd64 上变负数），
// 会把一个巨大的速率舍入成负数 —— 而负速率会让 Validate 拒掉**整帧**。
func TestRound2(t *testing.T) {
	cases := []struct{ in, want float64 }{
		{1.004, 1.0},
		{1.006, 1.01},
		{50.555, 50.56},
		{0, 0},
		{-1, 0},
		{100, 100},
		{math.NaN(), 0},
		{math.Inf(1), 0},
	}
	for _, tc := range cases {
		if got := round2(tc.in); got != tc.want {
			t.Errorf("round2(%v) = %v, 期望 %v", tc.in, got, tc.want)
		}
	}

	// 大数：必须仍是正数且有限（不溢出成负数/NaN）
	big := round2(1e15)
	if !(big > 0) || math.IsInf(big, 0) || math.IsNaN(big) {
		t.Errorf("round2(1e15) = %v，溢出成了非正数/非有限值", big)
	}
}
