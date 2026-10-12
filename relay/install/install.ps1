# RemoteDesk agent installer for Windows (served by the relay with server/token filled in).
# Run in PowerShell:   irm "{{SERVER}}/install.ps1?t=<token>" | iex
$ErrorActionPreference = 'Stop'
try { [Console]::OutputEncoding = [Text.Encoding]::UTF8 } catch {}
$ProgressPreference = 'SilentlyContinue'
[Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12

$Server = '{{SERVER}}'
$Token = '{{TOKEN}}'
$WithApp = '{{WITH_APP}}' -eq '1'   # also install the RemoteDesk viewer app
$Dir = Join-Path $env:ProgramData 'RemoteDesk'
$TaskName = 'RemoteDesk'

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
if (-not $isAdmin) {
    Write-Host 'Cần quyền Administrator — đang mở cửa sổ PowerShell nâng quyền...' -ForegroundColor Yellow
    Start-Process powershell -Verb RunAs -ArgumentList "-NoProfile -ExecutionPolicy Bypass -NoExit -Command `"irm '$Server/install.ps1?t=$Token&app={{WITH_APP}}' | iex`""
    return
}

Write-Host "==> Cài RemoteDesk agent vào $Dir" -ForegroundColor Cyan
New-Item -ItemType Directory -Force -Path $Dir | Out-Null

# Stop a running agent so its exe can be replaced.
Stop-ScheduledTask -TaskName $TaskName -ErrorAction SilentlyContinue
Get-Process remote-agent -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 500

Write-Host '==> Tải agent...'
Invoke-WebRequest -UseBasicParsing -Headers @{ Authorization = "Bearer $Token" } `
    -Uri "$Server/dl/remote-agent-windows-amd64.exe" -OutFile (Join-Path $Dir 'remote-agent.exe')

$ff = Join-Path $Dir 'ffmpeg.exe'
if (-not (Test-Path $ff)) {
    Write-Host '==> Tải ffmpeg (~100 MB, chỉ lần đầu)...'
    $zip = Join-Path $env:TEMP 'rd-ffmpeg.zip'
    $tmp = Join-Path $env:TEMP 'rd-ffmpeg'
    $sources = @(
        'https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip',
        'https://github.com/BtbN/FFmpeg-Builds/releases/download/latest/ffmpeg-master-latest-win64-gpl.zip'
    )
    $ok = $false
    foreach ($src in $sources) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri $src -OutFile $zip
            if (Test-Path $tmp) { Remove-Item -Recurse -Force $tmp }
            Expand-Archive -Path $zip -DestinationPath $tmp -Force
            $exe = Get-ChildItem -Path $tmp -Recurse -Filter ffmpeg.exe | Select-Object -First 1
            if ($exe) { Copy-Item $exe.FullName $ff -Force; $ok = $true; break }
        } catch {
            Write-Host "   lỗi tải từ $src : $_" -ForegroundColor Yellow
        }
    }
    Remove-Item -Force $zip -ErrorAction SilentlyContinue
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
    if (-not $ok) { throw 'Không tải được ffmpeg' }
}

# The interactive (console) user — the agent must run in their desktop session.
$user = (Get-CimInstance Win32_ComputerSystem).UserName
if (-not $user) { $user = "$env:USERDOMAIN\$env:USERNAME" }

$cfgPath = Join-Path $Dir 'config.json'
$name = $env:COMPUTERNAME
$id = ($name.ToLower() -replace '[^a-z0-9]+', '-').Trim('-')
if (Test-Path $cfgPath) {
    try { $old = Get-Content $cfgPath -Raw | ConvertFrom-Json; if ($old.name) { $name = $old.name }; if ($old.id) { $id = $old.id } } catch {}
}
@{ server = $Server; token = $Token; id = $id; name = $name } | ConvertTo-Json | Set-Content -Encoding UTF8 $cfgPath
# The config holds the token: only SYSTEM, Administrators and the user may read it.
icacls $cfgPath /inheritance:r /grant:r 'SYSTEM:F' '*S-1-5-32-544:F' "${user}:R" | Out-Null

Write-Host '==> Kiểm tra chụp màn hình + encoder (khoảng 20 giây)...'
Start-Process -FilePath (Join-Path $Dir 'remote-agent.exe') -ArgumentList '-check' -WorkingDirectory $Dir -NoNewWindow -Wait

