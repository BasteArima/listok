package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

type AuditEntry struct {
	At         time.Time
	UserID     int64 // 0 = неизвестный пользователь или система
	Action     string
	ObjectType string
	ObjectID   int64
	Details    map[string]any
	IP         string
}

func (s *Store) AddAudit(ctx context.Context, e AuditEntry) error {
	var details sql.NullString
	if len(e.Details) > 0 {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return err
		}
		details = sql.NullString{String: string(b), Valid: true}
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (at, user_id, action, object_type, object_id, details, ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		unix(e.At), nullID(e.UserID), e.Action, nullStr(e.ObjectType), nullID(e.ObjectID), details, nullStr(e.IP))
	return err
}

func nullID(id int64) sql.NullInt64 { return sql.NullInt64{Int64: id, Valid: id != 0} }

func nullStr(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }
