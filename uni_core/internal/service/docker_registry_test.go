package service

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"

	"gorm.io/gorm"
)

// fakeRegistryRepo 是 RegistryCredentialRepo 的内存替身。
//
// 它保留的是**落库前的实体行**（含密文盒）：这样测试既能断言「库里的形态」
// （密码不出现、密文盒各不重复），又能驱动 ResolveAuth 的真实解密路径。
type fakeRegistryRepo struct {
	mu   sync.Mutex
	rows map[string]*entity.DockerRegistryCredential
}

func newFakeRegistryRepo() *fakeRegistryRepo {
	return &fakeRegistryRepo{rows: map[string]*entity.DockerRegistryCredential{}}
}

func (f *fakeRegistryRepo) FindByRegistry(_ context.Context, registry string) (*entity.DockerRegistryCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[registry]
	if !ok {
		return nil, gorm.ErrRecordNotFound
	}
	cp := *row
	return &cp, nil
}

func (f *fakeRegistryRepo) Create(_ context.Context, row *entity.DockerRegistryCredential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *row
	f.rows[row.Registry] = &cp
	return nil
}

func (f *fakeRegistryRepo) Save(_ context.Context, row *entity.DockerRegistryCredential) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.rows[row.Registry]
	if !ok {
		return gorm.ErrRecordNotFound
	}
	cur.Username, cur.PasswordEnc, cur.Remark = row.Username, row.PasswordEnc, row.Remark
	return nil
}

func (f *fakeRegistryRepo) Delete(_ context.Context, registry string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.rows[registry]; !ok {
		return gorm.ErrRecordNotFound
	}
	delete(f.rows, registry)
	return nil
}

func (f *fakeRegistryRepo) List(_ context.Context) ([]entity.DockerRegistryCredential, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]entity.DockerRegistryCredential, 0, len(f.rows))
	for _, r := range f.rows {
		out = append(out, entity.DockerRegistryCredential{
			Registry: r.Registry, Username: r.Username, PasswordEnc: r.PasswordEnc, Remark: r.Remark,
		})
	}
	return out, nil
}

// fakeKeyGetter 是 RegistryMasterKeyGetter 的替身：主密钥可注入/可抽走
// （抽走 = 模拟线上「主密钥未配置」的 fail-closed 路径）。
type fakeKeyGetter struct{ val string }

func (f *fakeKeyGetter) GetString(_ context.Context, key, defaultVal string) string {
	if key == RegistryMasterKeyConfigKey && f.val != "" {
		return f.val
	}
	return defaultVal
}

// testMasterKey 是一条合法的 32 字节测试主密钥（hex 形式）。
func testMasterKey() string { return hex.EncodeToString([]byte(strings.Repeat("mk", 16))) }

func newRegistrySvc(repo RegistryCredentialRepo, key string) *DockerRegistryService {
	return NewDockerRegistryService(repo, &fakeKeyGetter{val: key}, nil)
}

// req 是创建请求的测试工具。
func req(registry, user, pass, remark string) *request.DockerRegistrySaveReq {
	return &request.DockerRegistrySaveReq{Registry: registry, Username: user, Password: pass, Remark: remark}
}

