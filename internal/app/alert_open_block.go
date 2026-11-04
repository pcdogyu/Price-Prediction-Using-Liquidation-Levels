package app

import (
	"time"
	_ "time/tzdata"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

var alertOpenBlockLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

var alertOpenBlockNewYorkLocation = func() *time.Location {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		// The application embeds tzdata; keep a safe winter-time fallback if a
		// platform nevertheless cannot load the IANA zone database.
		return time.FixedZone("America/New_York", -5*60*60)
	}
	return location
}()

type alertOpenBlockWindow struct {
	name    string
	start   int
	end     int
	enabled func(domain.AlertStrategySettings) bool
}

var alertOpenBlockWindows = [...]alertOpenBlockWindow{
	{
		name:    "08:00–09:00",
		start:   8 * 60,
		end:     9 * 60,
		enabled: func(settings domain.AlertStrategySettings) bool { return settings.BlockOpen0800To0900 },
	},
	{
		name:    "16:00–17:00",
		start:   16 * 60,
		end:     17 * 60,
		enabled: func(settings domain.AlertStrategySettings) bool { return settings.BlockOpen1600To1700 },
	},
}

func alertOpenBlockName(at time.Time, settings domain.AlertStrategySettings) string {
	if at.IsZero() {
		return ""
	}
	local := at.In(alertOpenBlockLocation)
	minute := local.Hour()*60 + local.Minute()
	for _, window := range alertOpenBlockWindows {
		if window.enabled(settings) && minute >= window.start && minute < window.end {
			return window.name
		}
	}
	if settings.BlockOpen2130To2230 {
		if at.In(alertOpenBlockNewYorkLocation).IsDST() {
			if minute >= 21*60+30 && minute < 22*60+30 {
				return "21:30–22:30"
			}
		} else if minute >= 22*60+30 && minute < 23*60+30 {
			return "22:30–23:30"
		}
	}
	return ""
}

func (rtm *alertRuntime) automaticOpenBlockName(at time.Time) string {
	settings := rtm.snapshotStrategySettings()
	return alertOpenBlockName(at, settings)
}
