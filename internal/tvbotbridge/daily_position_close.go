package tvbotbridge

import (
	"context"
	"log/slog"
	"strings"
	"time"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
)

const (
	dailyPositionCloseRunInterval = 15 * time.Second
	dailyPositionCloseRunTimeout  = 10 * time.Minute
)

var chinaStandardTime = time.FixedZone("Asia/Shanghai", 8*60*60)

func (b *Bridge) startDailyPositionCloseMonitor(ctx context.Context) {
	if b == nil || b.Handler == nil || b.Tenants == nil {
		return
	}
	go b.runDailyPositionCloseMonitor(ctx)
}

func (b *Bridge) runDailyPositionCloseMonitor(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	b.runDailyPositionClosePass(ctx)
	ticker := time.NewTicker(dailyPositionCloseRunInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.runDailyPositionClosePass(ctx)
		}
	}
}

func (b *Bridge) runDailyPositionClosePass(ctx context.Context) {
	now := b.schedulerNow()
	records, err := b.Tenants.readOnlyTenantConfigs(ctx)
	if err != nil {
		b.logDailyPositionClose("failed to enumerate TVBot tenants for scheduled close", "error", err)
		return
	}
	localNow := now.In(chinaStandardTime)
	date := localNow.Format("2006-01-02")
	for _, record := range records {
		if !record.Config.Trading.PositionMonitor.DailyCloseEnabled {
			continue
		}
		hour, minute, parseErr := tvbotconfig.ParseDailyCloseTime(record.Config.Trading.PositionMonitor.DailyCloseTime)
		if parseErr != nil {
			b.logDailyPositionClose("scheduled close skipped because configured time is invalid", "username", record.Username, "time", record.Config.Trading.PositionMonitor.DailyCloseTime, "error", parseErr)
			continue
		}
		if !scheduledCloseDue(localNow, hour, minute) {
			continue
		}
		b.claimAndRunDailyPositionClose(ctx, record.Username, date, now)
	}
}

func (b *Bridge) claimAndRunDailyPositionClose(ctx context.Context, username, date string, now time.Time) {
	scope, err := b.Tenants.ForUserScope(username)
	if err != nil {
		b.logDailyPositionClose("failed to resolve TVBot tenant for scheduled close", "username", username, "error", err)
		return
	}
	if scope.ConfigStore == nil || scope.Orders == nil {
		b.logDailyPositionClose("scheduled close skipped because tenant resources are unavailable", "username", username)
		return
	}
	cfg := scope.ConfigStore.Get()
	if !cfg.Trading.PositionMonitor.DailyCloseEnabled {
		return
	}
	hour, minute, parseErr := tvbotconfig.ParseDailyCloseTime(cfg.Trading.PositionMonitor.DailyCloseTime)
	if parseErr != nil || !scheduledCloseDue(now.In(chinaStandardTime), hour, minute) {
		return
	}
	claimed, err := scope.Orders.ClaimScheduledRun(ctx, tvbotstorage.DailyPositionCloseRunKey, date, now)
	if err != nil {
		b.logDailyPositionClose("failed to claim scheduled close run", "username", username, "date", date, "error", err)
		return
	}
	if !claimed {
		return
	}

	scoped := b.Handler.WithUserScope(scope)
	go func() {
		runCtx, cancel := context.WithTimeout(ctx, dailyPositionCloseRunTimeout)
		defer cancel()
		runErr := scoped.RunScheduledPositionClose(runCtx)
		status := "completed"
		message := "scheduled close completed"
		if runErr != nil {
			status = "partial"
			message = runErr.Error()
		}
		if finishErr := scope.Orders.FinishScheduledRun(context.Background(), tvbotstorage.DailyPositionCloseRunKey, date, status, message, b.schedulerNow()); finishErr != nil {
			b.logDailyPositionClose("failed to finish scheduled close run", "username", username, "date", date, "error", finishErr)
		}
		if runErr != nil {
			b.logDailyPositionClose("scheduled position close completed with errors", "username", username, "date", date, "error", runErr)
			return
		}
		b.logDailyPositionClose("scheduled position close completed", "username", username, "date", date)
	}()
}

func scheduledCloseDue(now time.Time, hour, minute int) bool {
	minutes := now.Hour()*60 + now.Minute()
	return minutes >= hour*60+minute
}

func (b *Bridge) schedulerNow() time.Time {
	if b != nil && b.Handler != nil && b.Handler.Now != nil {
		return b.Handler.Now().UTC()
	}
	return time.Now().UTC()
}

func (b *Bridge) logDailyPositionClose(message string, args ...any) {
	if b == nil || b.Handler == nil || b.Handler.Logger == nil {
		return
	}
	b.Handler.Logger.Log(context.Background(), slog.LevelInfo, strings.TrimSpace(message), args...)
}
