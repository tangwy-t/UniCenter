package migrations

import (
	"gorm.io/gorm"

	"github.com/tangwy-t/webmanager-server/internal/model/entity"
	"github.com/tangwy-t/webmanager-server/internal/pkg/migration"
	"github.com/tangwy-t/webmanager-server/internal/pkg/util"
)

func init() {
	migration.Register(migration.Migration{
		Version:     5,
		Description: "初始化字典类型与数据",
		Up:          seedDict,
	})
}

// dictTypeDef 描述一个字典类型；Data 是其下的数据项（type_id 用类型变量回填）。
type dictTypeDef struct {
	Code   string
	Name   string
	Data   []dictDataDef
	Remark string
}

// dictDataDef 描述一条字典数据；Class 为空表示不写 list_class。
type dictDataDef struct {
	Label     string
	Value     string
	Class     string
	Sort      int
	IsDefault bool
}

// dictDefinitions 是全部字典类型与数据的唯一来源。顺序即创建顺序。
// 说明：
//   - sys_notice_publish_type 采用“通知接收范围”值域（targetType 0-3），
//     合并了历史 v021 修正后的最终语义；
//   - list_class 直接作为字段值写入（合并历史 v015 的样式预填）；
//   - sys_opt_result_code 的 10001 文案取 v017 修正后的“认证失败”。
var dictDefinitions = []dictTypeDef{
	{Code: "sys_normal_disable", Name: "系统开关", Data: []dictDataDef{
		{Label: "正常", Value: "1", Class: "success", Sort: 1, IsDefault: true},
		{Label: "停用", Value: "0", Class: "danger", Sort: 2},
	}},
	{Code: "sys_show_hide", Name: "显示状态", Data: []dictDataDef{
		{Label: "显示", Value: "1", Class: "primary", Sort: 1, IsDefault: true},
		{Label: "隐藏", Value: "0", Class: "info", Sort: 2},
	}},
	{Code: "sys_yes_no", Name: "系统是否", Data: []dictDataDef{
		{Label: "是", Value: "1", Class: "primary", Sort: 1, IsDefault: true},
		{Label: "否", Value: "0", Class: "info", Sort: 2},
	}},
	{Code: "sys_notice_type", Name: "通知类型", Data: []dictDataDef{
		{Label: "通知", Value: "1", Class: "primary", Sort: 1, IsDefault: true},
		{Label: "公告", Value: "2", Class: "warning", Sort: 2},
	}},
	{Code: "sys_notice_status", Name: "通知状态", Data: []dictDataDef{
		{Label: "草稿", Value: "0", Class: "info", Sort: 1},
		{Label: "已发布", Value: "1", Class: "success", Sort: 2, IsDefault: true},
		{Label: "已撤回", Value: "2", Class: "warning", Sort: 3},
	}},
	{Code: "sys_notice_priority", Name: "通知优先级", Data: []dictDataDef{
		{Label: "普通", Value: "0", Class: "info", Sort: 1, IsDefault: true},
		{Label: "重要", Value: "1", Class: "warning", Sort: 2},
		{Label: "紧急", Value: "2", Class: "danger", Sort: 3},
	}},
	{Code: "sys_notice_publish_type", Name: "通知接收范围", Data: []dictDataDef{
		{Label: "全体成员", Value: "0", Class: "primary", Sort: 1, IsDefault: true},
		{Label: "指定角色", Value: "1", Class: "warning", Sort: 2},
		{Label: "指定部门", Value: "2", Class: "success", Sort: 3},
		{Label: "指定个人", Value: "3", Class: "info", Sort: 4},
	}},
	{Code: "sys_notice_read_status", Name: "通知阅读状态", Data: []dictDataDef{
		{Label: "未读", Value: "0", Class: "warning", Sort: 1, IsDefault: true},
		{Label: "已读", Value: "1", Class: "info", Sort: 2},
	}},
	{Code: "sys_job_status", Name: "任务状态", Data: []dictDataDef{
		{Label: "运行中", Value: "1", Class: "success", Sort: 1, IsDefault: true},
		{Label: "暂停", Value: "0", Class: "danger", Sort: 2},
	}},
	{Code: "sys_job_concurrent", Name: "任务并发", Data: []dictDataDef{
		{Label: "允许", Value: "1", Class: "success", Sort: 1, IsDefault: true},
		{Label: "禁止", Value: "0", Class: "danger", Sort: 2},
	}},
	{Code: "sys_job_log_trigger", Name: "任务日志触发类型", Data: []dictDataDef{
		{Label: "定时触发", Value: "1", Class: "primary", Sort: 1, IsDefault: true},
		{Label: "手动执行", Value: "2", Class: "info", Sort: 2},
	}},
	{Code: "sys_job_log_status", Name: "任务日志状态", Data: []dictDataDef{
		{Label: "执行中", Value: "0", Class: "warning", Sort: 1, IsDefault: true},
		{Label: "成功", Value: "1", Class: "success", Sort: 2},
		{Label: "失败", Value: "2", Class: "danger", Sort: 3},
	}},
	{Code: "sys_config_status", Name: "参数配置状态", Data: []dictDataDef{
		{Label: "启用", Value: "1", Class: "success", Sort: 1, IsDefault: true},
		{Label: "禁用", Value: "0", Class: "danger", Sort: 2},
	}},
	{Code: "sys_dict_status", Name: "字典数据状态", Data: []dictDataDef{
		{Label: "启用", Value: "1", Class: "success", Sort: 1, IsDefault: true},
		{Label: "禁用", Value: "0", Class: "danger", Sort: 2},
	}},
	{Code: "sys_menu_status", Name: "菜单状态", Data: []dictDataDef{
		{Label: "启用", Value: "1", Class: "success", Sort: 1, IsDefault: true},
		{Label: "禁用", Value: "0", Class: "danger", Sort: 2},
	}},
	{Code: "sys_role_status", Name: "角色状态", Data: []dictDataDef{
		{Label: "启用", Value: "1", Class: "success", Sort: 1, IsDefault: true},
		{Label: "禁用", Value: "0", Class: "danger", Sort: 2},
	}},
	{Code: "sys_dept_status", Name: "部门状态", Data: []dictDataDef{
		{Label: "启用", Value: "1", Class: "success", Sort: 1, IsDefault: true},
		{Label: "禁用", Value: "0", Class: "danger", Sort: 2},
	}},
	{Code: "sys_user_gender", Name: "用户性别", Data: []dictDataDef{
		{Label: "未知", Value: "0", Sort: 0, IsDefault: true},
		{Label: "男", Value: "1", Sort: 1},
		{Label: "女", Value: "2", Sort: 2},
	}},
	{Code: "sys_job_run_at_startup", Name: "任务启动执行", Data: []dictDataDef{
		{Label: "是", Value: "1", Class: "primary", Sort: 1, IsDefault: true},
		{Label: "否", Value: "0", Class: "info", Sort: 2},
	}},
	{Code: "sys_list_class", Name: "字典样式", Data: []dictDataDef{
		{Label: "主要", Value: "primary", Class: "primary", Sort: 1, IsDefault: true},
		{Label: "成功", Value: "success", Class: "success", Sort: 2},
		{Label: "信息", Value: "info", Class: "info", Sort: 3},
		{Label: "警告", Value: "warning", Class: "warning", Sort: 4},
		{Label: "危险", Value: "danger", Class: "danger", Sort: 5},
	}},
	{Code: "sys_opt_result_code", Name: "操作日志结果码", Data: []dictDataDef{
		{Label: "成功", Value: "0", Class: "success", Sort: 1},
		{Label: "认证失败", Value: "10001", Class: "warning", Sort: 2},
		{Label: "无操作权限", Value: "10002", Class: "warning", Sort: 3},
		{Label: "令牌已过期", Value: "10003", Class: "warning", Sort: 4},
		{Label: "需要验证码", Value: "10004", Class: "warning", Sort: 5},
		{Label: "验证码错误", Value: "10005", Class: "danger", Sort: 6},
		{Label: "验证码已过期", Value: "10006", Class: "warning", Sort: 7},
		{Label: "请求过于频繁", Value: "10007", Class: "danger", Sort: 8},
		{Label: "账号已锁定", Value: "10008", Class: "danger", Sort: 9},
		{Label: "参数错误", Value: "40000", Class: "danger", Sort: 10},
		{Label: "资源不存在", Value: "40400", Class: "danger", Sort: 11},
		{Label: "操作冲突", Value: "40900", Class: "danger", Sort: 12},
		{Label: "服务器内部错误", Value: "50000", Class: "danger", Sort: 13},
	}},
}

