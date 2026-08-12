package util

import (
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompareVersion(t *testing.T) {
	testCases := []struct {
		name           string
		otherVersion   string
		currVersion    string
		expectedOutput int
		shouldUpdate   bool
	}{
		{
			name:           "Current version is newer by major version",
			currVersion:    "2.0.0",
			otherVersion:   "1.0.0",
			expectedOutput: 3,
			shouldUpdate:   false,
		},
		{
			name:           "Current version is older by major version",
			currVersion:    "2.0.0",
			otherVersion:   "3.0.0",
			expectedOutput: -3,
			shouldUpdate:   true,
		},
		{
			name:           "Current version is older by minor version",
			currVersion:    "0.2.2",
			otherVersion:   "0.3.0",
			expectedOutput: -2,
			shouldUpdate:   true,
		},
		{
			name:           "Current version is older by major version",
			currVersion:    "0.2.2",
			otherVersion:   "3.0.0",
			expectedOutput: -3,
			shouldUpdate:   true,
		},
		{
			name:           "Current version is older by minor version",
			currVersion:    "0.2.2",
			otherVersion:   "0.2.3",
			expectedOutput: -1,
			shouldUpdate:   true,
		},
		{
			name:           "Current version is newer by minor version",
			currVersion:    "1.2.0",
			otherVersion:   "1.1.0",
			expectedOutput: 2,
			shouldUpdate:   false,
		},
		{
			name:           "Current version is older by minor version",
			currVersion:    "1.2.0",
			otherVersion:   "1.3.0",
			expectedOutput: -2,
			shouldUpdate:   true,
		},
		{
			name:           "Current version is newer by patch version",
			currVersion:    "1.1.2",
			otherVersion:   "1.1.1",
			expectedOutput: 1,
			shouldUpdate:   false,
		},
		{
			name:           "Current version is older by patch version",
			currVersion:    "1.1.2",
			otherVersion:   "1.1.3",
			expectedOutput: -1,
			shouldUpdate:   true,
		},
		{
			name:           "Versions are equal",
			currVersion:    "1.1.1",
			otherVersion:   "1.1.1",
			expectedOutput: 0,
			shouldUpdate:   false,
		},
		{
			name:           "Current version is newer by patch version",
			currVersion:    "1.1.1",
			otherVersion:   "1.1",
			expectedOutput: 1,
			shouldUpdate:   false,
		},
		{
			name:           "Current version is newer by minor version + prerelease",
			currVersion:    "2.2.0-prerelease",
			otherVersion:   "2.1.0",
			expectedOutput: 2,
			shouldUpdate:   false,
		},
		{
			name:           "Current version is newer (not prerelease)",
			currVersion:    "2.2.0",
			otherVersion:   "2.2.0-prerelease",
			expectedOutput: 1,
			shouldUpdate:   false,
		},
		{
			name:           "Current version is older (is prerelease)",
			currVersion:    "2.2.0-prerelease",
			otherVersion:   "2.2.0",
			expectedOutput: -1,
			shouldUpdate:   true,
		},
		{
			name:           "Current version is older (is canary)",
			currVersion:    "2.5.2",
			otherVersion:   "2.5.3-canary.1",
			expectedOutput: -1,
			shouldUpdate:   true,
		},
		{
			name:           "Current version is newer than canary",
			currVersion:    "2.5.3",
			otherVersion:   "2.5.3-canary.1",
			expectedOutput: 1,
			shouldUpdate:   false,
		},
		{
			name:           "Canary vs Canary",
			currVersion:    "2.5.3-canary.1",
			otherVersion:   "2.5.3-canary.2",
			expectedOutput: -1,
			shouldUpdate:   true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			output, boolOutput := CompareVersion(tc.currVersion, tc.otherVersion)
			if output != tc.expectedOutput || boolOutput != tc.shouldUpdate {
				t.Errorf("Expected output to be %d and shouldUpdate to be %v, got output=%d and shouldUpdate=%v", tc.expectedOutput, tc.shouldUpdate, output, boolOutput)
			}
		})
	}
}

func TestVersionIsOlderThan(t *testing.T) {

	testCases := []struct {
		name    string
		version string
		compare string
		isOlder bool
	}{
		{
			name:    "Version is older than compare",
			version: "1.7.3",
			compare: "2.0.0",
			isOlder: true,
		},
		{
			name:    "Version is newer than compare",
			version: "2.0.1",
			compare: "2.0.0",
			isOlder: false,
		},
		{
			name:    "Version is equal to compare",
			version: "2.0.0",
			compare: "2.0.0",
			isOlder: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			output := VersionIsOlderThan(tc.version, tc.compare)
			if output != tc.isOlder {
				t.Errorf("Expected output to be %v, got %v", tc.isOlder, output)
			}
		})
	}

}

