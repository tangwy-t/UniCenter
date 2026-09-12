package database

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

type ctxKey string

const dbKey ctxKey = "db"

// ErrDBNotInContext is returned when no *gorm.DB is found in the context.
var ErrDBNotInContext = errors.New("database: no *gorm.DB found in context")

// WithDB stores a *gorm.DB in the given context for downstream retrieval.
func WithDB(ctx context.Context, db *gorm.DB) context.Context {
	return context.WithValue(ctx, dbKey, db)
}

// FromContext retrieves the *gorm.DB stored by WithDB.
// It returns ErrDBNotInContext if no database is present.
func FromContext(ctx context.Context) (*gorm.DB, error) {
	if db, ok := ctx.Value(dbKey).(*gorm.DB); ok {
		return db, nil
	}
	return nil, ErrDBNotInContext
}
