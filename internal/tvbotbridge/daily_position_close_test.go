package tvbotbridge

import (
	"testing"
	"time"
)

func TestScheduledCloseDueUsesChinaLocalMinutes(t *testing.T) {
	china := time.Date(2026, time.November, 5, 6, 0, 0, 0, chinaStandardTime)
	if !scheduledCloseDue(china, 6, 0) {
		t.Fatal("scheduled close should be due at the configured minute")
	}
	if scheduledCloseDue(china.Add(-time.Second), 6, 0) {
		t.Fatal("scheduled close should not be due before the configured minute")
	}
	if !scheduledCloseDue(china.Add(12*time.Hour), 6, 0) {
		t.Fatal("scheduled close should remain due after the configured minute")
	}
}

func TestScheduledCloseDueAtMidnight(t *testing.T) {
	midnight := time.Date(2026, time.November, 5, 0, 0, 0, 0, chinaStandardTime)
	if !scheduledCloseDue(midnight, 0, 0) {
		t.Fatal("midnight schedule should be due at midnight")
	}
	if !scheduledCloseDue(midnight.Add(-time.Nanosecond), 0, 0) {
		t.Fatal("a missed midnight schedule should be due later on that China date")
	}
}