if ($WithApp) {
    Write-Host '==> Cài app RemoteDesk (để từ máy này điều khiển máy khác)...'
    Get-Process RemoteDesk -ErrorAction SilentlyContinue | Stop-Process -Force
    $appExe = Join-Path $Dir 'RemoteDesk.exe'
    Invoke-WebRequest -UseBasicParsing -Headers @{ Authorization = "Bearer $Token" } -Uri "$Server/dl/RemoteDesk.exe" -OutFile $appExe
    $shell = New-Object -ComObject WScript.Shell
    foreach ($lnk in @(
        (Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs\RemoteDesk.lnk'),
        (Join-Path $env:PUBLIC 'Desktop\RemoteDesk.lnk'))) {
        $sc = $shell.CreateShortcut($lnk)
        $sc.TargetPath = $appExe
        $sc.WorkingDirectory = $Dir
        $sc.IconLocation = "$appExe,0"
        $sc.Description = 'RemoteDesk — điều khiển máy tính từ xa'
        $sc.Save()
    }
}

Write-Host '==> Cho phép agent nhận kết nối trực tiếp (P2P, UDP) qua tường lửa'
$agentExe = Join-Path $Dir 'remote-agent.exe'
netsh advfirewall firewall delete rule name="RemoteDesk agent" 2>&1 | Out-Null
netsh advfirewall firewall add rule name="RemoteDesk agent" dir=in action=allow program="$agentExe" protocol=UDP enable=yes profile=any | Out-Null

Write-Host "==> Đăng ký chạy tự động khi $user đăng nhập (quyền cao nhất để điều khiển cả cửa sổ Admin)"
$action = New-ScheduledTaskAction -Execute (Join-Path $Dir 'remote-agent.exe') -WorkingDirectory $Dir
$trigger = New-ScheduledTaskTrigger -AtLogOn -User $user
$principal = New-ScheduledTaskPrincipal -UserId $user -LogonType Interactive -RunLevel Highest
$settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries `
    -ExecutionTimeLimit ([TimeSpan]::Zero) -RestartCount 999 -RestartInterval (New-TimeSpan -Minutes 1) `
    -MultipleInstances IgnoreNew -StartWhenAvailable
Register-ScheduledTask -TaskName $TaskName -Action $action -Trigger $trigger -Principal $principal -Settings $settings -Force | Out-Null
Start-ScheduledTask -TaskName $TaskName

Write-Host '==> Giữ máy thức khi cắm điện (màn hình tắt / máy ngủ thì không điều khiển từ xa được)'
powercfg /change monitor-timeout-ac 0
powercfg /change standby-timeout-ac 0
powercfg /change hibernate-timeout-ac 0

# After a reboot Windows waits at the sign-in screen and the agent (which starts
# at logon) is not running yet. Offer Microsoft's Sysinternals Autologon; the
# user types the password into its own window, which stores it encrypted (LSA).
$winlogon = Get-ItemProperty 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon' -ErrorAction SilentlyContinue
if ($winlogon.AutoAdminLogon -eq '1') {
    Write-Host '==> Tự đăng nhập Windows: đã bật — agent sẽ tự chạy sau khi khởi động lại.' -ForegroundColor Green
} else {
    Write-Host ''
    Write-Host 'Tự đăng nhập Windows: CHƯA BẬT.' -ForegroundColor Yellow
    Write-Host 'Nếu không bật, sau khi máy khởi động lại, agent chỉ chạy khi có người đăng nhập Windows.'
    $ans = Read-Host 'Mở công cụ Autologon (Microsoft) để bật ngay? [c/K]'
    if ($ans -match '^[cCyY]') {
        try {
            $alZip = Join-Path $env:TEMP 'rd-autologon.zip'
            $alDir = Join-Path $Dir 'Autologon'
            Invoke-WebRequest -UseBasicParsing -Uri 'https://download.sysinternals.com/files/AutoLogon.zip' -OutFile $alZip
            Expand-Archive -Path $alZip -DestinationPath $alDir -Force
            Remove-Item -Force $alZip
            $alExe = if ([Environment]::Is64BitOperatingSystem) { 'Autologon64.exe' } else { 'Autologon.exe' }
            Write-Host "   Trong cửa sổ Autologon: kiểm tra Username, nhập Password của $user rồi bấm Enable."
            Start-Process -FilePath (Join-Path $alDir $alExe) -ArgumentList '-accepteula' -Wait
        } catch {
            Write-Host "   Không mở được Autologon: $_" -ForegroundColor Yellow
            Write-Host '   Tải thủ công: https://learn.microsoft.com/sysinternals/downloads/autologon'
        }
    }
}

Write-Host ''
Write-Host "XONG. Máy '$name' sẽ hiện trong $Server" -ForegroundColor Green
if ($WithApp) { Write-Host 'App RemoteDesk: biểu tượng trên Desktop và Start Menu.' }
Write-Host "Log: $Dir\agent.log"
Write-Host 'Lưu ý: nếu máy khởi động lại, agent chỉ chạy sau khi đăng nhập Windows — nên bật tự đăng nhập (Sysinternals Autologon).'
