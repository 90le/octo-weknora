package database

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// privateGORMLogger keeps operational query diagnostics without writing SQL
// text, bound values, or driver error messages into routine application logs.
// These values can contain user questions, credentials, and opaque IDs. GORM
// still returns the original error to callers; only the log representation is
// reduced.
type privateGORMLogger struct {
	level         gormlogger.LogLevel
	slowThreshold time.Duration
}

// NewPrivateGORMLogger preserves GORM's default warning and slow-query
// thresholds while excluding query contents from ordinary logs.
func NewPrivateGORMLogger() gormlogger.Interface {
	return &privateGORMLogger{level: gormlogger.Warn, slowThreshold: 200 * time.Millisecond}
}

func (l *privateGORMLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	copy := *l
	copy.level = level
	return &copy
}

func (l *privateGORMLogger) Info(ctx context.Context, _ string, _ ...interface{}) {
	if l.level >= gormlogger.Info {
		logger.Info(ctx, "Database info")
	}
}

func (l *privateGORMLogger) Warn(ctx context.Context, _ string, _ ...interface{}) {
	if l.level >= gormlogger.Warn {
		logger.Warn(ctx, "Database warning")
	}
}

func (l *privateGORMLogger) Error(ctx context.Context, _ string, _ ...interface{}) {
	if l.level >= gormlogger.Error {
		logger.Error(ctx, "Database error")
	}
}

func (l *privateGORMLogger) Trace(
	ctx context.Context, begin time.Time, _ func() (string, int64), err error,
) {
	if l.level <= gormlogger.Silent {
		return
	}

	elapsed := time.Since(begin)
	if err != nil && l.level >= gormlogger.Error {
		logger.Errorf(ctx, "Database query: status=error elapsed_ms=%.3f error_type=%T record_not_found=%t",
			float64(elapsed.Nanoseconds())/1e6, err, errors.Is(err, gorm.ErrRecordNotFound))
		return
	}
	if l.slowThreshold != 0 && elapsed > l.slowThreshold && l.level >= gormlogger.Warn {
		logger.Warnf(ctx, "Database query: status=slow elapsed_ms=%.3f",
			float64(elapsed.Nanoseconds())/1e6)
		return
	}
	if l.level >= gormlogger.Info {
		logger.Infof(ctx, "Database query: status=ok elapsed_ms=%.3f",
			float64(elapsed.Nanoseconds())/1e6)
	}
}

// GORM calls ParamsFilter before constructing the SQL passed to Trace. Keep
// placeholders even if a future Trace implementation inspects that SQL.
func (*privateGORMLogger) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return sql, nil
}
