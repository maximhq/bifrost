package postgresconn

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"gorm.io/gorm/logger"
)

type gormLogger struct {
	logger schemas.Logger
}

// NewGormLogger adapts the Bifrost logger to GORM's logger interface.
func NewGormLogger(logger schemas.Logger) logger.Interface {
	return &gormLogger{logger: logger}
}

func (l *gormLogger) LogMode(level logger.LogLevel) logger.Interface {
	return l
}

func (l *gormLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	if l.logger == nil {
		return
	}
	l.logger.Info(msg, data...)
}

func (l *gormLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	if l.logger == nil {
		return
	}
	l.logger.Warn(msg, data...)
}

func (l *gormLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	if l.logger == nil {
		return
	}
	l.logger.Error(msg, data...)
}

func (l *gormLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if err != nil && l.logger != nil {
		rows := int64(-1)
		if fc != nil {
			_, rows = fc()
		}
		l.logger.Debug("SQL Error: %v | Rows: %d", err, rows)
	}
}
