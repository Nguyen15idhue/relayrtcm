# Script kiểm tra trạng thái workers sau khi khởi động
Write-Host "=== WORKER STATUS CHECK ===" -ForegroundColor Cyan
Write-Host "Checking workers at: http://localhost:8081/status" -ForegroundColor Gray
Write-Host ""

try {
    # Tạo credentials
    $user = "admin"
    $pass = "admin"
    $pair = "$($user):$($pass)"
    $encodedCreds = [System.Convert]::ToBase64String([System.Text.Encoding]::ASCII.GetBytes($pair))
    $headers = @{
        Authorization = "Basic $encodedCreds"
    }
    
    # Gọi API
    $response = Invoke-RestMethod -Uri "http://localhost:8081/status" -Headers $headers -Method Get
    
    # Phân tích status
    $total = $response.Count
    $waiting = ($response | Where-Object { $_.status -like "Waiting*" }).Count
    $running = ($response | Where-Object { $_.status -eq "Running" }).Count
    $error = ($response | Where-Object { $_.status -eq "Error" }).Count
    $connecting = ($response | Where-Object { $_.status -like "Connecting*" }).Count
    
    Write-Host "📊 Summary:" -ForegroundColor Cyan
    Write-Host "   Total workers: $total" -ForegroundColor White
    Write-Host "   ⏳ Waiting (staggered startup): $waiting" -ForegroundColor Yellow
    Write-Host "   🔄 Connecting: $connecting" -ForegroundColor Cyan
    Write-Host "   ✅ Running: $running" -ForegroundColor Green
    Write-Host "   ❌ Error: $error" -ForegroundColor Red
    Write-Host ""
    
    if ($waiting -gt 0) {
        Write-Host "⚠️  $waiting workers đang chờ khởi động (Staggered Startup)" -ForegroundColor Yellow
        Write-Host "   ➜ Đây là BÌNH THƯỜNG! Đợi thêm 30-60s nữa." -ForegroundColor Gray
    }
    
    if ($running -gt ($total * 0.8)) {
        Write-Host "✅ HỆ THỐNG HOẠT ĐỘNG TỐT!" -ForegroundColor Green
        Write-Host "   $running/$total workers đã Running ($(($running/$total*100).ToString('0.0'))%)" -ForegroundColor Green
    } elseif ($waiting + $connecting -gt ($total * 0.3)) {
        Write-Host "⏳ HỆ THỐNG ĐANG KHỞI ĐỘNG..." -ForegroundColor Yellow
        Write-Host "   Staggered startup đang hoạt động. Đợi thêm..." -ForegroundColor Yellow
    } else {
        Write-Host "⚠️  CÓ VẤN ĐỀ!" -ForegroundColor Red
        Write-Host "   Quá nhiều workers lỗi. Kiểm tra log để biết chi tiết." -ForegroundColor Red
    }
    
    # Hiển thị một số workers đang waiting (nếu có)
    if ($waiting -gt 0) {
        Write-Host ""
        Write-Host "⏳ Workers đang chờ (top 5):" -ForegroundColor Yellow
        $response | Where-Object { $_.status -like "Waiting*" } | Select-Object -First 5 | ForEach-Object {
            Write-Host "   - $($_.id): $($_.status)" -ForegroundColor Gray
        }
    }
    
} catch {
    Write-Host "❌ Không thể kết nối tới Web Monitor!" -ForegroundColor Red
    Write-Host "   Lỗi: $($_.Exception.Message)" -ForegroundColor Red
    Write-Host ""
    Write-Host "Kiểm tra:" -ForegroundColor Yellow
    Write-Host "   1. relay.exe có đang chạy không?" -ForegroundColor Gray
    Write-Host "   2. Port 8081 có bị block không?" -ForegroundColor Gray
    Write-Host "   3. Username/Password đúng chưa? (admin/admin)" -ForegroundColor Gray
}

Write-Host ""
Write-Host "💡 Tips:" -ForegroundColor Cyan
Write-Host "   - Chạy lại sau 30s để xem tiến độ" -ForegroundColor Gray
Write-Host "   - Xem chi tiết: http://localhost:8081 (admin/admin)" -ForegroundColor Gray
Write-Host "   - Đọc thêm: CHANGELOG_200RELAY_OPTIMIZATION.md" -ForegroundColor Gray
