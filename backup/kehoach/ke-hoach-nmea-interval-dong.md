# Kế hoạch: Cấu hình động tần suất gửi GGA/NMEA

- **Ngày lập:** 2026-10-20
- **File áp dụng:** `F:\3.Laptrinh\1. Project\1. GNSS\relayrtcm\backup\main.go`
- **Mục tiêu:** Cho phép cấu hình tần suất gửi GGA/NMEA theo từng kết nối trong `config.json` và trên UI.

## Quyết định đã chốt

1. Cách áp dụng: **restart worker** khi thay đổi cấu hình (dùng cơ chế hiện có).
2. Jitter: **tự động**, không mở field riêng ra UI.
3. Đơn vị: **giây**.

## Hiện trạng

- Tần suất GGA đang lấy từ `DeviceProfile.NMEAInterval` + `NMEAJitter` (`main.go:92-105`).
- Gửi 1 câu GGA ngay khi kết nối (`main.go:594`).
- Sau đó gửi trong goroutine heartbeat (`main.go:626-671`):
  - `nextInterval = device.NMEAInterval + random(-jitter, +jitter)`.
  - Random lại sau mỗi lần gửi (`main.go:663-668`).
- Hằng `SendNMEAInterval` (`main.go:39`) khai báo nhưng không được dùng.
- `configHash` là MD5 của toàn bộ `ConfigStation` (`main.go:1298`), nên thay đổi config sẽ restart worker.

## Các bước triển khai

### 1. Config model — `main.go:108-127`

Thêm field:

```go
NMEAIntervalSec int `json:"nmea_interval_sec,omitempty"` // 0/không có = auto theo device profile
```

Dùng `omitempty` để config cũ (chưa có field) giữ nguyên MD5 hash, tránh restart toàn bộ worker khi nâng cấp binary.

### 2. Worker helper + heartbeat — `main.go:626-671`

Thêm 2 helper:

```go
func (w *Worker) nmeaInterval() time.Duration // cfg > 0 ? cfg giây : device.NMEAInterval
func (w *Worker) nmeaJitter() time.Duration   // min(device.NMEAJitter, 20% nmeaInterval)
```

- Thay `w.device.NMEAInterval` / `w.device.NMEAJitter` tại `main.go:628-631` và `main.go:663-667` bằng helper.
- Clamp: `< 1s` → auto, `> 3600s` → 3600s.
- Jitter ví dụ: interval 5s → ±1s; 10s → ±2s; 30s → ±4s.
- GGA mở hàng lúc kết nối (`main.go:594`) giữ nguyên, luôn gửi ngay 1 lần.
- Log khởi tạo worker (`main.go:372`) thêm interval thực tế.

### 3. API validate — `main.go:1395-1540`

Chuẩn hoá `nmea_interval_sec` ở cả `POST /api/configs` và `PUT /api/configs/{id}`:

- Giá trị âm → 0.
- Giá trị lớn hơn 3600 → 3600.

### 4. UI (HTML nhúng trong main.go)

- Modal Add/Edit (`main.go:1778-1858`): thêm ô số **"GGA/NMEA Interval (seconds, 0 = auto)"**, `min=0 max=3600`.
- `editStation` (`main.go:2145-2173`): nạp `s.nmea_interval_sec || 0`.
- Submit form (`main.go:2324-2343`): gửi `nmea_interval_sec: parseInt(...) || 0`.
- `validateJSON` (`main.go:2240-2252`): default `nmea_interval_sec = 0`.
- Ví dụ JSON import (`main.go:1716-1742`): thêm field.
- Bảng Manage (`main.go:2076-2098`): thêm cột **GGA** hiển thị `10s` hoặc `auto`.

### 5. Hành vi sau khi triển khai

- Đổi interval trong UI → lưu config → `reloadConfig` restart worker đó (reconnect khoảng 1-3 giây).
- Config cũ/không có field → tự động dùng profile như hiện nay.
- Không thêm field jitter ra UI.

### 6. Kiểm tra

- Build: `go build -o relay_new.exe .` trong thư mục `backup`.
- Test: thêm/sửa station với 5s, 10s, 0 (auto); kiểm tra log và tốc độ GGA thực tế.
- Xác nhận config cũ không bị restart hàng loạt sau khi thay binary.

## Ghi chú

- GGA có thể được gửi kèm GSA (~1/3 lần) và RMC (~1/4 lần) tùy device profile (`main.go:1218-1223`).
- `SendNMEAInterval` hiện không dùng; có thể xóa hoặc giữ làm tài liệu tham khảo.
