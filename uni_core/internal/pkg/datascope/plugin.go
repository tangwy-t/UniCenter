package datascope

import (
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// ScopePlugin holds the entity scope registry and manages GORM scope callbacks.
// Created once at startup; all fields are set during initialization and read-only at runtime.
type ScopePlugin struct {
	registry map[string]DataScopeable
}

// NewScopePlugin creates a ScopePlugin with an empty registry.
func NewScopePlugin() *ScopePlugin {
	return &ScopePlugin{registry: map[string]DataScopeable{}}
}

// RegisterEntity registers an entity to the scope registry.
func (p *ScopePlugin) RegisterEntity(tableName string, entity DataScopeable) {
	p.registry[tableName] = entity
}

// tabler 由所有 GORM 实体实现(gorm.io Tabler 约定)。
type tabler interface{ TableName() string }

// RegisterEntities 批量注册,键取自实体自身的 TableName()。
// 消除"实体里定义一次表名、注册处再手写一次"的双源漂移。
// 未实现 TableName 的实体触发 panic:注册键无法确定,静默跳过等于
// 该实体失去 scope 过滤 —— 宁可启动期爆炸。
func (p *ScopePlugin) RegisterEntities(entities ...DataScopeable) {
	for _, e := range entities {
		t, ok := e.(tabler)
		if !ok {
			panic(fmt.Sprintf("datascope: entity %T has no TableName()", e))
		}
		p.registry[t.TableName()] = e
	}
}

// RegisterPlugin registers the GORM Query/Update/Delete callbacks that
// automatically inject scope WHERE conditions.
//
// Update and Delete coverage is what prevents out-of-scope writes (IDOR):
// with a Query-only callback, a "self"-scoped operator could still modify or
// delete arbitrary rows by ID because write statements bypassed the scope.
func (p *ScopePlugin) RegisterPlugin(db *gorm.DB) {
	db.Callback().Query().Before("gorm:query").Register("datascope:query", p.scopeCallback)
	db.Callback().Update().Before("gorm:update").Register("datascope:update", p.scopeCallback)
	db.Callback().Delete().Before("gorm:delete").Register("datascope:delete", p.scopeCallback)
}

// scopeCallback injects the scope conditions into the statement being built.
// It is shared by the Query, Update, and Delete callbacks: appending WHERE
// conditions works identically for SELECT, UPDATE ... WHERE, and DELETE ...
// WHERE statements.
func (p *ScopePlugin) scopeCallback(db *gorm.DB) {
	sc, ok := ScopeContextFromCtx(db.Statement.Context)
	if !ok || sc == nil {
		return
	}

	entity, ok := p.registry[db.Statement.Table]
	if !ok {
		return
	}

	// Collect conditions from all dimensions; they are ORed.
	// - nil AllowedIDs: dimension grants full access → skip (no restriction)
	// - empty []AllowedIDs: dimension has no access → skip (other dimensions may still grant access)
	var clauses []string
	var args []interface{}
	var hasEmptyDimension bool

	for _, rule := range entity.DataScopeRules() {
		dim, ok := sc.Dimensions[rule.DimensionType]
		if !ok || dim == nil || dim.AllowedIDs == nil {
			continue
		}
		if len(dim.AllowedIDs) == 0 {
			hasEmptyDimension = true
			continue
		}

		col := db.Statement.Table + "." + rule.Column

		if rule.Via != nil {
			clause := col + " IN (SELECT " + rule.Via.Select + " FROM " + rule.Via.Table + " WHERE " + rule.Via.Where + " IN ?)"
			clauses = append(clauses, clause)
		} else {
			clauses = append(clauses, col+" IN ?")
		}
		args = append(args, dim.AllowedIDs)
	}

	if len(clauses) == 0 {
		if hasEmptyDimension {
			db.Where("1 = 0")
		}
		return
	}

	whereClause := "(" + strings.Join(clauses, " OR ") + ")"
	db.Where(whereClause, args...)
}
