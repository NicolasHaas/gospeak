// Package version holds build-time version info injected via ldflags.
//
// Set at compile time:
//
//	go build -ldflags "-X github.com/NicolasHaas/gospeak/pkg/version.tag=v1.0.0
//	  -X github.com/NicolasHaas/gospeak/pkg/version.commit=abc1234
//	  -X github.com/NicolasHaas/gospeak/pkg/version.date=2026-01-01"
package version

// Populated by -ldflags "-X ...". Defaults are used for local dev builds.
var (
	tag    = ""        // git tag (e.g. "v0.2.0"), empty if not on a tag
	commit = "unknown" // short git commit SHA
	date   = "unknown" // build date (ISO 8601)
)

// String returns a human-readable version string.
//
//	Tagged:   "v0.2.0"
//	Untagged: "abc1234"
//	Dev:      "dev"
func String() string {
	if tag != "" {
		return tag
	}
	if commit != "unknown" {
		return commit
	}
	return "dev"
}

// Full returns "tag (commit) built date" or a sensible fallback.
func Full() string {
	if tag != "" {
		return tag + " (" + commit + ") built " + date
	}
	if commit != "unknown" {
		return commit + " built " + date
	}
	return "dev"
}

// Tag returns the git tag, or empty string.
func Tag() string { return tag }

// Commit returns the short commit SHA.
func Commit() string { return commit }

// Date returns the build date.
func Date() string { return date }
