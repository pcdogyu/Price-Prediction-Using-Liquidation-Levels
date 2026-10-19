package httpapi

import (
	"strings"
	"testing"
)

func TestVersionFooterUsesBuildMetadataAndDefaults(t *testing.T) {
	originalTime, originalRevision, originalBranch := BuildTime, BuildRevision, BuildBranch
	t.Cleanup(func() {
		BuildTime, BuildRevision, BuildBranch = originalTime, originalRevision, originalBranch
	})

	BuildTime = "2026-10-17T20:26:29+08:00"
	BuildRevision = "ec01b5c"
	BuildBranch = "main"
	if got, want := versionFooter(), "Code by Yuhao@jiansutech.com · 2026-10-17T20:26:29+08:00 · ec01b5c · main"; got != want {
		t.Fatalf("version footer=%q, want %q", got, want)
	}

	BuildTime, BuildRevision, BuildBranch = "", "", ""
	if got, want := versionFooter(), "Code by Yuhao@jiansutech.com · unknown · unknown · unknown"; got != want {
		t.Fatalf("default version footer=%q, want %q", got, want)
	}
}

func TestVersionFooterEscapesBuildMetadata(t *testing.T) {
	originalTime, originalRevision, originalBranch := BuildTime, BuildRevision, BuildBranch
	t.Cleanup(func() {
		BuildTime, BuildRevision, BuildBranch = originalTime, originalRevision, originalBranch
	})

	BuildTime = `<time>`
	BuildRevision = `"revision"`
	BuildBranch = `feature&footer`
	got := versionFooter()
	for _, forbidden := range []string{"<time>", `"revision"`, "feature&footer"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("version footer failed to escape %q: %q", forbidden, got)
		}
	}
}
