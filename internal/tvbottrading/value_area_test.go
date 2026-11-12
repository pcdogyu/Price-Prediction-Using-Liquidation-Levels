package trading

import "testing"

func TestIsValueAreaOpeningSignal(t *testing.T) {
	tests := []struct {
		name      string
		condition string
		effect    string
		want      bool
	}{
		{name: "automatic", condition: "value_area_signal", effect: PositionEffectOpen, want: true},
		{name: "manual", condition: "value_area_manual", effect: PositionEffectOpen, want: true},
		{name: "close", condition: "value_area_signal", effect: PositionEffectClose},
		{name: "other strategy", condition: "liquidation_alert", effect: PositionEffectOpen},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := IsValueAreaOpeningSignal(Signal{Condition: tc.condition, PositionEffect: tc.effect})
			if got != tc.want {
				t.Fatalf("value-area opening=%v want=%v", got, tc.want)
			}
		})
	}
}
