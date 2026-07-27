// Package shared holds data types and pure matching logic used by both the
// server and agent Go modules (linked via the repo's go.work workspace) so
// identity/policy-matching rules live in exactly one place instead of being
// hand-duplicated across the two modules and silently drifting apart.
package shared

import "strings"

// ApplicationIdentity is the identifying information for a single
// executable, gathered from whatever sources are actually available (PE
// version-info resources, a file hash, or nothing at all for tools with no
// embedded metadata). Every field is optional — construct one with whatever
// you have; no field beyond ExeName is required, and nothing here panics on
// missing data.
type ApplicationIdentity struct {
	ExeName          string
	Company          string
	ProductName      string
	OriginalFilename string
	FileDescription  string
	SHA256           string
	FullPath         string
}

// NewApplicationIdentity builds the minimal identity known at process-start
// time — just the exe's own name and path — to be filled in later via
// WithMetadata once (if ever) richer data is extracted.
func NewApplicationIdentity(exeName, fullPath string) ApplicationIdentity {
	return ApplicationIdentity{ExeName: exeName, FullPath: fullPath}
}

// WithMetadata returns a copy of id with the given fields filled in only
// where they're non-empty, so a later call with partial/missing data never
// blanks out what an earlier call already established — the same
// don't-blank-on-refresh rule already applied at the DB layer by
// UpsertApplication.
func (id ApplicationIdentity) WithMetadata(company, productName, originalFilename, fileDescription, sha256 string) ApplicationIdentity {
	out := id
	if company != "" {
		out.Company = company
	}
	if productName != "" {
		out.ProductName = productName
	}
	if originalFilename != "" {
		out.OriginalFilename = originalFilename
	}
	if fileDescription != "" {
		out.FileDescription = fileDescription
	}
	if sha256 != "" {
		out.SHA256 = sha256
	}
	return out
}

// Key returns a stable, deterministic string identifying this app for
// logging and map-keying — prefers the most specific field available,
// falling back to ExeName alone when nothing else is known.
func (id ApplicationIdentity) Key() string {
	switch {
	case id.SHA256 != "":
		return "sha256:" + id.SHA256
	case id.ProductName != "" && id.Company != "":
		return strings.ToLower(id.ProductName) + "|" + strings.ToLower(id.Company)
	default:
		return strings.ToLower(id.ExeName)
	}
}

// SameApp reports whether id and other most likely refer to the same
// real-world application, most-specific signal first. Used by the kill path
// to re-verify a PID's live identity still matches the one a policy
// decision was made against, and by the catalog layer to decide whether an
// install-detected row and a process-detected row represent the same app.
func (id ApplicationIdentity) SameApp(other ApplicationIdentity) bool {
	if id.SHA256 != "" && other.SHA256 != "" {
		return id.SHA256 == other.SHA256
	}
	if id.ProductName != "" && id.Company != "" && other.ProductName != "" && other.Company != "" {
		return strings.EqualFold(id.ProductName, other.ProductName) && strings.EqualFold(id.Company, other.Company)
	}
	return id.ExeName != "" && strings.EqualFold(id.ExeName, other.ExeName)
}
