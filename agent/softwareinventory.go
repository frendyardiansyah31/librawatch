package main

import (
	"context"
	"encoding/json"
	"math/rand"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows/registry"

	"library-monitor/shared"
)

// softwareEntry is one installed-software registry entry, as read directly
// from an Uninstall key, optionally enriched with winget's view of the
// latest available version. This is a full state snapshot, separate from
// installwatch.go's diff-based software_installed/updated/removed events
// (which continue to feed the existing Applications catalog, untouched).
type softwareEntry struct {
	DisplayName            string
	Publisher              string
	DisplayVersion         string
	InstallDate            string
	WindowsInstaller       bool
	ProductCode            string
	UninstallString        string
	QuietUninstallString   string
	Hive                   string // "HKLM" | "HKCU"
	Arch                   string // "x64" | "x86"
	Scope                  string // "machine" | "user"
	WingetID               string
	WingetAvailableVersion string
}

func (e softwareEntry) identityKey() string {
	return shared.SoftwareIdentity{
		WindowsInstaller: e.WindowsInstaller,
		ProductCode:      e.ProductCode,
		DisplayName:      e.DisplayName,
		Publisher:        e.Publisher,
	}.Key()
}

type inventoryRoot struct {
	root  registry.Key
	path  string
	hive  string
	arch  string
	scope string
}

