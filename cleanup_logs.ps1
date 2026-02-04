# Script tự động xóa nội dung file log mỗi 1 giờ
# Tương tự cronjob trên Linux

$LogDir = "F:\3.Laptrinh\TEST\relayrtcm\logs"

Write-Host "[$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')] Starting log cleanup..."

# Lấy danh sách file log
$LogFiles = Get-ChildItem -Path $LogDir -Filter "*.log" -File

foreach ($file in $LogFiles) {
    $sizeMB = [math]::Round($file.Length / 1MB, 2)
    
    if ($file.Length -gt 0) {
        Write-Host "Cleaning $($file.Name) (Size: $sizeMB MB)"
        
        # Xóa nội dung file nhưng giữ lại file
        Clear-Content -Path $file.FullName -Force
        
        Write-Host "  -> Cleared successfully"
    } else {
        Write-Host "$($file.Name) is already empty"
    }
}

Write-Host "[$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss')] Cleanup completed.`n"
