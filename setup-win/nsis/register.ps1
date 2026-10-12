# Run (elevated) by the NSIS installer after the files are in place: lock down
# config.json, register the logon task, open the firewall for P2P, keep the
# machine awake on AC power, then start the agent. Same steps as install.ps1.
param([Parameter(Mandatory = $true)][string]$Dir)
$ErrorActionPreference = 'Stop'
$TaskName = 'RemoteDesk'
$agentExe = Join-Path $Dir 'remote-agent.exe'

# The agent runs as the person signed in at the console, not as whoever approved UAC.
$user = (Get-CimInstance Win32_ComputerSystem).UserName
if (-not $user) { $user = "$env:USERDOMAIN\$env:USERNAME" }

# The config holds the token: only SYSTEM, Administrators and that user may read it.
icacls (Join-Path $Dir 'config.json') /inheritance:r /grant:r 'SYSTEM:F' '*S-1-5-32-544:F' "${user}:R" | Out-Null

netsh advfirewall firewall delete rule name="RemoteDesk agent" 2>&1 | Out-Null
netsh advfirewall firewall add rule name="RemoteDesk agent" dir=in action=allow program="$agentExe" protocol=UDP enable=yes profile=any | Out-Null

$action = New-ScheduledTaskAction -Execute $agentExe -WorkingDirectory $Dir
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -MultipleInstances IgnoreNew -StartWhenAvailable
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null

powercfg /change monitor-timeout-ac 0
powercfg /change standby-timeout-ac 0
powercfg /change hibernate-timeout-ac 0

Start-ScheduledTask -TaskName $TaskName
