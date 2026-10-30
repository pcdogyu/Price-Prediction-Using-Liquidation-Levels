package httpapi

import (
	"html"
	"time"
)

// These values are populated by the production build with go build -ldflags
// -X. Keeping development defaults makes locally built binaries self-describing
// without requiring a Git checkout at runtime.
var (
	BuildTime     = "unknown"
	BuildRevision = "unknown"
	BuildBranch   = "unknown"
)

const buildAuthor = "Yuhao@jiansutech.com"

func versionFooter() string {
	return "Code by " + buildAuthor + " · " + formatBuildTime(BuildTime) + " · " + shortenRevision(BuildRevision) + " · " + escapeBuildValue(BuildBranch)
}

func formatBuildTime(value string) string {
	if value == "" || value == "unknown" {
		return escapeBuildValue(value)
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return escapeBuildValue(value)
	}
	return escapeBuildValue(parsed.In(time.FixedZone("UTC+8", 8*60*60)).Format(time.RFC3339))
}

func shortenRevision(value string) string {
	if len(value) > 8 && isHexRevision(value) {
		value = value[len(value)-8:]
	}
	return escapeBuildValue(value)
}

func isHexRevision(value string) bool {
	for index := 0; index < len(value); index++ {
		character := value[index]
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F')) {
			return false
		}
	}
	return value != ""
}

func escapeBuildValue(value string) string {
	if value == "" {
		value = "unknown"
	}
	return html.EscapeString(value)
}
