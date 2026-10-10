package repository

import (
	"context"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
)

// DockerHostDiskRepo.LatestWideDisk 的取数口径测试：**每设备最近一条**宽表行（按
// max(bucket_ts)），未上报设备缺席（不是 0 值行），空 ids 发空 map 不查询。
//
// 用真 sqlite（newMetricTestDB）而不是替身：这里要钉的正是 SQL 的形状（派生表取
// 最近桶再 JOIN 回主表）—— 替身证明不了「挑的是最近的那条」。
func TestDockerHostDiskLatestWideDisk(t *testing.T) {
	db := newMetricTestDB(t)
	repo := NewDockerHostDiskRepo(db)
	ctx := context.Background()

	// 设备 1 有两个桶（1000 → 2000，最近=2000）；设备 2 一个桶（1500）；设备 3 无行。
	rows := []*entity.DeviceMetricWide{
		{DeviceID: 1, BucketTS: 1000, Samples: 1, DiskUsedPercent: ptr(10), DiskTotalGB: ptr(100), DiskUsedGB: ptr(10)},
		{DeviceID: 1, BucketTS: 2000, Samples: 1, DiskUsedPercent: ptr(20), DiskTotalGB: ptr(200), DiskUsedGB: ptr(40)},
		{DeviceID: 2, BucketTS: 1500, Samples: 1, DiskUsedPercent: ptr(30), DiskTotalGB: ptr(300), DiskUsedGB: ptr(90)},
	}
	for _, r := range rows {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	got, err := repo.LatestWideDisk(ctx, []uint64{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[3]; ok {
		t.Fatalf("未上报指标的设备必须缺席（不是 0 值行 ——「没有数据」≠「磁盘 0%%」）: %+v", got[3])
	}
	r1, ok := got[1]
	if !ok || r1.DiskUsedPercent == nil || *r1.DiskUsedPercent != 20 ||
		r1.DiskTotalGB == nil || *r1.DiskTotalGB != 200 || r1.DiskUsedGB == nil || *r1.DiskUsedGB != 40 {
		t.Fatalf("设备 1 必须取最近一条（bucket 2000，不是 1000）: %+v", r1)
	}
	if r2, ok := got[2]; !ok || r2.DiskUsedPercent == nil || *r2.DiskUsedPercent != 30 ||
		r2.DiskTotalGB == nil || *r2.DiskTotalGB != 300 {
		t.Fatalf("设备 2 取唯一那条: %+v", r2)
	}

	// 空 ids：空 map、不报错（不发 `IN ()` 这种无意义查询）。
	empty, err := repo.LatestWideDisk(ctx, nil)
	if err != nil || len(empty) != 0 {
		t.Fatalf("空 ids 应返回空 map: %+v %v", empty, err)
	}
}
