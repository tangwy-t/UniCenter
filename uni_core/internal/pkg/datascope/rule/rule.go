// Package rule defines the core types for data scope declaration — kept as a leaf package
// to avoid import cycles (entity imports rule, datascope imports rule, but rule imports nothing).
package rule

// DataScopeable is the interface that entities implement to declare data scope rules.
// When a GORM model implements this interface, the GORM Plugin automatically
// injects scope conditions into its queries.
type DataScopeable interface {
	DataScopeRules() []ScopeRule
}

// ScopeRule describes a single data scope filtering rule.
type ScopeRule struct {
	Column        string    // Column name in the current table, e.g. "dept_id", "user_id"
	DimensionType string    // Dimension type, e.g. "dept", "self"
	Via           *ScopeVia // nil = direct IN filter; non-nil = subquery indirect filter
}

// ScopeVia describes an indirect filter via a subquery through an intermediate table.
type ScopeVia struct {
	Table  string // Intermediate table name, e.g. "sys_user"
	Select string // Column to select from the intermediate table, e.g. "id"
	Where  string // Column in the intermediate table that matches the dimension, e.g. "dept_id"
}
