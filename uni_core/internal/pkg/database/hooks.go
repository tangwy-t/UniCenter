package database

import (
	"reflect"
	"sync/atomic"

	"github.com/tangwy-t/webmanager-server/internal/pkg/contextkeys"

	sf "github.com/bwmarrin/snowflake"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// Callbacks holds the snowflake Node for GORM callback ID generation.
// Created once at startup; the node is stored atomically for safe concurrent access.
// Audit callbacks read the authenticated user via contextkeys.UserIDFromCtx —
// the same key written by the auth middleware and ws clients — which keeps
// the GORM callback layer decoupled from the transport layer.
type Callbacks struct {
	node atomic.Pointer[sf.Node]
}

// NewCallbacks creates a Callbacks registry with the given snowflake node.
func NewCallbacks(sfNode *sf.Node) *Callbacks {
	c := &Callbacks{}
	c.node.Store(sfNode)
	return c
}

// Register registers all GORM callbacks:
//   - id:generate — auto-generates snowflake ID for zero-value ID fields
//   - audit:set_create / audit:set_update — auto-fill CreatedBy/UpdatedBy from context
func (c *Callbacks) Register(db *gorm.DB) {
	db.Callback().Create().Before("gorm:before_create").Register("id:generate", c.setGeneratedID)

	db.Callback().Create().Before("gorm:before_create").Register("audit:set_create", c.setCreateAudit)
	db.Callback().Update().Before("gorm:before_update").Register("audit:set_update", c.setUpdateAudit)
}

// ── Callback implementations ───────────────────────────────────────────────

func (c *Callbacks) setGeneratedID(db *gorm.DB) {
	n := c.node.Load()
	if n == nil {
		return
	}

	rv := db.Statement.ReflectValue
	switch rv.Kind() {
	case reflect.Struct:
		if !rv.CanAddr() {
			return
		}
		c.setID(db, n, rv)

	case reflect.Slice:
		for i := 0; i < rv.Len(); i++ {
			elem := rv.Index(i)
			if elem.Kind() == reflect.Ptr {
				if elem.IsNil() {
					continue
				}
				elem = elem.Elem()
			}
			c.setID(db, n, elem)
		}

	default:
		return
	}
}

func (c *Callbacks) setID(db *gorm.DB, node *sf.Node, rv reflect.Value) {
	idField := c.lookupField(db, "ID")
	if idField == nil {
		return
	}
	_, isZero := idField.ValueOf(db.Statement.Context, rv)
	if isZero {
		idField.Set(db.Statement.Context, rv, uint64(node.Generate().Int64()))
	}
}

func (c *Callbacks) setCreateAudit(db *gorm.DB) {
	userID, ok := contextkeys.UserIDFromCtx(db.Statement.Context)
	if !ok {
		return
	}
	c.setField(db, "CreatedBy", userID)
	c.setField(db, "UpdatedBy", userID)
}

func (c *Callbacks) setUpdateAudit(db *gorm.DB) {
	userID, ok := contextkeys.UserIDFromCtx(db.Statement.Context)
	if !ok {
		return
	}
	field := c.setField(db, "UpdatedBy", userID)
	if field == nil {
		return
	}

	// For single-column Update() calls (e.g. db.Update("status", 0)),
	// the Dest is a map — inject updated_by so it appears in the SET clause.
	if dest, ok := db.Statement.Dest.(map[string]interface{}); ok {
		dest[field.DBName] = userID
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────

func (c *Callbacks) setField(db *gorm.DB, fieldName string, value uint64) *schema.Field {
	field := c.lookupField(db, fieldName)
	if field == nil {
		return nil
	}

	// During association saves (e.g. Association.Append of a many2many
	// relation), GORM internally calls Create/Updates with a slice as Dest
	// (ReflectValue is the slice, not a struct). schema.Field.Set reflects
	// on the value with reflect.Value.Field, which panics on a slice
	// ("reflect: call of reflect.Value.Field on slice Value"). Iterate the
	// slice and set the field per element, mirroring setGeneratedID.
	rv := db.Statement.ReflectValue
	switch rv.Kind() {
	case reflect.Struct:
		if rv.CanAddr() {
			_ = field.Set(db.Statement.Context, rv, value)
		}
	case reflect.Slice:
		for i := 0; i < rv.Len(); i++ {
			elem := rv.Index(i)
			if elem.Kind() == reflect.Ptr {
				if elem.IsNil() {
					continue
				}
				elem = elem.Elem()
			}
			if elem.Kind() == reflect.Struct && elem.CanAddr() {
				_ = field.Set(db.Statement.Context, elem, value)
			}
		}
	}
	return field
}

func (c *Callbacks) lookupField(db *gorm.DB, fieldName string) *schema.Field {
	if db.Statement.Schema == nil {
		return nil
	}
	return db.Statement.Schema.LookUpField(fieldName)
}
