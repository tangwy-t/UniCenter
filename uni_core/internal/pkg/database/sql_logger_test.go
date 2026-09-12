package database

import "testing"

// TestExtractTable 覆盖 GORM 常见 SQL 形态:
// 反引号/双引号包裹、子查询关键字误匹配、事务等无表语句。
func TestExtractTable(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		// 反引号包裹（GORM 生成的主流形态）
		{"SELECT * FROM `sys_user` WHERE id = 1", "sys_user"},
		{"SELECT count(*) FROM `sys_user`", "sys_user"},
		{"UPDATE `sys_config` SET config_value=1 WHERE id=2", "sys_config"},
		{"INSERT INTO `sys_dept` (`id`,`name`) VALUES (1,'x')", "sys_dept"},
		{"DELETE FROM `sys_dict_data` WHERE id = 1", "sys_dict_data"},
		// 双引号 / 裸表名
		{`SELECT * FROM "sys_role" ORDER BY id`, "sys_role"},
		{"UPDATE sys_config SET config_value=1 WHERE id=2", "sys_config"},
		// JOIN 场景
		{"SELECT * FROM `sys_user` JOIN sys_role ON sys_role.id = 1", "sys_user"},
		{"SELECT u.* FROM `sys_user` u INNER JOIN `sys_dept` d ON d.id = u.dept_id", "sys_user"},
		// 子查询:第一个候选是 SELECT 关键字,应跳过取内层表
		{"SELECT COUNT(*) FROM (SELECT id FROM `sys_user`) t", "sys_user"},
		// 线上真实 SQL(用户反馈样本)
		{
			"SELECT * FROM `sys_user` WHERE username = 'admin' AND `sys_user`.`deleted_at` IS NULL ORDER BY `sys_user`.`id` LIMIT 1",
			"sys_user",
		},
		{
			"SELECT column_name, column_default, is_nullable = 'YES', data_type, character_maximum_length, column_type, column_key, extra, column_comment, numeric_precision, numeric_scale , datetime_precision FROM information_schema.columns WHERE table_schema = 'web_manager_framework' AND table_name = 'sys_menu' ORDER BY ORDINAL_POSITION",
			"information_schema.columns",
		},
		// 库名限定 / 反引号限定
		{"SELECT * FROM web_manager_framework.sys_user LIMIT 1", "web_manager_framework.sys_user"},
		{"SELECT * FROM `web_manager_framework`.`sys_user` LIMIT 1", "web_manager_framework.sys_user"},
		// 无表语句
		{"BEGIN", ""},
		{"COMMIT", ""},
		{"SELECT NOW()", ""},
		{"SELECT DATABASE()", ""},
		{"SELECT 1", ""},
	}
	for _, c := range cases {
		if got := extractTable(c.sql); got != c.want {
			t.Errorf("extractTable(%q) = %q, want %q", c.sql, got, c.want)
		}
	}
}
