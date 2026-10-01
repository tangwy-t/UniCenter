package entity

// DockerRegistryCredential 是一条私有镜像仓库凭据（4c 分发支柱）。
//
// **绝不明文落库**：PasswordEnc 是 AES-256-GCM 密文盒（base64(nonce‖ciphertext)，
// 见 pkg/seccrypt），明文的生命周期只有两段 —— 写路径（受理请求里的瞬时值，
// 加密后即弃）与拉取注入（core 解出后随指令瞬时下发 agent，agent 用完即弃）。
// 读取任何行都拿不到明文：列表/详情走掩码「****」，解密只发生在 image:pull 的
// 受理路径（service.DockerRegistryService.ResolveAuth 是唯一解密密文盒的调用方）。
//
// Registry 是唯一键（规范化为小写、去首尾空白）：凭据按仓库地址寻址，
// 与协议 DockerCmdOptions.Registry 同一把键（见 uni_protocol 的
// IsDockerRegistryAddr —— 不含协议头与镜像路径）。
type DockerRegistryCredential struct {
	BaseEntity
	Registry string `gorm:"column:registry;size:255;not null;uniqueIndex" json:"registry"`
	Username string `gorm:"column:username;size:255;not null" json:"username"`
	// PasswordEnc 是密码的密文盒（**只进不出明文**；有 json:"-" 防序列化带上）。
	PasswordEnc string `gorm:"column:password_enc;type:text;not null" json:"-"`
	Remark      string `gorm:"column:remark;size:512" json:"remark"`
}

func (DockerRegistryCredential) TableName() string { return "docker_registry_credential" }