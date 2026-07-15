package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	networkPSTimeout      = 10 * time.Second
	adapterVerifyPoll     = 3
	adapterVerifyInterval = 2 * time.Second
)

// networkMu is purely defensive: it stops two reconcile calls (e.g. a
// duplicate push arriving mid-reconcile) from racing each other's adapter
// enable/disable calls. It plays no role in the idempotency guarantee itself
// — that comes from reconcileNetworkMode always comparing against live OS
// state before acting, so redelivery of the same or a stale desired mode
// converges to the same end state instead of repeating an action.
var networkMu sync.Mutex

// runNetPS runs psCmd via powershell.exe with a short timeout — same idiom
// as agent/executor.go's runPSCommand, just scoped for quick adapter queries
// instead of long-running deploy commands.
func runNetPS(psCmd string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), networkPSTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "powershell.exe",
		"-NonInteractive", "-NoProfile", "-ExecutionPolicy", "Bypass",
		"-Command", psCmd)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}

	out, err := cmd.CombinedOutput()
	output := strings.TrimSpace(string(out))
	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("timeout running: %s", psCmd)
	}
	if err != nil {
		return output, err
	}
	return output, nil
}

// getAdapterStatus reports name's current Status ("Up"/"Disconnected"/
// "Disabled"/...), whether it was found at all, and whether it's currently
// administratively enabled (found and not "Disabled" — link state like
// "Disconnected" still counts as enabled, since Enable/Disable-NetAdapter
// control admin state, not physical link).
func getAdapterStatus(name string) (status string, found bool, enabled bool, err error) {
	safe := strings.ReplaceAll(name, "'", "''")
	psCmd := fmt.Sprintf(
		`$a = Get-NetAdapter -Name '%s' -ErrorAction SilentlyContinue; if ($a) { $a.Status } else { 'NotFound' }`,
		safe)
	out, err := runNetPS(psCmd)
	if err != nil {
		return "", false, false, err
	}
	if out == "NotFound" || out == "" {
		return "NotFound", false, false, nil
	}
	return out, true, out != "Disabled", nil
}

func enableAdapter(name string) error {
	safe := strings.ReplaceAll(name, "'", "''")
	_, err := runNetPS(fmt.Sprintf(`Enable-NetAdapter -Name '%s' -Confirm:$false`, safe))
	return err
}

func disableAdapter(name string) error {
	safe := strings.ReplaceAll(name, "'", "''")
	_, err := runNetPS(fmt.Sprintf(`Disable-NetAdapter -Name '%s' -Confirm:$false`, safe))
	return err
}

// pollUntilEnabled waits for name to report enabled, polling a few times —
// used as a verification gate before disabling a different adapter that name
// is meant to replace, so a transition never passes through a window where
// the only working adapter has already been switched off before its
// replacement is confirmed up. This is the direct fix for the original bug's
// physical root cause (a disable executing before its replacement path was
// confirmed usable).
func pollUntilEnabled(name string) bool {
	for i := 0; i < adapterVerifyPoll; i++ {
		if _, _, enabled, err := getAdapterStatus(name); err == nil && enabled {
			return true
		}
		time.Sleep(adapterVerifyInterval)
	}
	return false
}

func currentCombinedMode(wifiEnabled, ethEnabled bool) string {
	switch {
	case wifiEnabled && ethEnabled:
		return "both"
	case wifiEnabled:
		return "wifi"
	case ethEnabled:
		return "ethernet"
	default:
		return "none"
	}
}