var inventoryRoots = []inventoryRoot{
	{registry.LOCAL_MACHINE, `Software\Microsoft\Windows\CurrentVersion\Uninstall`, "HKLM", "x64", "machine"},
	{registry.LOCAL_MACHINE, `Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall`, "HKLM", "x86", "machine"},
	{registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Uninstall`, "HKCU", "x64", "user"},
}

// scanFullInventory enumerates all three Uninstall registry roots (HKLM
// 64-bit, HKLM WOW6432Node/32-bit, and HKCU for per-user installs — the
// third root installwatch.go's diff-watcher does not cover) and returns
// every entry with a non-empty DisplayName, same filter installwatch.go
// already applies. This is registry-only: no winget call happens here.
func scanFullInventory() []softwareEntry {
	var entries []softwareEntry
	for _, r := range inventoryRoots {
		entries = append(entries, scanInventoryRoot(r)...)
	}
	return entries
}

func scanInventoryRoot(r inventoryRoot) []softwareEntry {
	k, err := registry.OpenKey(r.root, r.path, registry.ENUMERATE_SUB_KEYS|registry.READ)
	if err != nil {
		// Missing root (e.g. no WOW6432Node on some hosts, or no per-user
		// installs under HKCU) is normal, not an error.
		return nil
	}
	defer k.Close()

	names, err := k.ReadSubKeyNames(-1)
	if err != nil {
		return nil
	}

	var out []softwareEntry
	for _, name := range names {
		sub, err := registry.OpenKey(k, name, registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		displayName, _, err := sub.GetStringValue("DisplayName")
		if err != nil || displayName == "" {
			sub.Close()
			continue
		}
		publisher, _, _ := sub.GetStringValue("Publisher")
		version, _, _ := sub.GetStringValue("DisplayVersion")
		installDate, _, _ := sub.GetStringValue("InstallDate")
		uninstallString, _, _ := sub.GetStringValue("UninstallString")
		quietUninstallString, _, _ := sub.GetStringValue("QuietUninstallString")

		windowsInstaller := false
		if v, _, err := sub.GetIntegerValue("WindowsInstaller"); err == nil && v == 1 {
			windowsInstaller = true
		}
		productCode := ""
		if windowsInstaller {
			// For MSI entries the registry subkey name itself is the
			// ProductCode GUID (Uninstall\{GUID}).
			if _, ok := shared.NormalizeProductCode(name); ok {
				productCode = name
			}
		}
		sub.Close()

		out = append(out, softwareEntry{
			DisplayName: displayName, Publisher: publisher, DisplayVersion: version,
			InstallDate: installDate, WindowsInstaller: windowsInstaller, ProductCode: productCode,
			UninstallString: uninstallString, QuietUninstallString: quietUninstallString,
			Hive: r.hive, Arch: r.arch, Scope: r.scope,
		})
	}
	return out
}

// ─── Winget enrichment (additive only — never gates inventory) ────────────

// wingetListTimeout is short and separate from executor.go's 10-minute
// execTimeout: this is a background enrichment pass, not an admin-issued
// command, and must not hold up the periodic snapshot loop.
const wingetListTimeout = 2 * time.Minute

type wingetRow struct {
	Name, ID, Version, Available string
}

// enrichWithWinget layers winget_id/winget_available_version onto entries
// already sourced from the registry. Any failure (winget not installed,
// timeout, non-zero exit, unparsable output) is logged and swallowed —
// entries is returned unmodified so a broken/missing winget never removes
// software from inventory or blocks a snapshot.
func enrichWithWinget(entries []softwareEntry) []softwareEntry {
	out, err := runWingetList()
	if err != nil {
		logMsg("WARN", "software inventory: winget enrichment skipped: %v", err)
		return entries
	}
	rows := parseWingetListOutput(out)
	if len(rows) == 0 {
		return entries
	}
	byName := make(map[string]wingetRow, len(rows))
	for _, row := range rows {
		byName[normalizeSoftwareName(row.Name)] = row
	}
	for i := range entries {
		if row, ok := byName[normalizeSoftwareName(entries[i].DisplayName)]; ok {
			entries[i].WingetID = row.ID
			entries[i].WingetAvailableVersion = row.Available
		}
	}
	return entries
}

func normalizeSoftwareName(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

func runWingetList() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), wingetListTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "winget", "list", "--accept-source-agreements", "--disable-interactivity")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// parseWingetListOutput parses `winget list`'s column-aligned text table by
// locating the header row's column start offsets ("Id", "Version",
// "Available") and slicing every data row at those same offsets — the
// standard approach for this format, since column widths vary by content
// and locale. Rows that don't parse cleanly are skipped, not fatal.
func parseWingetListOutput(out string) []wingetRow {
	lines := strings.Split(out, "\n")
	headerIdx := -1
	for i, l := range lines {
		if strings.Contains(l, "Id") && strings.Contains(l, "Version") {
			headerIdx = i
			break
		}
	}
	if headerIdx == -1 || headerIdx+2 >= len(lines) {
		return nil
	}
	header := lines[headerIdx]
	idCol := strings.Index(header, "Id")
	versionCol := strings.Index(header, "Version")
	availableCol := strings.Index(header, "Available")
	if idCol < 0 || versionCol < 0 {
		return nil
	}

	slice := func(line string, from, to int) string {
		if from >= len(line) {
			return ""
		}
		if to < 0 || to > len(line) {
			to = len(line)
		}
		if to < from {
			return ""
		}
		return strings.TrimSpace(line[from:to])
	}

	var rows []wingetRow
	for _, l := range lines[headerIdx+2:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		name := slice(l, 0, idCol)
		id := slice(l, idCol, versionCol)
		var version, available string
		if availableCol > 0 {
			version = slice(l, versionCol, availableCol)
			available = slice(l, availableCol, -1)
		} else {
			version = slice(l, versionCol, -1)
		}
		if name == "" || id == "" {
			continue
		}
		rows = append(rows, wingetRow{Name: name, ID: id, Version: version, Available: available})
	}
	return rows
}

// ─── Concurrency-safe local snapshot state ─────────────────────────────────
//
// Guards the agent's own most-recently-built inventory, read by the
// quiet-uninstall self-consistency check (executeQuietUninstall) and
// written by every snapshot cycle (buildAndSendSnapshot) — potentially
// concurrently, since the snapshot loop, a WebSocket-dispatched uninstall
// job, and a reconnect-triggered resend can all run at once. Same bare
// mutex-guarded package-var idiom already used for other shared agent state
// (see ackStoreMu in ack.go, networkMu in network.go) rather than a new
// abstraction.

var (
	invMu    sync.RWMutex
	invByKey map[string]softwareEntry
)

// replaceInventorySnapshot atomically swaps the agent's local view of its
// own installed software. The new map is fully built before the lock is
// taken, so the lock is only ever held for the pointer swap — never across
// a registry scan or external process call.
func replaceInventorySnapshot(entries []softwareEntry) {
	m := make(map[string]softwareEntry, len(entries))
	for _, e := range entries {
		m[e.identityKey()] = e
	}
	invMu.Lock()
	invByKey = m
	invMu.Unlock()
}

// lookupInventoryEntry reads a consistent snapshot of one entry. Callers
// must not hold the returned value across a subsequent external-process
// call as if it were still "live" — it's a point-in-time copy.
func lookupInventoryEntry(key string) (softwareEntry, bool) {
	invMu.RLock()
	defer invMu.RUnlock()
	e, ok := invByKey[key]
	return e, ok
}

// ─── Periodic snapshot loop ─────────────────────────────────────────────────

// inventorySnapshotInterval is a package-level var, not a buried literal,
// so it's a one-line change if a shorter/configurable interval is needed
// later (no settings-push channel exists for this today — out of scope).
var inventorySnapshotInterval = 6 * time.Hour

// startSoftwareInventoryLoop sends a full inventory snapshot shortly after
// start (jittered 0-60s to avoid a thundering herd if many agents restart
// together, e.g. after a mass agent update) and then on every
// inventorySnapshotInterval tick, for the lifetime of ctx. Called from
// startEventWatchers in agent/main.go alongside startInstallWatch.
func startSoftwareInventoryLoop(ctx context.Context, agentID string) {
	jitter := time.Duration(rand.Intn(60)) * time.Second
	select {
	case <-ctx.Done():
		return
	case <-time.After(jitter):
	}
	buildAndSendSnapshot(agentID)

	ticker := time.NewTicker(inventorySnapshotInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			buildAndSendSnapshot(agentID)
		}
	}
}

// buildAndSendSnapshot scans the registry, layers on winget enrichment,
// updates the agent's local snapshot state, and sends the full inventory to
// the server as a software_inventory_snapshot message — never a diff, so
// the server can fully reconcile (including detecting removals) from this
// message alone.
func buildAndSendSnapshot(agentID string) {
	entries := scanFullInventory()
	entries = enrichWithWinget(entries)
	replaceInventorySnapshot(entries)

	items := make([]map[string]interface{}, 0, len(entries))
	for _, e := range entries {
		items = append(items, map[string]interface{}{
			"display_name": e.DisplayName, "publisher": e.Publisher, "display_version": e.DisplayVersion,
			"install_date": e.InstallDate, "windows_installer": e.WindowsInstaller, "product_code": e.ProductCode,
			"uninstall_string": e.UninstallString, "quiet_uninstall_string": e.QuietUninstallString,
			"hive": e.Hive, "arch": e.Arch, "scope": e.Scope,
			"winget_id": e.WingetID, "winget_available_version": e.WingetAvailableVersion,
		})
	}
	msg := map[string]interface{}{
		"type":     "software_inventory_snapshot",
		"agent_id": agentID,
		"items":    items,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		logMsg("ERROR", "software inventory: marshal snapshot failed: %v", err)
		return
	}
	wsSend(data)
	logMsg("INFO", "software inventory: snapshot sent, %d items", len(entries))
}

// triggerImmediateSnapshot schedules a fresh snapshot shortly after an
// uninstall job finishes (any tier), so the server's next reconciliation
// pass — not the job's exit code alone — is what confirms the software is
// actually gone. The short delay lets the just-uninstalled registry key
// finish disappearing first.
func triggerImmediateSnapshot(agentID string) {
	go func() {
		time.Sleep(5 * time.Second)
		buildAndSendSnapshot(agentID)
	}()
}

// ─── Structured (non-shell) uninstall execution ────────────────────────────

// runStructuredCommand runs exe with args directly via exec.Command — never
// through powershell.exe -Command or any other shell — and reports the same
// psResult shape runPSCommand (executor.go) produces, so sendExecResult/
// logPSResult need no changes. This is the execution primitive for both new
// uninstall tiers; the existing runPSCommand/executeCommand path used for
// the admin's manual free-text "exec" job type is untouched and shares no
// code with this beyond the result shape.
func runStructuredCommand(exe string, args []string) psResult {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	out, err := cmd.CombinedOutput()
	durationMS := time.Since(start).Milliseconds()
	status := "success"
	output := strings.TrimSpace(string(out))
	exitCode := 0

	switch {
	case ctx.Err() == context.DeadlineExceeded:
		status = "error"
		exitCode = -1
		if output != "" {
			output += "\n"
		}
		output += "[timeout: command exceeded execution time limit]"
	case err != nil:
		status = "error"
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
		if output != "" {
			output += "\n" + err.Error()
		} else {
			output = err.Error()
		}
	}

	return psResult{Status: status, Output: output, ExitCode: exitCode, DurationMS: durationMS}
}

// executeMsiexecUninstall handles the "msiexec_uninstall" job type (Tier A).
// payload is expected to be nothing but a validated ProductCode GUID — the
// agent re-validates it locally regardless (defense in depth against a
// compromised/relaying server) before building the msiexec argv itself.
func executeMsiexecUninstall(agentID string, msg map[string]interface{}) {
	jobID, _ := msg["job_id"].(string)
	payload, _ := msg["payload"].(string)
	attempt := msg["attempt"]

	guid, ok := shared.NormalizeProductCode(payload)
	if !ok {
		logMsg("ERROR", "msiexec uninstall job=%s: invalid product code payload", jobID)
		sendExecResult(agentID, jobID, attempt, psResult{
			Status: "error", Output: "uninstall unsupported: invalid product code", ExitCode: -1,
		})
		return
	}

	logMsg("INFO", "Executing msiexec uninstall job=%s product_code=%s", jobID, guid)
	r := runStructuredCommand("msiexec.exe", []string{"/x", guid, "/quiet", "/norestart"})
	logPSResult("Msiexec uninstall", jobID, r)
	sendExecResult(agentID, jobID, attempt, r)
	triggerImmediateSnapshot(agentID)
}

// executeQuietUninstall handles the "quiet_uninstall" job type (Tier B).
// args carries the software's identity key so the agent can look up its own
// last-known inventory entry. Three gates must all pass before anything is
// executed — self-consistency against the agent's own last snapshot,
// structural validation of the command, and on-disk existence of the target
// executable — matching the security model's steps 5-7. Any failure results
// in "uninstall unsupported", never a fallback to raw/shell execution.
func executeQuietUninstall(agentID string, msg map[string]interface{}) {
	jobID, _ := msg["job_id"].(string)
	payload, _ := msg["payload"].(string)
	identityKey, _ := msg["args"].(string)
	attempt := msg["attempt"]

	// Step 5: self-consistency — payload must exact-match what this agent
	// itself last reported for this identity key.
	entry, found := lookupInventoryEntry(identityKey)
	if !found || entry.QuietUninstallString == "" || entry.QuietUninstallString != payload {
		logMsg("ERROR", "quiet uninstall job=%s: payload does not match agent's own last-known inventory for key=%s", jobID, identityKey)
		sendExecResult(agentID, jobID, attempt, psResult{
			Status: "error", Output: "uninstall unsupported: stale or mismatched inventory state", ExitCode: -1,
		})
		return
	}

	// Step 6: structural validation — same allowlist logic the server used
	// to classify this as Tier B; the agent is the authoritative final gate.
	exe, args, ok := shared.ParseQuietUninstallCommand(payload)
	if !ok {
		logMsg("ERROR", "quiet uninstall job=%s: command failed structural validation", jobID)
		sendExecResult(agentID, jobID, attempt, psResult{
			Status: "error", Output: "uninstall unsupported: command could not be safely validated", ExitCode: -1,
		})
		return
	}
	if info, statErr := os.Stat(exe); statErr != nil || info.IsDir() {
		logMsg("ERROR", "quiet uninstall job=%s: target executable not found: %s", jobID, exe)
		sendExecResult(agentID, jobID, attempt, psResult{
			Status: "error", Output: "uninstall unsupported: target executable not found", ExitCode: -1,
		})
		return
	}

	// Step 7: execute — structured argv, never a shell.
	logMsg("INFO", "Executing quiet uninstall job=%s exe=%s", jobID, exe)
	r := runStructuredCommand(exe, args)
	logPSResult("Quiet uninstall", jobID, r)
	sendExecResult(agentID, jobID, attempt, r)
	triggerImmediateSnapshot(agentID)
}
