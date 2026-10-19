package httpapi

import "html"

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
	return "Code by " + buildAuthor + " · " + escapeBuildValue(BuildTime) + " · " + escapeBuildValue(BuildRevision) + " · " + escapeBuildValue(BuildBranch)
}

func escapeBuildValue(value string) string {
	if value == "" {
		value = "unknown"
	}
	return html.EscapeString(value)
}
