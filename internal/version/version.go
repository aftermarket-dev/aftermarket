// Package version holds the release version. The release workflow publishes
// a release whenever this changes to a version without a tag and CI is green.
// Versions ending in -dev are never released.
package version

// Version is the Aftermarket version.
const Version = "0.5.1"
