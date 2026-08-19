// Software inventory identity, version-comparison, and uninstall-command
// validation logic — used identically by both the agent (its own local
// snapshot map keys and the final pre-execution gate for a quiet-uninstall
// job) and the server (software_inventory table keys, uninstall-tier
// classification, and dashboard display), so the two sides can never
// silently disagree about what counts as "the same software" or "a safe
// command to run." See server/software.go and agent/softwareinventory.go
// for the call sites.
package shared

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
)

// ─── Identity ──────────────────────────────────────────────────────────────

// SoftwareIdentity is the identifying information for one installed-software
// registry entry, gathered by the agent's registry scanner and mirrored
// server-side when reconciling a software_inventory_snapshot into the
// software_inventory table.
type SoftwareIdentity struct {
	WindowsInstaller bool
	ProductCode      string // raw registry value, braces optional
	DisplayName      string
	Publisher        string
}

var msiProductCodeRe = regexp.MustCompile(`^\{?([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})\}?$`)

// NormalizeProductCode validates raw as an MSI ProductCode GUID (braces
// optional, case-insensitive) and returns it normalized to uppercase with
// braces, e.g. "{A1B2C3D4-...}". ok is false if raw isn't a valid GUID.
func NormalizeProductCode(raw string) (normalized string, ok bool) {
	m := msiProductCodeRe.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return "", false
	}
	return "{" + strings.ToUpper(m[1]) + "}", true
}

// Key returns a stable, deterministic identity string for this software
// entry, used both as the software_inventory primary-key component and as
// the agent's local snapshot map key.
//
// Identity algorithm (documented here so future changes to either tier
// don't accidentally break reconciliation):
//   - MSI entries (WindowsInstaller set AND ProductCode is a valid GUID):
//     key = "msi:" + the normalized (uppercase, braced) ProductCode.
//     DisplayName/Publisher/version play no part — the ProductCode alone
//     identifies the product across machines and across version upgrades.
//   - Everything else: key = "np:" + lower(trim(DisplayName)) + "|" +
//     lower(trim(Publisher)). Version plays no part here either, so the
//     same software at different versions on different endpoints still
//     aggregates into one fleet-wide row.
//
// The "msi:" and "np:" prefixes are permanent, disjoint namespaces: an MSI
// product and a non-MSI product that happen to share a display name and
// publisher can never collide, by construction.
func (s SoftwareIdentity) Key() string {
	if s.WindowsInstaller {
		if code, ok := NormalizeProductCode(s.ProductCode); ok {
			return "msi:" + code
		}
	}
	name := strings.ToLower(strings.TrimSpace(s.DisplayName))
	pub := strings.ToLower(strings.TrimSpace(s.Publisher))
	return "np:" + name + "|" + pub
}

// ─── Version comparison ─────────────────────────────────────────────────────

var versionDigitsRe = regexp.MustCompile(`\d+`)