// reconcileNetworkMode converges live adapter state to desiredMode
// ("ethernet"/"wifi"/"both"). It is safe to call any number of times with
// any value, in any order, including concurrently with itself and including
// redelivery of a stale or duplicate desired mode: it always starts by
// reading current truth from the OS, so a call that finds nothing to change
// performs zero adapter actions and simply reports the already-converged
// state. This is what eliminates the old command-replay bug class — there is
// no stored action to redo, only a target and a live comparison against it.
func reconcileNetworkMode(agentID, desiredMode string) {
	networkMu.Lock()
	defer networkMu.Unlock()

	if desiredMode != "ethernet" && desiredMode != "wifi" && desiredMode != "both" {
		sendNetworkModeResult(agentID, "unknown", "error", fmt.Sprintf("unrecognized desired mode: %q", desiredMode))
		return
	}

	wifiName := wifiAdapterName()
	ethName := ethernetAdapterName()

	wifiStatus, wifiFound, wifiEnabled, err1 := getAdapterStatus(wifiName)
	ethStatus, ethFound, ethEnabled, err2 := getAdapterStatus(ethName)

	var errs []string
	if err1 != nil {
		errs = append(errs, fmt.Sprintf("wifi status check failed: %v", err1))
	}
	if err2 != nil {
		errs = append(errs, fmt.Sprintf("ethernet status check failed: %v", err2))
	}
	logMsg("INFO", "NetworkMode: desired=%s wifi=%s(found=%v) ethernet=%s(found=%v)",
		desiredMode, wifiStatus, wifiFound, ethStatus, ethFound)

	wantWifi := desiredMode == "wifi" || desiredMode == "both"
	wantEth := desiredMode == "ethernet" || desiredMode == "both"

	needEnableWifi := wantWifi && wifiFound && !wifiEnabled
	needEnableEth := wantEth && ethFound && !ethEnabled
	needDisableWifi := !wantWifi && wifiFound && wifiEnabled
	needDisableEth := !wantEth && ethFound && ethEnabled

	if !needEnableWifi && !needEnableEth && !needDisableWifi && !needDisableEth {
		status := "ok"
		if len(errs) > 0 {
			status = "error"
		}
		sendNetworkModeResult(agentID, currentCombinedMode(wifiEnabled, ethEnabled), status,
			strings.Join(append(errs, "already converged, no action taken"), "; "))
		return
	}

	// Phase 1: bring up everything that should be up, before touching
	// anything that should go down.
	if needEnableWifi {
		if err := enableAdapter(wifiName); err != nil {
			errs = append(errs, fmt.Sprintf("enable wifi failed: %v", err))
		}
	}
	if needEnableEth {
		if err := enableAdapter(ethName); err != nil {
			errs = append(errs, fmt.Sprintf("enable ethernet failed: %v", err))
		}
	}

	wifiConfirmed := !needEnableWifi
	if needEnableWifi {
		wifiConfirmed = pollUntilEnabled(wifiName)
		if !wifiConfirmed {
			errs = append(errs, "wifi did not come up after enable")
		}
	}
	ethConfirmed := !needEnableEth
	if needEnableEth {
		ethConfirmed = pollUntilEnabled(ethName)
		if !ethConfirmed {
			errs = append(errs, "ethernet did not come up after enable")
		}
	}

	// Phase 2: disable whatever should go down, but only once its
	// replacement (if this transition depends on one) is confirmed up.
	if needDisableWifi {
		if !needEnableEth || ethConfirmed {
			if err := disableAdapter(wifiName); err != nil {
				errs = append(errs, fmt.Sprintf("disable wifi failed: %v", err))
			}
		} else {
			errs = append(errs, "kept wifi enabled: replacement ethernet path not confirmed up")
		}
	}
	if needDisableEth {
		if !needEnableWifi || wifiConfirmed {
			if err := disableAdapter(ethName); err != nil {
				errs = append(errs, fmt.Sprintf("disable ethernet failed: %v", err))
			}
		} else {
			errs = append(errs, "kept ethernet enabled: replacement wifi path not confirmed up")
		}
	}

	// Final verification: re-detect actual state and report truth, not intent.
	_, _, finalWifiEnabled, _ := getAdapterStatus(wifiName)
	_, _, finalEthEnabled, _ := getAdapterStatus(ethName)
	currentMode := currentCombinedMode(finalWifiEnabled, finalEthEnabled)

	status := "ok"
	if len(errs) > 0 {
		status = "partial"
	}
	if (wantWifi && wifiFound && !finalWifiEnabled) || (wantEth && ethFound && !finalEthEnabled) || currentMode == "none" {
		status = "error"
	}

	output := "converged to " + currentMode
	if len(errs) > 0 {
		output = strings.Join(errs, "; ")
	}
	sendNetworkModeResult(agentID, currentMode, status, output)
}

func sendNetworkModeResult(agentID, mode, status, output string) {
	logMsg("INFO", "NetworkMode: result mode=%s status=%s output=%s", mode, status, output)
	resp := map[string]interface{}{
		"type":         "network_mode_result",
		"agent_id":     agentID,
		"network_mode": mode,
		"status":       status,
		"output":       output,
	}
	data, _ := json.Marshal(resp)
	wsSend(data)
}
