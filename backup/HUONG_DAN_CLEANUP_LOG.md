# Hướng dẫn tự động xóa log (Windows)

## Phương án 1: Dùng Task Scheduler (Khuyên dùng - Giống Cronjob Linux)

### Bước 1: Cài đặt Task (Chạy 1 lần)
```powershell
# Mở PowerShell với quyền Administrator
.\setup_task_scheduler.ps1
```

### Bước 2: Kiểm tra
```powershell
# Xem task đã tạo
Get-ScheduledTask -TaskName "NTRIP_Relay_Log_Cleanup"

# Chạy thử ngay
Start-ScheduledTask -TaskName "NTRIP_Relay_Log_Cleanup"
```

### Xóa task (nếu muốn tắt tự động cleanup)
```powershell
Unregister-ScheduledTask -TaskName "NTRIP_Relay_Log_Cleanup" -Confirm:$false
```

---

## Phương án 2: Chạy thủ công khi cần

```powershell
.\cleanup_logs.ps1
```

---

## Phương án 3: Tích hợp vào code Go (Tự động nhất)

Nếu muốn tự động cleanup ngay trong chương trình Go (không cần setup gì thêm), có thể thêm hàm vào `main.go`:

```go
// Thêm vào hàm main() sau khi khởi động Monitor:
go autoCleanupLogs()

// Thêm hàm này vào cuối file:
func autoCleanupLogs() {
    ticker := time.NewTicker(1 * time.Hour)
    defer ticker.Stop()
    
    for range ticker.C {
        cleanupLogs()
    }
}

func cleanupLogs() {
    logDir := "logs"
    files, err := os.ReadDir(logDir)
    if err != nil {
        return
    }
    
    for _, file := range files {
        if !file.IsDir() && strings.HasSuffix(file.Name(), ".log") {
            path := filepath.Join(logDir, file.Name())
            if info, err := os.Stat(path); err == nil {
                sizeMB := float64(info.Size()) / 1024 / 1024
                if sizeMB > 0.1 { // Nếu > 100KB
                    os.Truncate(path, 0) // Xóa nội dung
                    log.Printf("[System] Cleaned log: %s (%.2f MB)", file.Name(), sizeMB)
                }
            }
        }
    }
}
```

---

## So sánh 3 phương án

| Phương án | Ưu điểm | Nhược điểm |
|-----------|---------|------------|
| **Task Scheduler** | - Chạy độc lập với app<br>- Dễ quản lý/tắt | - Cần setup 1 lần |
| **Script thủ công** | - Đơn giản, linh hoạt | - Phải nhớ chạy |
| **Tích hợp Go** | - Tự động 100%<br>- Cross-platform | - Phải sửa code |

**Khuyến nghị**: Dùng Task Scheduler (giống cronjob trên Linux)