// seedDict 批量创建字典类型，回读各类型的 snowflake ID，再批量创建其下数据。
func seedDict(tx *gorm.DB) error {
	// 1. 批量创建全部字典类型。
	types := make([]entity.SysDictType, 0, len(dictDefinitions))
	for _, d := range dictDefinitions {
		types = append(types, entity.SysDictType{
			Code:   d.Code,
			Name:   d.Name,
			Status: util.Ptr[int8](entity.DictTypeStatusEnabled),
		})
	}
	if err := tx.CreateInBatches(types, 100).Error; err != nil {
		return err
	}
	idByCode := make(map[string]uint64, len(types))
	for i := range types {
		idByCode[types[i].Code] = types[i].ID
	}

	// 2. 批量创建全部字典数据，type_id 用变量引用。
	var data []entity.SysDictData
	for _, d := range dictDefinitions {
		typeID := idByCode[d.Code]
		for _, item := range d.Data {
			dd := entity.SysDictData{
				TypeID:    typeID,
				Label:     item.Label,
				Value:     item.Value,
				Sort:      util.Ptr(item.Sort),
				IsDefault: util.Ptr[int8](boolToInt8(item.IsDefault)),
				Status:    util.Ptr[int8](entity.DictDataStatusEnabled),
			}
			if item.Class != "" {
				dd.ListClass = util.Ptr(item.Class)
			}
			data = append(data, dd)
		}
	}
	return tx.CreateInBatches(data, 100).Error
}

func boolToInt8(b bool) int8 {
	if b {
		return entity.DictDataDefaultYes
	}
	return entity.DictDataDefaultNo
}