// TestRegistryCreateListResolve：创建 → 列表（掩码）→ ResolveAuth 解密往返；
// 落库行是密文且不含明文字面量。
func TestRegistryCreateListResolve(t *testing.T) {
	repo := newFakeRegistryRepo()
	svc := newRegistrySvc(repo, testMasterKey())
	const secret = "Sup3r-Secret-仓库密码"

	item, err := svc.Create(context.Background(), req("harbor.example.com", "robot$ci", secret, "内部仓库"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if item.Password != response.DockerRegistryPasswordMask {
		t.Fatalf("创建响应的密码必须是掩码 %q，实际 %q", response.DockerRegistryPasswordMask, item.Password)
	}
	if item.Registry != "harbor.example.com" {
		t.Fatalf("registry 应规范化写入，实际 %q", item.Registry)
	}
	row, err := repo.FindByRegistry(context.Background(), "harbor.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row.PasswordEnc, secret) {
		t.Fatal("落库的密文盒不得包含明文字面量（绝不明文落库）")
	}
	if row.PasswordEnc == secret {
		t.Fatal("密码必须加密落库（密文盒 ≠ 明文）")
	}

	user, pass, found, err := svc.ResolveAuth(context.Background(), "HARBOR.example.com ")
	if err != nil || !found {
		t.Fatalf("ResolveAuth: found=%v err=%v", found, err)
	}
	if user != "robot$ci" || pass != secret {
		t.Fatalf("解出的三元组与写入不一致: %q/%q", user, pass)
	}

	list, err := svc.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list.List) != 1 || list.List[0].Password != response.DockerRegistryPasswordMask {
		t.Fatalf("列表必须恰好一条且密码恒为掩码: %+v", list.List)
	}
	raw, _ := json.Marshal(list)
	if strings.Contains(string(raw), secret) {
		t.Fatal("列表响应 JSON 不得包含明文字面量")
	}
}

// TestRegistryCiphertextsDiffer：同一明文两次落库（两条记录/更新）的密文盒
// 必须不同 —— nonce 唯一性在服务层的再钉一次。
func TestRegistryCiphertextsDiffer(t *testing.T) {
	repo := newFakeRegistryRepo()
	svc := newRegistrySvc(repo, testMasterKey())
	if _, err := svc.Create(context.Background(), req("a.example.com", "u", "same-pass", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), req("b.example.com", "u", "same-pass", "")); err != nil {
		t.Fatal(err)
	}
	rows, _ := repo.List(context.Background())
	if len(rows) != 2 || rows[0].PasswordEnc == rows[1].PasswordEnc {
		t.Fatal("同一明文的不同记录必须产生不同密文盒（nonce 唯一性）")
	}
}

// TestRegistryUpdateRequiresPassword：更新必须重输密码（空密码 400）——
// 服务没有任何「读回旧密码」的路径，请求里不带就是参数错误。
func TestRegistryUpdateRequiresPassword(t *testing.T) {
	repo := newFakeRegistryRepo()
	svc := newRegistrySvc(repo, testMasterKey())
	if _, err := svc.Create(context.Background(), req("h.example.com", "u", "old-pass", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Update(context.Background(), req("h.example.com", "u2", "", "改备注")); err == nil {
		t.Fatal("空密码更新必须被拒（不接受读回再提交）")
	}
	item, err := svc.Update(context.Background(), req("h.example.com", "u2", "new-pass", "改备注"))
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if item.Username != "u2" || item.Remark != "改备注" {
		t.Fatalf("更新未生效: %+v", item)
	}
	_, pass, _, _ := svc.ResolveAuth(context.Background(), "h.example.com")
	if pass != "new-pass" {
		t.Fatalf("更新后解出的密码应为新密码,实际 %q", pass)
	}
	// 更新不存在的凭据 → 404。
	if _, err := svc.Update(context.Background(), req("no.example.com", "u", "p", "")); err == nil {
		t.Fatal("更新不存在的凭据必须报 404")
	}
}

// TestRegistryDeleteInvalidates：删除即失效 —— 再查 NotFound、ResolveAuth
// found=false、同键可重建。
func TestRegistryDeleteInvalidates(t *testing.T) {
	repo := newFakeRegistryRepo()
	svc := newRegistrySvc(repo, testMasterKey())
	if _, err := svc.Create(context.Background(), req("h.example.com", "u", "p", "")); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(context.Background(), "h.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := svc.ResolveAuth(context.Background(), "h.example.com"); found || err != nil {
		t.Fatalf("删除后必须不再命中: found=%v err=%v", found, err)
	}
	if _, err := svc.Create(context.Background(), req("h.example.com", "u", "new-p", "")); err != nil {
		t.Fatalf("删除后同键应可重建: %v", err)
	}
	if err := svc.Delete(context.Background(), "no.example.com"); err == nil {
		t.Fatal("删除不存在的凭据必须报 404")
	}
}

// TestRegistryResolveUnmatched：未匹配凭据 found=false 且 err=nil —— 调用方
// （受理链路）据此给出「没有这个仓库的凭据」的结论句。
func TestRegistryResolveUnmatched(t *testing.T) {
	svc := newRegistrySvc(newFakeRegistryRepo(), testMasterKey())
	_, _, found, err := svc.ResolveAuth(context.Background(), "ghcr.io")
	if err != nil || found {
		t.Fatalf("未匹配必须 found=false 且无错,实际 found=%v err=%v", found, err)
	}
}

// TestRegistryMasterKeyMissingFailClosed：主密钥缺失时创建与解析都必须显式失败
// （fail-closed —— 拿着不存在的密钥把密码写进库或解出错误值都不可接受）。
func TestRegistryMasterKeyMissingFailClosed(t *testing.T) {
	repo := newFakeRegistryRepo()
	if _, err := newRegistrySvc(repo, "").Create(context.Background(), req("h.example.com", "u", "p", "")); err == nil {
		t.Fatal("主密钥缺失时创建必须失败")
	}
	// 手动塞一条密文（模拟密钥轮换后失修的旧密文）：解析必须报错而不是给错值。
	repo.rows["old.example.com"] = &entity.DockerRegistryCredential{
		Registry: "old.example.com", Username: "u", PasswordEnc: "AAAA",
	}
	if _, _, found, err := newRegistrySvc(repo, "").ResolveAuth(context.Background(), "old.example.com"); found || err == nil {
		t.Fatalf("主密钥缺失时解析必须失败: found=%v err=%v", found, err)
	}
}

// TestRegistrySaveReqValidation：registry 形态/用户名/密码的入参校验。
func TestRegistrySaveReqValidation(t *testing.T) {
	svc := newRegistrySvc(newFakeRegistryRepo(), testMasterKey())
	bad := []*request.DockerRegistrySaveReq{
		req("https://h.example.com", "u", "p", ""),               // 带协议头
		req("h.example.com/img", "u", "p", ""),                   // 带镜像路径
		req("h.example.com", " ", "p", ""),                       // 空用户名
		req("h.example.com", "u", "", ""),                        // 空密码
		req("h.example.com", "u", strings.Repeat("x", 4097), ""), // 超长密码
	}
	for _, r := range bad {
		if _, err := svc.Create(context.Background(), r); err == nil {
			t.Fatalf("非法请求不该通过: %+v", r)
		}
	}
	if _, err := svc.Create(context.Background(), req("h.example.com", "u", "p", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(context.Background(), req("H.Example.com", "u2", "p2", "")); err == nil {
		t.Fatal("大小写不同的同仓库必须撞唯一键（409 语义）")
	} else if !strings.Contains(err.Error(), "已有凭据") {
		t.Fatalf("重复创建应是冲突结论句,实际 %v", err)
	}
}

// TestRegistryPasswordNeverInAnyReadPath：全读路径（列表 + 创建/更新的响应）
// 序列化后都不含密码字面量 —— 掩码是结构保证,不是「写的时候注意点」。
func TestRegistryPasswordNeverInAnyReadPath(t *testing.T) {
	repo := newFakeRegistryRepo()
	svc := newRegistrySvc(repo, testMasterKey())
	const secret = "never-leak-this-password"
	created, err := svc.Create(context.Background(), req("h.example.com", "u", secret, "普通备注"))
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]any{
		"create 响应": created,
	} {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s 不得包含密码字面量", name)
		}
	}
	updated, _ := svc.Update(context.Background(), req("h.example.com", "u", secret, ""))
	list, _ := svc.List(context.Background())
	for name, v := range map[string]any{"update 响应": updated, "list 响应": list} {
		raw, _ := json.Marshal(v)
		if strings.Contains(string(raw), secret) {
			t.Fatalf("%s 不得包含密码字面量", name)
		}
	}
	// 掩码是常量：不随密码长度变化（长度也不泄漏）。
	if list.List[0].Password != "****" {
		t.Fatalf("掩码必须是常量 ****,实际 %q", list.List[0].Password)
	}
}

// apperror 形态抽查：冲突/404 的错误码落在预期的业务码族（信封由 handler 统一包）。
func TestRegistryErrorShapes(t *testing.T) {
	svc := newRegistrySvc(newFakeRegistryRepo(), testMasterKey())
	if err := svc.Delete(context.Background(), "no.example.com"); err == nil {
		t.Fatal("删除不存在的凭据应报错")
	} else if _, ok := err.(*apperror.AppError); !ok {
		t.Fatalf("删除不存在应给 AppError,实际 %T", err)
	}
}