// CompareVersions compares two version strings by extracting their numeric
// components (runs of digits, in order) and comparing them component-wise,
// treating missing trailing components as 0 — so "1.2" == "1.2.0" and
// "v1.2.3" == "1.2.3.0" (a leading "v"/"V" and any non-digit separators are
// simply not part of either component list).
//
// ok is false when either string has zero extractable numeric components —
// callers must treat that as "cannot be confidently compared" (e.g. map to
// an "unknown" update status) rather than guessing from raw string
// inequality.
func CompareVersions(installed, available string) (cmp int, ok bool) {
	a := extractVersionParts(installed)
	b := extractVersionParts(available)
	if len(a) == 0 || len(b) == 0 {
		return 0, false
	}
	n := len(a)
	if len(b) > n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		var av, bv uint64
		if i < len(a) {
			av = a[i]
		}
		if i < len(b) {
			bv = b[i]
		}
		if av != bv {
			if av < bv {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func extractVersionParts(s string) []uint64 {
	matches := versionDigitsRe.FindAllString(s, -1)
	if len(matches) == 0 {
		return nil
	}
	parts := make([]uint64, 0, len(matches))
	for _, m := range matches {
		v, err := strconv.ParseUint(m, 10, 64)
		if err != nil {
			// Absurdly long digit run (not a realistic version
			// component) — clamp instead of failing the whole
			// comparison outright.
			v = ^uint64(0)
		}
		parts = append(parts, v)
	}
	return parts
}

// ─── Quiet-uninstall command validation ─────────────────────────────────────

// blocklistedUninstallHosts are executable basenames (case-insensitive)
// ParseQuietUninstallCommand refuses to accept as argv[0] — shell and
// script-host interpreters whose own arguments could be used to run
// arbitrary code, which would defeat the point of executing a registered
// uninstall command as a structured argv instead of a shell string.
// msiexec.exe is intentionally NOT here: it's a fixed-purpose Windows
// installer service, and a QuietUninstallString that invokes it (e.g.
// "MsiExec.exe /X{GUID}") is exactly the same operation the MSI uninstall
// tier already performs directly with a regex-validated GUID.
var blocklistedUninstallHosts = map[string]bool{
	"cmd.exe": true, "powershell.exe": true, "pwsh.exe": true,
	"wscript.exe": true, "cscript.exe": true, "mshta.exe": true,
	"rundll32.exe": true, "regsvr32.exe": true, "cmstp.exe": true,
	"installutil.exe": true, "msbuild.exe": true, "forfiles.exe": true,
	"certutil.exe": true, "bitsadmin.exe": true, "wmic.exe": true,
	"conhost.exe": true, "control.exe": true,
}

// absoluteExePathRe requires a drive-letter-rooted local path ending in
// .exe — this alone is what rules out UNC paths, bare command names, and
// anything that would need a shell to resolve.
var absoluteExePathRe = regexp.MustCompile(`(?i)^[A-Za-z]:\\[^\\].*\.exe$`)

var errUnbalancedQuote = errors.New("shared: unbalanced quote in command line")

// ParseQuietUninstallCommand validates and tokenizes a QuietUninstallString
// (or any similarly-shaped registry uninstall command) into a directly
// executable program + argument list — never a shell string. ok is false
// when raw cannot be safely treated as a structured, non-interactive
// uninstall invocation:
//   - empty, or contains a NUL byte
//   - fails to tokenize (e.g. an unbalanced quote)
//   - argv[0] is not an absolute, drive-letter-rooted ".exe" path
//   - argv[0]'s basename matches blocklistedUninstallHosts
//
// Callers (both server-side uninstall-tier classification and the agent's
// final pre-execution gate) must treat ok=false as "uninstall unsupported"
// and never fall back to running raw via a shell.
func ParseQuietUninstallCommand(raw string) (exe string, args []string, ok bool) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", nil, false
	}
	tokens, err := tokenizeCommandLine(raw)
	if err != nil || len(tokens) == 0 {
		return "", nil, false
	}
	candidate := tokens[0]
	if !absoluteExePathRe.MatchString(candidate) {
		return "", nil, false
	}
	base := strings.ToLower(candidate[strings.LastIndexByte(candidate, '\\')+1:])
	if blocklistedUninstallHosts[base] {
		return "", nil, false
	}
	return candidate, tokens[1:], true
}

// tokenizeCommandLine splits a Windows-style command line into argv,
// honoring double-quoted segments (their contents, including whitespace,
// stay one token) but without attempting full CommandLineToArgvW
// backslash-escaping fidelity — registry uninstall strings are simple
// "path" plus flags, not arbitrarily escaped input, so this is deliberately
// the smallest tokenizer that handles that shape correctly and rejects
// anything it can't parse confidently (an unbalanced quote) rather than
// guessing.
func tokenizeCommandLine(s string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuotes := false
	hasToken := false
	for _, r := range s {
		switch {
		case r == '"':
			inQuotes = !inQuotes
			hasToken = true
		case r == ' ' || r == '\t':
			if inQuotes {
				cur.WriteRune(r)
			} else if hasToken {
				tokens = append(tokens, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteRune(r)
			hasToken = true
		}
	}
	if inQuotes {
		return nil, errUnbalancedQuote
	}
	if hasToken {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}
