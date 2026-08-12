package util

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// IsValidBasicSemver
// e.g. "1.2.3" but not "1.2.3-beta" or "1.2"
func IsValidBasicSemver(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) != 3 {
		return false
	}

	for _, part := range parts {
		if _, err := strconv.Atoi(part); err != nil {
			return false
		}
	}

	return true
}

// ParseForkVersion splits a fork version into its upstream semver head and its fork
// revision.
//
// This fork keeps the upstream Seanime version it is built on and appends a fourth
// segment for its own builds: "3.10.2.1" reads as "fork build 1 on top of upstream
// 3.10.2". That form is deliberately NOT valid semver — semver.NewVersion rejects it —
// so every comparison has to come through here. Passing it straight to the semver
// package returns an error that CompareVersion swallows as "versions are equal", which
// silently disables the in-app updater for the whole 3.10.2.x line.
//
// A plain "3.10.2" parses with revision 0, so upstream versions sort below any fork
// build of the same upstream release.
func ParseForkVersion(version string) (*semver.Version, uint64, error) {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")

	if parsed, err := semver.NewVersion(v); err == nil {
		return parsed, 0, nil
	}

	// Only the 4-numeric-segment form gets a second chance; anything else is genuinely bad.
	parts := strings.Split(v, ".")
	if len(parts) != 4 {
		return nil, 0, fmt.Errorf("invalid version %q", version)
	}
	rev, err := strconv.ParseUint(parts[3], 10, 64)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid fork revision in %q", version)
	}
	parsed, err := semver.NewVersion(strings.Join(parts[:3], "."))
	if err != nil {
		return nil, 0, fmt.Errorf("invalid version %q", version)
	}
	return parsed, rev, nil
}

// CompareVersion compares two versions and returns the difference between them.
//
//	 3: Current version is newer by major version.
//	 2: Current version is newer by minor version.
//	 1: Current version is newer by patch version.
//		-3: Current version is older by major version.
//		-2: Current version is older by minor version.
//		-1: Current version is older by patch version.
//
// A difference in the fork revision alone (3.10.2 vs 3.10.2.1) ranks as a patch-level
// change, since it is a rebuild of the same upstream release.
func CompareVersion(current string, b string) (comp int, shouldUpdate bool) {

	currV, currRev, err := ParseForkVersion(current)
	if err != nil {
		return 0, false
	}
	otherV, otherRev, err := ParseForkVersion(b)
	if err != nil {
		return 0, false
	}

	comp = currV.Compare(otherV)
	if comp == 0 {
		// Same upstream release — the fork revision decides.
		switch {
		case currRev > otherRev:
			return 1, false
		case currRev < otherRev:
			return -1, true
		}
		return 0, false
	}

	if currV.GreaterThan(otherV) {
		shouldUpdate = false

		if currV.Major() > otherV.Major() {
			comp *= 3
		} else if currV.Minor() > otherV.Minor() {
			comp *= 2
		} else if currV.Patch() > otherV.Patch() {
			comp *= 1
		}
	} else if currV.LessThan(otherV) {
		shouldUpdate = true

		if currV.Major() < otherV.Major() {
			comp *= 3
		} else if currV.Minor() < otherV.Minor() {
			comp *= 2
		} else if currV.Patch() < otherV.Patch() {
			comp *= 1
		}
	}

	return comp, shouldUpdate
}

func VersionIsOlderThan(version string, compare string) bool {
	comp, shouldUpdate := CompareVersion(version, compare)
	// shouldUpdate is false means the current version is newer
	return comp < 0 && shouldUpdate
}

var allowedGitHubOwners = []string{"ClinShaiju"}

// validateReleaseUrl checks that the URL points to a GitHub release asset
// from an allowed owner.
func ValidateReleaseUrl(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("malformed URL")
	}

	if parsed.Scheme != "https" {
		return fmt.Errorf("only HTTPS URLs are allowed")
	}

	switch parsed.Host {
	case "github.com":
		// e.g. https://github.com/5rahim/seanime/releases/download/v1.0.0/file.zip
		parts := strings.Split(strings.TrimPrefix(parsed.Path, "/"), "/")
		if len(parts) < 6 || parts[2] != "releases" || parts[3] != "download" {
			return fmt.Errorf("URL must point to a GitHub release asset")
		}
		owner := parts[0]
		for _, allowed := range allowedGitHubOwners {
			if strings.EqualFold(owner, allowed) {
				return nil
			}
		}
		return fmt.Errorf("repository owner %q is not allowed", owner)

	case "seanime.app":
		return nil

	default:
		return fmt.Errorf("host %q is not allowed", parsed.Host)
	}
}
