package request

// DockerRegistrySaveReq 是仓库凭据的创建/更新请求（4c）。
//
// 创建与更新**同形**（更新按 registry 定位、必须重输密码 —— 凭据管理不提供
// 「读回再提交」的路径：密码除了掩码没有任何读回形态，见 response 的说明）。
type DockerRegistrySaveReq struct {
	// Registry 是仓库地址（唯一键：主机名/IP + 可选端口，不含协议头与镜像路径；
	// 形态校验在 service 复用协议 IsDockerRegistryAddr）。
	Registry string `json:"registry" binding:"required"`
	// Username 是仓库登录名。
	Username string `json:"username" binding:"required"`
	// Password 是仓库密码（明文仅在本次请求内瞬时存在；**每次创建/更新都重输**）。
	Password string `json:"password" binding:"required"`
	// Remark 是备注（可选）。
	Remark string `json:"remark"`
}