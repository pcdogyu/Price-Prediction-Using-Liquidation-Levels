package app

import (
	"time"

	"github.com/pcdogyu/price-prediction-liquidation-levels/internal/domain"
)

var alertOpenBlockLocation = time.FixedZone("Asia/Shanghai", 8*60*60)

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
	{
		name:    "21:30–22:30",
		start:   21*60 + 30,
		end:     22*60 + 30,
		enabled: func(settings domain.AlertStrategySettings) bool { return settings.BlockOpen2130To2230 },
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
	return ""
}

func (rtm *alertRuntime) automaticOpenBlockName(at time.Time) string {
	settings := rtm.snapshotStrategySettings()
	return alertOpenBlockName(at, settings)
}
