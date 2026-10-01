package service

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/request"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/dto/response"
	"github.com/tangwy-t/UniCenter/uni_core/internal/model/entity"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/apperror"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/logger"
	"github.com/tangwy-t/UniCenter/uni_core/internal/pkg/seccrypt"
	agentproto "github.com/tangwy-t/UniCenter/uni_protocol"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// RegistryMasterKeyConfigKey 是仓库凭据主密钥的 sys 配置键。
//
// 收敛成常量的理由同 jwt.SecretConfigKey：键名字面量散布多处时，任一处的
// 拼写漂移都会让读取点静默拿到空密钥（本服务对空密钥 fail-closed，表现是
// 「凭据功能整体不可用」，而这类故障最难排查）。
//
// 键名刻意含 `secret`：配置 API 的敏感键掩码（util.IsSensitiveConfigKey）
// 自动对列表/详情/按键查询生效 —— 权重与 jwt 签名密钥同档（明文落库的
// 密码全部依赖它解封），绝不通过配置读路径外泄。
const RegistryMasterKeyConfigKey = "sys.docker.registry.secret"

// RegistryCredentialRepo 是凭据存储面（消费方窄接口，由 repository.DockerRegistryRepo 满足）。
type RegistryCredentialRepo interface {
	FindByRegistry(ctx context.Context, registry string) (*entity.DockerRegistryCredential, error)
	Create(ctx context.Context, row *entity.DockerRegistryCredential) error
	Save(ctx context.Context, row *entity.DockerRegistryCredential) error
	Delete(ctx context.Context, registry string) error
	List(ctx context.Context) ([]entity.DockerRegistryCredential, error)
}

// RegistryMasterKeyGetter 读取凭据主密钥所在配置（只读 GetString —— 与
// AgentConfigGetter 同构;限窄接口,谁也没有多余能力）。实现:*service.ConfigService。
type RegistryMasterKeyGetter interface {
	GetString(ctx context.Context, key, defaultVal string) string
}

// DockerRegistryService 是私有仓库凭据的管理面 + 受理注入面（4c）。
//
// 一把密钥、两个方向：
//   - 管理面（List/Create/Update/Delete）：密码**只进不出** —— 写请求里的明文
//     瞬时存在,进库前加密封盒;一切读路径只给掩码「****」;
//   - 注入面（ResolveAuth）：**只**被 image:pull 的受理路径调用（DockerCmdService.
//     Send 的 registry 分支）,是本进程唯一解密密文盒的地方 —— 解出的明文
//     直接进下发给 agent 的指令消息,此后不留任何副本。
type DockerRegistryService struct {
	repo RegistryCredentialRepo
	cfg  RegistryMasterKeyGetter
	log  logger.LoggerInterface
}

// NewDockerRegistryService 构造服务。
func NewDockerRegistryService(repo RegistryCredentialRepo, cfg RegistryMasterKeyGetter, log logger.LoggerInterface) *DockerRegistryService {
	return &DockerRegistryService{repo: repo, cfg: cfg, log: log}
}

// listItem 把一行记录折成响应项：密码一律掩码 —— 不是「脱敏」而是「结构上
// 没有明文字段」（response.DockerRegistryItem 唯一的密码槽位就是掩码常量）。
func listItem(row *entity.DockerRegistryCredential) response.DockerRegistryItem {
	return response.DockerRegistryItem{
		Registry:  row.Registry,
		Username:  row.Username,
		Remark:    row.Remark,
		Password:  response.DockerRegistryPasswordMask,
		CreatedAt: row.CreatedAt.Unix(),
	}
}

// List 返回全部凭据（掩码形态）。
func (s *DockerRegistryService) List(ctx context.Context) (*response.DockerRegistryListResp, error) {
	rows, err := s.repo.List(ctx)
	if err != nil {
		return nil, registryInternalErr(err)
	}
	out := &response.DockerRegistryListResp{List: make([]response.DockerRegistryItem, 0, len(rows))}
	for i := range rows {
		out.List = append(out.List, listItem(&rows[i]))
	}
	return out, nil
}

// Create 新建一条凭据：密码加密后落库（registry 规范化后进唯一键）。
func (s *DockerRegistryService) Create(ctx context.Context, req *request.DockerRegistrySaveReq) (*response.DockerRegistryItem, error) {
	if err := validateSaveReq(req); err != nil {
		return nil, err
	}
	registry := normalizeRegistry(req.Registry)
	if _, err := s.repo.FindByRegistry(ctx, registry); err == nil {
		return nil, apperror.Conflict("该仓库地址已有凭据")
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, registryInternalErr(err)
	}
	key, err := s.masterKey(ctx)
	if err != nil {
		return nil, err
	}
	box, err := seccrypt.Encrypt(key, []byte(req.Password))
	if err != nil {
		return nil, registryInternalErr(err)
	}
	row := &entity.DockerRegistryCredential{
		Registry:    registry,
		Username:    strings.TrimSpace(req.Username),
		PasswordEnc: box,
		Remark:      req.Remark,
	}
	if err := s.repo.Create(ctx, row); err != nil {
		return nil, registryInternalErr(err)
	}
	item := listItem(row)
	return &item, nil
}

