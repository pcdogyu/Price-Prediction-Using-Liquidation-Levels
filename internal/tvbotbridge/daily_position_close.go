package tvbotbridge

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	tvbotconfig "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotconfig"
	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
)

const (
	dailyPositionCloseRunInterval = 15 * time.Second
	dailyPositionCloseRunTimeout  = 10 * time.Minute
	dailyPositionCloseSlotWindow  = 2 * time.Minute
	dailyPositionCloseBeforeKey   = tvbotstorage.DailyPositionCloseRunKey + "_before"
	dailyPositionCloseAfterKey    = tvbotstorage.DailyPositionCloseRunKey + "_after"
)

type scheduledPositionCloseSlot struct {
	Name         string
	RunKey       string
	ScheduleDate string
	Target       time.Time
}

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
	for _, record := range records {
		if !record.Config.Trading.PositionMonitor.DailyCloseEnabled {
			continue
		}
		hour, minute, parseErr := tvbotconfig.ParseDailyCloseTime(record.Config.Trading.PositionMonitor.DailyCloseTime)
		if parseErr != nil {
			b.logDailyPositionClose("scheduled close skipped because configured time is invalid", "username", record.Username, "time", record.Config.Trading.PositionMonitor.DailyCloseTime, "error", parseErr)
			continue
		}
		for _, slot := range scheduledCloseSlotsDue(localNow, hour, minute) {
			b.claimAndRunDailyPositionClose(ctx, record.Username, slot)
		}
	}
}

func (b *Bridge) claimAndRunDailyPositionClose(ctx context.Context, username string, slot scheduledPositionCloseSlot) {
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
	now := b.schedulerNow()
	if parseErr != nil || !scheduledCloseSlotIsDue(now.In(chinaStandardTime), hour, minute, slot) {
		return
	}
	claimed, err := scope.Orders.ClaimScheduledRun(ctx, slot.RunKey, slot.ScheduleDate, now)
	if err != nil {
		b.logDailyPositionClose("failed to claim scheduled close run", "username", username, "slot", slot.Name, "scheduled_at", slot.Target.Format(time.RFC3339), "date", slot.ScheduleDate, "error", err)
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
		if finishErr := scope.Orders.FinishScheduledRun(context.Background(), slot.RunKey, slot.ScheduleDate, status, message, b.schedulerNow()); finishErr != nil {
			b.logDailyPositionClose("failed to finish scheduled close run", "username", username, "slot", slot.Name, "date", slot.ScheduleDate, "error", finishErr)
		}
		if runErr != nil {
			b.logDailyPositionClose("scheduled position close completed with errors", "username", username, "slot", slot.Name, "scheduled_at", slot.Target.Format(time.RFC3339), "date", slot.ScheduleDate, "error", runErr)
			return
		}
		b.logDailyPositionClose("scheduled position close completed", "username", username, "slot", slot.Name, "scheduled_at", slot.Target.Format(time.RFC3339), "date", slot.ScheduleDate)
	}()
}

func scheduledCloseSlotsDue(now time.Time, hour, minute int) []scheduledPositionCloseSlot {
	localNow := now.In(chinaStandardTime)
	today := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, chinaStandardTime)
	todayDate := today.Format("2006-01-02")
	definitions := []struct {
		name   string
		runKey string
		offset time.Duration
	}{
		{name: "before", runKey: dailyPositionCloseBeforeKey, offset: -8 * time.Minute},
		{name: "main", runKey: tvbotstorage.DailyPositionCloseRunKey, offset: 0},
		{name: "after", runKey: dailyPositionCloseAfterKey, offset: 7 * time.Minute},
	}

	var due []scheduledPositionCloseSlot
	// The configured daily time is the nominal schedule date. Checking the
	// adjacent nominal dates also handles offsets that cross midnight.
	for dayOffset := -1; dayOffset <= 1; dayOffset++ {
		nominal := today.AddDate(0, 0, dayOffset)
		base := time.Date(nominal.Year(), nominal.Month(), nominal.Day(), hour, minute, 0, 0, chinaStandardTime)
		for _, definition := range definitions {
			target := base.Add(definition.offset)
			if target.Format("2006-01-02") != todayDate || target.After(localNow) || !localNow.Before(target.Add(dailyPositionCloseSlotWindow)) {
				continue
			}
			due = append(due, scheduledPositionCloseSlot{
				Name:         definition.name,
				RunKey:       definition.runKey,
				ScheduleDate: base.Format("2006-01-02"),
				Target:       target,
			})
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].Target.Before(due[j].Target) })
	return due
}

func scheduledCloseSlotIsDue(now time.Time, hour, minute int, want scheduledPositionCloseSlot) bool {
	for _, slot := range scheduledCloseSlotsDue(now, hour, minute) {
		if slot.RunKey == want.RunKey && slot.ScheduleDate == want.ScheduleDate && slot.Target.Equal(want.Target) {
			return true
		}
	}
	return false
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
