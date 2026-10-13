package bootstrap

// This file has been modified for the KestrelDesk project.

import (
	"github.com/Swhshd/kestreldesk/internal/migration"
	"github.com/Swhshd/kestreldesk/internal/models"

	"github.com/mlogclub/simple/sqls"
)

func InitMigrations() error {
	if err := sqls.DB().AutoMigrate(models.Models...); err != nil {
		return err
	}
	return migration.Migrate()
}