func TestHasUpdated(t *testing.T) {

	testCases := []struct {
		name            string
		previousVersion string
		currentVersion  string
		hasUpdated      bool
	}{
		{
			name:            "previousVersion is older than currentVersion",
			previousVersion: "1.7.3",
			currentVersion:  "2.0.0",
			hasUpdated:      true,
		},
		{
			name:            "previousVersion is newer than currentVersion",
			previousVersion: "2.0.1",
			currentVersion:  "2.0.0",
			hasUpdated:      false,
		},
		{
			name:            "previousVersion is equal to currentVersion",
			previousVersion: "2.0.0",
			currentVersion:  "2.0.0",
			hasUpdated:      false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			hasUpdated := VersionIsOlderThan(tc.previousVersion, tc.currentVersion)
			if hasUpdated != tc.hasUpdated {
				t.Errorf("Expected output to be %v, got %v", tc.hasUpdated, hasUpdated)
			}
		})
	}

}

func TestSemverConstraints(t *testing.T) {

	testCases := []struct {
		name           string
		version        string
		constraints    string
		expectedOutput bool
	}{
		{
			name:           "Version is within constraint",
			version:        "1.2.0",
			constraints:    ">= 1.2.0, <= 1.3.0",
			expectedOutput: true,
		},
		{
			name:           "Updating from 2.0.0",
			version:        "2.0.1",
			constraints:    "< 2.1.0",
			expectedOutput: true,
		},
		{
			name:           "Version is still 2.1.0",
			version:        "2.1.0",
			constraints:    "< 2.1.0",
			expectedOutput: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			c, err := semver.NewConstraint(tc.constraints)
			require.NoError(t, err)

			v, err := semver.NewVersion(tc.version)
			require.NoError(t, err)

			output := c.Check(v)
			if output != tc.expectedOutput {
				t.Errorf("Expected output to be %v, got %v for version %s and constraint %s", tc.expectedOutput, output, tc.version, tc.constraints)
			}
		})
	}

}

func TestValidateReleaseUrl(t *testing.T) {

	testCases := []struct {
		url            string
		expectedOutput bool
	}{
		{
			url:            "https://github.com/5rahim/seanime/releases/download/v3.5.1/seanime-denshi-3.5.1_MacOS_arm64.dmg",
			expectedOutput: true,
		},
		{
			url:            "https://github.com/rando/seanime/releases/download/v3.5.1/seanime-denshi-3.5.1_MacOS_arm64.dmg",
			expectedOutput: false,
		},
		{
			url:            "https://seanime.app/api/updates/stable/seanime-denshi-3.5.1_MacOS_arm64.dmg",
			expectedOutput: true,
		},
		{
			url:            "http://example.com/badstuff.dmg",
			expectedOutput: false,
		},
	}

	for _, tt := range testCases {
		t.Run(tt.url, func(t *testing.T) {
			err := ValidateReleaseUrl(tt.url)
			valid := err == nil
			assert.Equal(t, tt.expectedOutput, valid)
		})
	}
}

// TestCompareVersionForkRevision pins the fork versioning scheme: this fork keeps the
// upstream version it is built on and appends a fourth segment (3.10.2.1 = fork build 1
// on upstream 3.10.2). That form is not valid semver, and CompareVersion returns
// "no update" for anything it cannot parse — so without ParseForkVersion every 3.10.2.x
// build would silently stop offering updates.
func TestCompareVersionForkRevision(t *testing.T) {
	testCases := []struct {
		currVersion    string
		otherVersion   string
		expectedOutput int
		shouldUpdate   bool
	}{
		// The fork revision decides when the upstream release matches.
		{"3.10.2", "3.10.2.1", -1, true},
		{"3.10.2.1", "3.10.2", 1, false},
		{"3.10.2.1", "3.10.2.2", -1, true},
		{"3.10.2.2", "3.10.2.1", 1, false},
		{"3.10.2.1", "3.10.2.1", 0, false},
		// The upstream head still outranks the fork revision.
		{"3.10.2.5", "3.10.3", -1, true},
		{"3.10.2.5", "3.11.0", -2, true},
		{"3.10.2.5", "4.0.0", -3, true},
		{"3.11.0", "3.10.2.9", 2, false},
		// Junk stays rejected rather than coerced into a bogus ordering.
		{"3.10.2.x", "3.10.2.1", 0, false},
		{"not-a-version", "3.10.2", 0, false},
	}

	for _, tc := range testCases {
		t.Run(tc.currVersion+" vs "+tc.otherVersion, func(t *testing.T) {
			output, boolOutput := CompareVersion(tc.currVersion, tc.otherVersion)
			if output != tc.expectedOutput || boolOutput != tc.shouldUpdate {
				t.Errorf("Expected output to be %d and shouldUpdate to be %v, got output=%d and shouldUpdate=%v",
					tc.expectedOutput, tc.shouldUpdate, output, boolOutput)
			}
		})
	}
}

func TestParseForkVersion(t *testing.T) {
	v, rev, err := ParseForkVersion("v3.10.2.4")
	require.NoError(t, err)
	assert.Equal(t, "3.10.2", v.String())
	assert.Equal(t, uint64(4), rev)

	// A plain upstream version is revision 0, so it sorts below any fork build of it.
	_, rev, err = ParseForkVersion("3.10.2")
	require.NoError(t, err)
	assert.Equal(t, uint64(0), rev)

	_, _, err = ParseForkVersion("3.10.2.1.7")
	assert.Error(t, err)
}
