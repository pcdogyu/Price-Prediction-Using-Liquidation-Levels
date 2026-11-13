package tvbotbridge

import (
	"testing"
	"time"

	tvbotstorage "github.com/pcdogyu/price-prediction-liquidation-levels/internal/tvbotstorage"
)

func TestScheduledCloseSlotsAtConfiguredChinaTime(t *testing.T) {
	before := time.Date(2026, time.November, 5, 5, 52, 10, 0, chinaStandardTime)
	due := scheduledCloseSlotsDue(before, 6, 0)
	if len(due) != 1 {
		t.Fatalf("at 05:52 expected only the pre-close slot, got %#v", due)
	}
	if due[0].Name != "before" || due[0].RunKey != dailyPositionCloseBeforeKey || due[0].Target.Hour() != 5 || due[0].Target.Minute() != 52 {
		t.Fatalf("unexpected pre-close slot: %#v", due[0])
	}

	main := before.Add(8 * time.Minute)
	due = scheduledCloseSlotsDue(main, 6, 0)
	if len(due) != 1 || due[0].Name != "main" || due[0].RunKey != tvbotstorage.DailyPositionCloseRunKey || due[0].Target.Hour() != 6 || due[0].Target.Minute() != 0 {
		t.Fatalf("configured close must retain its existing run key: %#v", due)
	}

	after := scheduledCloseSlotsDue(main.Add(7*time.Minute), 6, 0)
	if len(after) != 1 || after[0].Name != "after" || after[0].RunKey != dailyPositionCloseAfterKey || after[0].Target.Hour() != 6 || after[0].Target.Minute() != 7 {
		t.Fatalf("at 06:07 expected only the final independently keyed slot, got %#v", after)
	}
}

func TestScheduledCloseSlotsAllowShortSchedulerCatchUpButNotLateSameDayClose(t *testing.T) {
	china := time.Date(2026, time.November, 5, 6, 1, 30, 0, chinaStandardTime)
	due := scheduledCloseSlotsDue(china, 6, 0)
	if len(due) != 1 || due[0].Name != "main" {
		t.Fatalf("the configured close should catch up within the scheduler window, got %#v", due)
	}
	if !scheduledCloseSlotIsDue(china, 6, 0, due[0]) {
		t.Fatal("a due slot should pass claim-time revalidation")
	}
	changedSchedule := due[0]
	changedSchedule.Target = changedSchedule.Target.Add(time.Minute)
	if scheduledCloseSlotIsDue(china, 6, 0, changedSchedule) {
		t.Fatal("claim-time revalidation should reject a slot from a changed schedule")
	}
	late := time.Date(2026, time.November, 5, 8, 0, 0, 0, chinaStandardTime)
	if got := scheduledCloseSlotsDue(late, 6, 0); len(got) != 0 {
		t.Fatalf("a late restart must not launch the day's closes hours after their target times, got %#v", got)
	}
}

func TestScheduledCloseBeforeSlotHandlesMidnightOffset(t *testing.T) {
	china := time.Date(2026, time.November, 5, 23, 56, 0, 0, chinaStandardTime)
	due := scheduledCloseSlotsDue(china, 0, 4)
	if len(due) != 1 || due[0].Name != "before" || due[0].ScheduleDate != "2026-11-06" || !due[0].Target.Equal(china) {
		t.Fatalf("00:04 schedule should trigger its 23:56 pre-close on the prior evening, got %#v", due)
	}
}

func TestScheduledCloseSlotsUseChinaDateForUTCInput(t *testing.T) {
	utc := time.Date(2026, time.November, 4, 22, 0, 0, 0, time.UTC) // 06:00 China time.
	due := scheduledCloseSlotsDue(utc, 6, 0)
	if len(due) != 1 || due[0].Target.Format(time.RFC3339) != "2026-11-05T06:00:00+08:00" {
		t.Fatalf("UTC input should schedule by China local date, got %#v", due)
	}
}