// Update 更新既有凭据：registry 是**定位键**（唯一键不可改），用户名/密码/备注
// 整体重写。**密码必须重输** —— 本服务没有任何「读回旧密码」的能力，请求里
// 不带新密码就是参数错误（binding:required 之外，service 再挡一道空值）。
func (s *DockerRegistryService) Update(ctx context.Context, req *request.DockerRegistrySaveReq) (*response.DockerRegistryItem, error) {
	if err := validateSaveReq(req); err != nil {
		return nil, err
	}
	registry := normalizeRegistry(req.Registry)
	row, err := s.repo.FindByRegistry(ctx, registry)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, apperror.NotFound("该仓库没有凭据记录")
		}
		return nil, registryInternalErr(err)
	}
	key, err := s.masterKey(ctx)
	if err != nil {
		return nil, err
	}
	box, err := seccrypt.Encrypt(key, []byte(req.Password))
	if err != nil {
		return nil, registryInternalErr(err)
	}
	row.Username = strings.TrimSpace(req.Username)
	row.PasswordEnc = box
	row.Remark = req.Remark
	if err := s.repo.Save(ctx, row); err != nil {
		return nil, registryInternalErr(err)
	}
	// CreatedAt 保留原始值：Save 只碰 username/password_enc/remark 三列
	//（唯一键不可改），列表里「创建时间」的含义不因更新而漂移。
	item := listItem(row)
	return &item, nil
}

// Delete 删除一条凭据（删除即失效：不触发任何对账，下一单带该 registry 的
// 拉取在受理处就得到「没有这个仓库的凭据」的 400）。
func (s *DockerRegistryService) Delete(ctx context.Context, registry string) error {
	registry = normalizeRegistry(registry)
	err := s.repo.Delete(ctx, registry)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return apperror.NotFound("该仓库没有凭据记录")
		}
		return registryInternalErr(err)
	}
	return nil
}

// ResolveAuth 解出仓库的认证三元组 —— **唯一**解密路径（image:pull 受理）。
//
// found=false 且 err=nil 表示「没有这个仓库的凭据」（由调用方折成 400 结论句）；
// err!=nil 是主密钥缺失/密文损坏（调用方折成 500 结论句,不把细节带给用户）。
// 解出的明文**不要落日志、不要存任何结构** —— 调用方拿到即编入指令消息。
func (s *DockerRegistryService) ResolveAuth(ctx context.Context, registry string) (username, password string, found bool, err error) {
	registry = normalizeRegistry(registry)
	row, err := s.repo.FindByRegistry(ctx, registry)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", "", false, nil
		}
		return "", "", false, err
	}
	key, err := s.masterKey(ctx)
	if err != nil {
		return "", "", false, err
	}
	plain, err := seccrypt.Decrypt(key, row.PasswordEnc)
	if err != nil {
		// 密文损坏既要暴露给运维（日志有 registry 可定位）又要对用户收口：
		// 结论句由调用方给出「凭据不可用」，这里只记日志。
		s.log.Warn("registry credential box undecryptable", zap.String("registry", registry), zap.Error(err))
		return "", "", false, err
	}
	return row.Username, string(plain), true, nil
}

// masterKey 读取并校验主密钥（hex 形式，≥32 字节）。
//
// fail-closed：缺失/过短/非 hex 一律报错 —— 与其拿着一个弱密钥把明文写进库
//（或解出错误的值下发），不如让凭据写路径与拉取注入都显式失败。
func (s *DockerRegistryService) masterKey(ctx context.Context) ([]byte, error) {
	raw := s.cfg.GetString(ctx, RegistryMasterKeyConfigKey, "")
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) < seccrypt.KeyMinBytes {
		if s.log != nil {
			s.log.Error("docker registry master key missing or too short",
				zap.Int("key_bytes", len(decoded)))
		}
		return nil, apperror.Internal("仓库凭据主密钥未配置，凭据功能暂不可用")
	}
	return decoded, nil
}

// validateSaveReq 校验创建/更新请求的形态（registry 复用协议形态闸 ——
// 与 image:pull 的 options.registry 同一把尺,凭据键就不会出现「存的时候
// 合法、用的时候标不中」的两套口径）。尺寸闸与协议同档。
func validateSaveReq(req *request.DockerRegistrySaveReq) error {
	if !agentproto.IsDockerRegistryAddr(req.Registry) {
		return apperror.BadRequest("仓库地址格式不合法")
	}
	if strings.TrimSpace(req.Username) == "" || len(req.Username) > 255 {
		return apperror.BadRequest("用户名不合法")
	}
	if req.Password == "" || len(req.Password) > seccrypt.MaxPlaintextBytes {
		return apperror.BadRequest("密码不合法")
	}
	return nil
}

// normalizeRegistry 规范化唯一键：小写 + 去首尾空白（DNS 不分大小写；
// 用户写 Harbor.X 与 harbor.x 必须命中同一条凭据）。
func normalizeRegistry(registry string) string {
	return strings.ToLower(strings.TrimSpace(registry))
}

// internal 把存储/加密错误折成「内部错误」结论句（原因进错误链由 app.Error 记日志,
// 不把实现细节带给用户）。
func registryInternalErr(err error) error { return apperror.Internal("内部错误", err) }