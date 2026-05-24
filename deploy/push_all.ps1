#Requires -Version 5.1
<#
.SYNOPSIS
    Mass-deploy Library Monitor agent to all PCs listed in ips.txt via WinRM.

.PARAMETER User
    Local administrator username (same on all PCs).

.PARAMETER Pass
    Local administrator password.

.PARAMETER Server
    Optional WebSocket server URL to write as server.txt on each PC.
    Default: ws://192.168.1.10:8080/ws

.EXAMPLE
    .\push_all.ps1 -User "Administrator" -Pass "secret"
    .\push_all.ps1 -User "Administrator" -Pass "secret" -Server "ws://192.168.1.10:8080/ws"
#>
param(
    [Parameter(Mandatory)]
    [string]$User,

    [Parameter(Mandatory)]
    [string]$Pass,

    [string]$Server = "ws://192.168.1.10:8080/ws"
)

$ErrorActionPreference = "Continue"
$ScriptDir  = Split-Path -Parent $MyInvocation.MyCommand.Path
$IpsFile    = Join-Path $ScriptDir "ips.txt"
$LogFile    = Join-Path $ScriptDir "deploy_log.txt"
$AgentExe   = Join-Path $ScriptDir "agent.exe"

# Validate prerequisites
if (-not (Test-Path $IpsFile)) {
    Write-Error "ips.txt not found in $ScriptDir"
    exit 1
}
if (-not (Test-Path $AgentExe)) {
    Write-Error "agent.exe not found in $ScriptDir"
    exit 1
}

$IPs  = Get-Content $IpsFile | Where-Object { $_ -match '\S' } | ForEach-Object { $_.Trim() }
$Cred = New-Object System.Management.Automation.PSCredential(
    $User,
    (ConvertTo-SecureString $Pass -AsPlainText -Force)
)

$StartTime = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
$Header    = "=== Deploy started $StartTime | targets: $($IPs.Count) ==="
Write-Host $Header
Add-Content $LogFile $Header

$Ok  = 0
$Err = 0

foreach ($IP in $IPs) {
    $TS  = Get-Date -Format "HH:mm:ss"
    Write-Host "[$TS] $IP ..." -NoNewline

    try {
        $SessionOpt = New-PSSessionOption -OperationTimeout 30000 -OpenTimeout 15000
        $Session    = New-PSSession -ComputerName $IP -Credential $Cred `
                        -SessionOption $SessionOpt -ErrorAction Stop

        # Create agent directory
        Invoke-Command -Session $Session -ScriptBlock {
            if (-not (Test-Path "C:\LibraryAgent")) {
                New-Item -ItemType Directory -Path "C:\LibraryAgent" | Out-Null
            }
        }

        # Copy agent binary
        Copy-Item -Path $AgentExe -Destination "C:\LibraryAgent\agent.exe" `
            -ToSession $Session -Force

        # Write server URL
        Invoke-Command -Session $Session -ScriptBlock {
            param($url)
            Set-Content "C:\LibraryAgent\server.txt" $url -Encoding UTF8
        } -ArgumentList $Server

        # Register and start Task Scheduler entry
        Invoke-Command -Session $Session -ScriptBlock {
            $TaskName = "LibraryAgent"
            schtasks /Delete /TN $TaskName /F 2>$null | Out-Null
            schtasks /Create /TN $TaskName /TR '"C:\LibraryAgent\agent.exe"' `
                /SC ONSTART /RU SYSTEM /RL HIGHEST /F | Out-Null
            schtasks /Run /TN $TaskName | Out-Null
        }

        Remove-PSSession $Session

        $Msg = "[OK]  $IP"
        Write-Host " OK"
        Add-Content $LogFile $Msg
        $Ok++
    }
    catch {
        $Msg = "[ERR] $IP — $($_.Exception.Message)"
        Write-Host " FAIL"
        Write-Host "      $($_.Exception.Message)" -ForegroundColor Red
        Add-Content $LogFile $Msg
        $Err++
    }
}

$EndTime = Get-Date -Format "yyyy-MM-dd HH:mm:ss"
$Footer  = "=== Deploy finished $EndTime | OK: $Ok  ERR: $Err ==="
Write-Host $Footer
Add-Content $LogFile $Footer
Write-Host "Log: $LogFile"
