package devtransfer

import (
	"database/sql"

	"github.com/libtnb/sqlite"
	"gorm.io/gorm"
)

func gormOpen(d *sql.DB) (*gorm.DB, error) {
	return gorm.Open(sqlite.Dialector{Conn: d})
}
