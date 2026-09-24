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
		// ── DDL：表名在 TABLE 之后（不在 FROM/JOIN/INTO/UPDATE 后面）──
		// 这一组是慢查询实录「表名空白」那个缺陷的回归：此前 ALTER/CREATE/TRUNCATE
		// 一条都提取不到，而部署时迁移与后台 job 的 DDL 会集中出现在慢查询里。
		{"ALTER TABLE `device` ADD `target_agent_version` varchar(32)", "device"},
		{"CREATE TABLE `agent_release` (`id` bigint unsigned, `version` varchar(32))", "agent_release"},
		// 线上真实样本（分区维护 job）：表名与后面的 PARTITION 段都要能对上
		{"ALTER TABLE device_metric_disk TRUNCATE PARTITION p2026_w34", "device_metric_disk"},
		{"DROP TABLE IF EXISTS tmp_import", "tmp_import"},
		// MySQL 的 TRUNCATE 可省 TABLE 关键字
		{"TRUNCATE TABLE sys_login_log", "sys_login_log"},
		{"TRUNCATE sys_login_log", "sys_login_log"},
		// 索引的 DDL：表名在 ON 之后（不是 JOIN 的那个 ON）
		{"CREATE INDEX idx_user_name ON sys_user (username)", "sys_user"},
		{"CREATE UNIQUE INDEX `uk_device_token` ON `device` (`token_hash`)", "device"},
		// DDL 与 DML 同时出现时以「被操作的表」为准（创建 x、读 y → x）
		{"CREATE TABLE t_backup AS SELECT * FROM sys_user", "t_backup"},
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
