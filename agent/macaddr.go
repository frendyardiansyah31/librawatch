package main

// getMACAddress mirrors the legacy report.ps1 selection logic: the network
// adapter that has a default gateway and is Up, preferring the lowest
// InterfaceMetric when more than one qualifies. Returns "" if none is found
// or the PowerShell query fails — MAC is best-effort, never blocks startup.
func getMACAddress() string {
	out, err := runNetPS(`$c = Get-NetIPConfiguration | Where-Object { $_.IPv4DefaultGateway -ne $null -and $_.NetAdapter.Status -eq 'Up' } | Sort-Object { $_.NetAdapter.InterfaceMetric } | Select-Object -First 1; if ($c) { $c.NetAdapter.MacAddress } else { '' }`)
	if err != nil {
		return ""
	}
	return out
}
