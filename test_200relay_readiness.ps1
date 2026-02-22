# ============================================
# Test System Readiness for 200 NTRIP Relay
# ============================================
# Script này kiểm tra xem hệ thống có đủ khả năng chạy 200 relay không

Write-Host "===== NTRIP RELAY - SYSTEM READINESS CHECK =====" -ForegroundColor Cyan
Write-Host ""

$allGood = $true

# 1. Kiểm tra OS Version
Write-Host "[1/6] Checking Windows Version..." -ForegroundColor Yellow
$osInfo = Get-WmiObject -Class Win32_OperatingSystem
Write-Host "      OS: $($osInfo.Caption)" -ForegroundColor Gray
Write-Host "      Version: $($osInfo.Version)" -ForegroundColor Gray

# 2. Kiểm tra RAM
Write-Host "[2/6] Checking Available Memory..." -ForegroundColor Yellow
$ram = Get-WmiObject Win32_OperatingSystem
$freeGB = [math]::Round($ram.FreePhysicalMemory / 1MB, 2)
$totalGB = [math]::Round($ram.TotalVisibleMemorySize / 1MB, 2)
Write-Host "      Free: $freeGB GB / Total: $totalGB GB" -ForegroundColor Gray

if ($freeGB -lt 0.5) {
    Write-Host "      ⚠️  WARNING: Low memory! Recommended: > 1GB free" -ForegroundColor Red
    $allGood = $false
} else {
    Write-Host "      ✅ Memory OK" -ForegroundColor Green
}

# 3. Kiểm tra Network Limits
Write-Host "[3/6] Checking Network Configuration..." -ForegroundColor Yellow
$currentConnections = (netstat -ano | Select-String "ESTABLISHED").Count
Write-Host "      Current TCP connections: $currentConnections" -ForegroundColor Gray

if ($currentConnections -gt 50000) {
    Write-Host "      ⚠️  WARNING: Too many existing connections!" -ForegroundColor Red
    $allGood = $false
} else {
    Write-Host "      ✅ Network capacity OK" -ForegroundColor Green
}

# 4. Kiểm tra Port availability
Write-Host "[4/6] Checking if monitor port :8081 is available..." -ForegroundColor Yellow
$portInUse = (netstat -ano | Select-String ":8081.*LISTENING").Count -gt 0
if ($portInUse) {
    Write-Host "      ⚠️  Port 8081 is already in use" -ForegroundColor Red
    $allGood = $false
} else {
    Write-Host "      ✅ Port 8081 available" -ForegroundColor Green
}

# 5. Kiểm tra file config
Write-Host "[5/6] Checking config.json..." -ForegroundColor Yellow
if (Test-Path "config.json") {
    try {
        $config = Get-Content "config.json" -Raw | ConvertFrom-Json
        $enabledCount = ($config | Where-Object { $_.enable -eq $true }).Count
        Write-Host "      Found: $($config.Count) stations (Enabled: $enabledCount)" -ForegroundColor Gray
        
        if ($enabledCount -eq 0) {
            Write-Host "      ⚠️  WARNING: No stations enabled!" -ForegroundColor Red
        } elseif ($enabledCount -gt 250) {
            Write-Host "      ⚠️  WARNING: > 250 stations may cause issues. Recommended: < 200" -ForegroundColor Red
            $allGood = $false
        } else {
            Write-Host "      ✅ Config OK" -ForegroundColor Green
        }
    } catch {
        Write-Host "      ❌ Error parsing config.json: $_" -ForegroundColor Red
        $allGood = $false
    }
} else {
    Write-Host "      ❌ config.json not found!" -ForegroundColor Red
    $allGood = $false
}

# 6. Kiểm tra Go runtime hoặc compiled binary
Write-Host "[6/6] Checking executable..." -ForegroundColor Yellow
if (Test-Path "relay.exe") {
    $size = [math]::Round((Get-Item "relay.exe").Length / 1MB, 2)
    Write-Host "      Found: relay.exe ($size MB)" -ForegroundColor Gray
    Write-Host "      ✅ Executable ready" -ForegroundColor Green
} elseif (Test-Path "backup\main.go") {
    Write-Host "      Found: backup\main.go (source code)" -ForegroundColor Gray
    # Kiểm tra Go
    try {
        $goVersion = go version 2>&1
        if ($LASTEXITCODE -eq 0) {
            Write-Host "      Go: $goVersion" -ForegroundColor Gray
            Write-Host "      ✅ Can build from source" -ForegroundColor Green
        } else {
            throw "Go not found"
        }
    } catch {
        Write-Host "      ⚠️  Go compiler not found. Need to build with Go or use pre-compiled relay.exe" -ForegroundColor Red
    }
} else {
    Write-Host "      ❌ Neither relay.exe nor main.go found!" -ForegroundColor Red
    $allGood = $false
}

# Tổng kết
Write-Host ""
Write-Host "============================================" -ForegroundColor Cyan
if ($allGood) {
    Write-Host "✅ SYSTEM READY FOR 200 RELAY!" -ForegroundColor Green
    Write-Host ""
    Write-Host "Next steps:" -ForegroundColor Cyan
    Write-Host "  1. Edit config.json (add your stations)" -ForegroundColor White
    Write-Host "  2. Run: .\relay.exe" -ForegroundColor White
    Write-Host "  3. Access monitor: http://localhost:8081 (admin/admin)" -ForegroundColor White
    Write-Host "  4. Watch logs for staggered startup (0-60s)" -ForegroundColor White
} else {
    Write-Host "⚠️  SYSTEM NOT READY - Please fix issues above" -ForegroundColor Red
}
Write-Host "============================================" -ForegroundColor Cyan
Write-Host ""

# Hiển thị system limits (Optional)
Write-Host "📊 System Limits Reference:" -ForegroundColor Cyan
Write-Host "   Max concurrent connections: 50 (controlled by semaphore)" -ForegroundColor Gray
Write-Host "   Startup time: 0-60 seconds (staggered)" -ForegroundColor Gray
Write-Host "   Expected RAM usage: ~10-20 MB" -ForegroundColor Gray
Write-Host "   Expected CPU usage: 1-5%" -ForegroundColor Gray
Write-Host ""
