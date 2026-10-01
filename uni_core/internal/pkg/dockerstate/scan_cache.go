package dockerstate

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	goredis "github.com/redis/go-redis/v9"

	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"
)

// ── 镜像漏洞扫描缓存（P3·安全面）────────────────────────────────────────
//
// trivy 一次扫描是分钟级操作（首扫还要下载漏洞库），而同一镜像内容在一天内重复
// 「打开页面再看一眼」是常态 —— 本缓存把扫描报告按**镜像内容键**存 24 小时，
// 命中时受理路径秒回缓存结果（见 service/docker_cmd.go 的快路径）。
//
// 键设计（spec §4.3.2 键清单之外的新键，与 StatsHistoryKeyPrefix 同族要写清理由）：
//
//	docker:scan:<imageID>   STRING —— 一份完整 DockerScanReport（JSON，
//	                               与 result.payload 同形）
//
// 三条口径：
//   - **内容寻址**：键是镜像的完整 ID（sha256:<64hex>，快照 DockerImage.ID 与
//     daemon inspect 同口径）而不是 tag —— tag 换内容（重新 build/pull）ID 必变，
//     自然走向一次新扫描；tag 换名（同内容挂新 tag）命中同一份缓存，答案不变
//     （漏洞由内容决定，与挂什么名无关）。同一内容在**多台主机**上也共享缓存：
//     报告是内容的事实，不是某台主机的事实；
//   - **24h TTL**（ScanCacheTTL）：漏洞库每天更新，CVE 的世界也是每天变 —— 一天
//     是「答案大概还有效」与「重新扫一次（分钟级）」之间的平衡点。手动重扫
//     （force）**刻意不做**：force 在协议上是「越过保护清单」的开关，把它复用成
//     「绕过缓存」会让两个语义在 options 上撞车；默认走缓存秒回 + 报告里的
//     scanned_at 如实说「扫描于 N 小时前」，要新鲜数据等 TTL —— 真正需要
//     「立刻重扫」的场景出现时，再为它设计显式的口径；
//   - **缓存 DTO 与报告同形**：值就是 DockerScanReport（与 agent 直发的
//     result.payload 同一形状）—— 命中回放与直发对前端只有 scanned_at 一个字段
//     之差，不需要两套解析。
//
// 设备删除**不连带清理**（与 stats-history 的冻结序列同一纪律）：键不带 deviceID
// （内容寻址，天然跨主机共享），无法从设备枚举；24h TTL 是唯一且足够的收口 ——
// 删除主机的残留最多活一天，而报告内容不含任何主机私有信息。

const (
	// ScanCacheKeyPrefix 是扫描缓存键族前缀（运维按 `docker:scan:*` 定位全部缓存）。
	ScanCacheKeyPrefix = "docker:scan:"
	// ScanCacheTTL 是缓存条目的存活窗口（从写入起算，见上方键设计的口径说明）。
	ScanCacheTTL = 24 * time.Hour
)

// scanImageIDRe 是镜像 ID 的唯一合法形态（完整 sha256 + 64 位小写 hex）。
//
// 它同时是 Redis 键的组成部分与 agent 报告里的 image_id 值：两侧都过这一把
// 白名单，畸形值（截断 ID、别的算法前缀、注入形态）进不了键 —— 键空间只有
// 「sha256:hex64」一种形状，扫描与清点都可控。
var scanImageIDRe = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// IsDockerScanImageID 报告 s 是否是扫描缓存接受的镜像内容键
// （sha256:<64 位小写 hex>）。
func IsDockerScanImageID(s string) bool { return scanImageIDRe.MatchString(s) }

// ScanCacheStore 读写镜像扫描缓存。
type ScanCacheStore struct {
	rdb goredis.UniversalClient
}

// NewScanCacheStore 构造扫描缓存。
func NewScanCacheStore(rdb goredis.UniversalClient) *ScanCacheStore {
	return &ScanCacheStore{rdb: rdb}
}

// Save 写入一份扫描报告（24h TTL）。
//
// at 是 core 侧的收帧/写缓时刻：**重盖** report.ScannedAt —— agent 时钟可能偏
// （.106 实测快 8 小时，stats-history 的同一条纪律），「扫描于 N 小时前」的
// 陈旧度陈述以 core 时钟为准；agent 填的值只活在它直发的那一次 result 里。
//
// 形态闸：imageID 必须过 IsDockerScanImageID（见 scanImageIDRe 的注释），
// 报告字段不在本层校验语义（它已通过 result 通道的 256KB 上限；内容归一化是
// agent 的职责，缓存只做「忠实回放」）。
func (s *ScanCacheStore) Save(ctx context.Context, imageID string, report *agentproto.DockerScanReport, at time.Time) error {
	if !IsDockerScanImageID(imageID) || report == nil {
		return errScanCacheBadKey
	}
	r := *report
	r.ScannedAt = at.Unix()
	b, err := json.Marshal(&r)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, ScanCacheKeyPrefix+imageID, b, ScanCacheTTL).Err()
}

// Get 读取一份扫描报告；没有（从未扫过 / TTL 已过 / 键损坏）返回 (nil, nil)。
//
// 「没有缓存」是正常答案（受理路径回落到真扫描），与 Redis 故障（err != nil，
// 受理路径也回落但要有日志）刻意分开 —— 前者是数据面的事实，后者是基础设施
// 的旁证（「索引只是加速器，判定以记录为准」的同一纪律）。单条值损坏
// （JSON 解不开）按「没有」处理：缓存不可用时最坏是重扫一次，不能把
// 「缓存坏了」升级成「扫描不可用」。
func (s *ScanCacheStore) Get(ctx context.Context, imageID string) (*agentproto.DockerScanReport, error) {
	if !IsDockerScanImageID(imageID) {
		return nil, nil
	}
	b, err := s.rdb.Get(ctx, ScanCacheKeyPrefix+imageID).Bytes()
	if err != nil {
		if err == goredis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var report agentproto.DockerScanReport
	if err := json.Unmarshal(b, &report); err != nil {
		return nil, nil
	}
	return &report, nil
}

// errScanCacheBadKey 是形态闸的拒绝（imageID 不是 sha256:<64hex> —— 只可能是
// 装配缺陷或协议被绕过，调用方记日志即可；不是运行时状况，不重试）。
var errScanCacheBadKey = errors.New("scan cache: 镜像内容键形态不合法")
