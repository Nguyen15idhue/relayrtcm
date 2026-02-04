# Script tự động cài đặt Task Scheduler cho cleanup log
# Chạy 1 lần để setup, sau đó sẽ tự động chạy mỗi giờ

$TaskName = "NTRIP_Relay_Log_Cleanup"
$ScriptPath = "F:\3.Laptrinh\TEST\relayrtcm\cleanup_logs.ps1"

# Xóa task cũ nếu có
Unregister-ScheduledTask -TaskName $TaskName -Confirm:$false -ErrorAction SilentlyContinue

# Tạo action - chạy PowerShell script
$Action = New-ScheduledTaskAction -Execute "powershell.exe" -Argument "-NoProfile -ExecutionPolicy Bypass -File `"$ScriptPath`""

# Tạo trigger - chạy mỗi 1 giờ, bắt đầu ngay khi tạo
$Trigger = New-ScheduledTaskTrigger -Once -At (Get-Date) -RepetitionInterval (New-TimeSpan -Hours 1)

# Tạo settings
$Settings = New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable

# Đăng ký task (chạy dưới quyền user hiện tại)
Register-ScheduledTask -TaskName $TaskName -Action $Action -Trigger $Trigger -Settings $Settings -Description "Tự động xóa log NTRIP Relay mỗi 1 giờ"

Write-Host "`n✅ Task Scheduler đã được cài đặt thành công!" -ForegroundColor Green
Write-Host "`nThông tin:" -ForegroundColor Cyan
Write-Host "  - Task Name: $TaskName"
Write-Host "  - Tần suất: Mỗi 1 giờ"
Write-Host "  - Script: $ScriptPath"
Write-Host "`nKiểm tra task:"
Write-Host "  Get-ScheduledTask -TaskName '$TaskName' | Format-List" -ForegroundColor Yellow
Write-Host "`nChạy thử ngay:"
Write-Host "  Start-ScheduledTask -TaskName '$TaskName'" -ForegroundColor Yellow
Write-Host "`nXóa task (nếu cần):"
Write-Host "  Unregister-ScheduledTask -TaskName '$TaskName' -Confirm:`$false" -ForegroundColor Yellow
