package service

import (
	"context"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/repository"
)

// TestUpgradePhaseOf 钉住列表相位的**推导规则**（读时算、不落库）。
//
// 最容易写错的是「等待开工」：下发时就会插一条 pending 尝试行（设备可能还没上线），
// 若按「有未终结尝试 = 升级中」处理，一台刚下发、根本没开始的机器会显示成正在升级
// —— 运维会去等它，而它连 hello 都还没发。
func TestUpgradePhaseOf(t *testing.T) {
	dev := &entity.Device{AgentVersion: "0.2.4"}
	cases := []struct {
		name      string
		target    string
		openState string
		want      string
	}{
		{"无目标", "", "", ""},
		{"版本与目标一致 = 已达成", "0.2.4", "", "achieved"},
		{"有目标无尝试 = 待升级", "0.2.5", "", "pending"},
		{"尝试尚未开工 = 待升级", "0.2.5", entity.AttemptStatePending, "pending"},
		{"尝试已开工 = 升级中", "0.2.5", entity.AttemptStateDownloading, "running"},
		{"替换阶段仍算升级中", "0.2.5", entity.AttemptStateRestarting, "running"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			snap := DeviceUpgradeSnapshot{
				EffectiveTargetVersion: c.target,
				OpenAttemptState:       c.openState,
			}
			if got := upgradePhaseOf(dev, snap); got != c.want {
				t.Fatalf("相位 = %q，期望 %q", got, c.want)
			}
		})
	}
}

// TestDeviceListExposesUpgradeFields 钉住设备列表/详情的升级字段来自**升级域的同一份快照**
// （设备服务自己再算一份就是第二处口径）。
func TestDeviceListExposesUpgradeFields(t *testing.T) {
	ctx := context.Background()
	env := newUpgradeTestEnv(t, stubCfg{})
	env.seedRelease(t, "0.2.0", "linux", "amd64", true)
	dev := env.seedDevice(t, "0.1.0")

	// 设备服务注入升级域（生产装配同款）。
	deviceSvc := NewDeviceService(env.devices, repository.NewDeviceResourceRepository(env.db),
		stubPurger{}, stubLatestReader{}, stubCfg{}, env.svc, logger.NewNop())

	// 列表接口能跑通（升级字段在列表与详情走同一条组装路径）。
	if _, err := deviceSvc.List(ctx, nil); err != nil {
		t.Fatal(err)
	}
	detail, err := deviceSvc.GetByID(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.TargetVersion != "" || detail.UpgradePhase != "" {
		t.Fatalf("无目标时不应有相位: %+v", detail.DeviceListItem)
	}
	if !detail.AgentUpgradeSupported {
		t.Fatal("支持位应由设备行透出")
	}
	if detail.RollbackVersion != "" {
		t.Fatalf("无成功历史时回滚版本应为空: %q", detail.RollbackVersion)
	}

	// 固定到当前版本：相位应为 achieved（版本 == 目标），且标注为设备级指定。
	pin := true
	if _, err := env.svc.SetDeviceTarget(ctx, dev.ID, "0.1.0", pin, 0); err != nil {
		t.Fatal(err)
	}
	detail, err = deviceSvc.GetByID(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.TargetVersion != "0.1.0" || detail.TargetFromGlobal ||
		detail.UpgradePhase != "achieved" {
		t.Fatalf("固定到当前版本应显示已达成（设备级指定）: %+v", detail.DeviceListItem)
	}

	// 下发到 0.2.0：相位 pending（尝试已建但未开工）。
	if _, err := env.svc.SetDeviceTarget(ctx, dev.ID, "0.2.0", false, 0); err != nil {
		t.Fatal(err)
	}
	detail, err = deviceSvc.GetByID(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.TargetVersion != "0.2.0" || detail.UpgradePhase != "pending" {
		t.Fatalf("已下发未开工应为待升级: %+v", detail.DeviceListItem)
	}

	// 成功之后：相位 achieved + 一键回滚版本 = 起始版本（走真实握手时序：
	// 鉴权刷新版本 → 对账判成功）。
	if d := env.simulateHello(t, dev.ID, "0.2.0"); d != nil {
		t.Fatalf("已到达目标不应再下发指令: %+v", d)
	}
	detail, err = deviceSvc.GetByID(ctx, dev.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.UpgradePhase != "achieved" || detail.RollbackVersion != "0.1.0" {
		t.Fatalf("成功后应已达成且给出回滚目标: %+v", detail)
	}
	if detail.UpgradeResult != entity.DeviceUpgradeAchieved {
		t.Fatalf("终态应透出为已达成: %d", detail.UpgradeResult)
	}
}
