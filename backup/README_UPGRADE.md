# 🚀 NTRIP Relay - Phiên bản nâng cấp toàn diện

## ✨ Tính năng mới

### 1. **Web Admin Panel** - Quản lý qua giao diện web
- ✅ Thêm/Sửa/Xóa station trực tiếp qua web (không cần chỉnh file JSON)
- ✅ 2 Tab: **Monitor** (theo dõi realtime) và **Manage Stations** (quản lý cấu hình)
- ✅ Bảo mật bằng Basic Authentication (user: `admin` / pass: `admin`)

### 2. **SOCKS5 Proxy Support**
- ✅ Hỗ trợ kết nối qua SOCKS5 proxy cho cả Source và Destination
- ✅ Format: `socks5://host:port` hoặc `socks5://user:pass@host:port`
- ✅ Hữu ích khi cần vượt firewall hoặc ẩn IP

### 3. **SSL/TLS Support**
- ✅ Hỗ trợ kết nối bảo mật (HTTPS-like) cho NTRIP
- ✅ Cờ `src_use_ssl` và `dst_use_ssl` để bật/tắt
- ✅ Tự động TLS handshake, tương thích với các Caster hiện đại (Trimble, Leica SmartNet)

### 4. **Cross-Platform**
- ✅ Chạy được trên **Windows, Linux, macOS**
- ✅ Build đơn giản với Go

---

## 📦 Cài đặt

### Windows
```powershell
# Build
go mod tidy
go build -o relay.exe main.go

# Chạy
.\relay.exe
```

### Linux / macOS
```bash
# Build
go mod tidy
go build -o relayrtcm main.go

# Chạy
./relayrtcm

# Hoặc chạy nền
nohup ./relayrtcm > relay.log 2>&1 &
```

---

## 🌐 Sử dụng Web Admin

1. **Khởi động chương trình:**
   ```bash
   ./relayrtcm
   ```

2. **Mở trình duyệt, truy cập:**
   ```
   http://localhost:8081
   ```

3. **Đăng nhập:**
   - Username: `admin`
   - Password: `admin`

4. **Quản lý stations:**
   - **Tab Monitor**: Xem trạng thái realtime của tất cả station
   - **Tab Manage Stations**: Thêm/Sửa/Xóa cấu hình
   - Click nút **"+ Add Station"** để thêm trạm mới

---

## 📝 Cấu trúc Config mới

File `config.json` hoặc `config_example.json`:

```json
[
  {
    "id": "VRS1",
    "enable": true,
    "src_host": "14.248.80.81",
    "src_port": 2101,
    "src_mount": "SAIGON1_RINEX",
    "src_user": "freedom2",
    "src_pass": "2345@",
    "src_proxy": "",                    // ← MỚI: SOCKS5 proxy cho source
    "src_use_ssl": false,               // ← MỚI: Bật SSL/TLS cho source
    "dst_host": "171.244.50.117",
    "dst_port": 2101,
    "dst_mount": "TPHCM",
    "dst_user": "user01",
    "dst_pass": "123456",
    "dst_proxy": "",                    // ← MỚI: SOCKS5 proxy cho destination
    "dst_use_ssl": false,               // ← MỚI: Bật SSL/TLS cho destination
    "lat": 10.762622,
    "lon": 106.660172
  }
]
```

### Ví dụ sử dụng Proxy + SSL:
```json
{
  "id": "SecureStation",
  "enable": true,
  "src_host": "secure.ntrip.com",
  "src_port": 2102,
  "src_mount": "MOUNT1",
  "src_user": "user",
  "src_pass": "pass",
  "src_proxy": "socks5://127.0.0.1:1080",  // Kết nối source qua proxy
  "src_use_ssl": true,                      // Dùng SSL cho source
  "dst_host": "destination.com",
  "dst_port": 443,                           // Port HTTPS
  "dst_mount": "DEST",
  "dst_user": "dest",
  "dst_pass": "pass",
  "dst_proxy": "",                           // Không dùng proxy cho dest
  "dst_use_ssl": true,                       // Dùng SSL cho dest
  "lat": 21.0,
  "lon": 105.0
}
```

---

## 🔐 Bảo mật

### Web Admin Authentication
- Đổi mật khẩu mặc định trong code tại [main.go](main.go#L666):
  ```go
  const (
      WebUser = "admin"
      WebPass = "your_strong_password_here"
  )
  ```

### SSL/TLS Certificate Validation
- Mặc định: Xác thực certificate (production)
- Để bỏ qua (self-signed cert), sửa trong `connectToHost`:
  ```go
  InsecureSkipVerify: true  // Chỉ dùng khi test
  ```

---

## 🚀 Chạy dưới dạng Service

### Linux (systemd)
Tạo file `/etc/systemd/system/relayrtcm.service`:
```ini
[Unit]
Description=NTRIP Relay Service
After=network.target

[Service]
Type=simple
User=your_user
WorkingDirectory=/path/to/relayrtcm
ExecStart=/path/to/relayrtcm/relayrtcm
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

Kích hoạt:
```bash
sudo systemctl daemon-reload
sudo systemctl enable relayrtcm
sudo systemctl start relayrtcm
sudo systemctl status relayrtcm
```

### Windows (Task Scheduler)
Dùng script có sẵn hoặc tạo Task Scheduler:
- Program: `F:\3.Laptrinh\TEST\relayrtcm\relayrtcm.exe`
- Start in: `F:\3.Laptrinh\TEST\relayrtcm`
- Trigger: At system startup
- Run with highest privileges

---

## 📊 API Endpoints

### Status Monitor
- **GET** `/status` - Lấy trạng thái tất cả stations (JSON)

### Config Management
- **GET** `/api/configs` - Lấy toàn bộ config
- **POST** `/api/configs` - Thêm station mới
- **GET** `/api/configs/:id` - Lấy thông tin 1 station
- **PUT** `/api/configs/:id` - Cập nhật station
- **DELETE** `/api/configs/:id` - Xóa station

**Lưu ý:** Tất cả API đều yêu cầu Basic Auth (`admin:admin`)

---

## 🔧 Troubleshooting

### Port 8081 đã được sử dụng
Đổi port trong [main.go](main.go#L19):
```go
const MonitorPort = ":8082"  // Đổi thành port khác
```

### Không kết nối được qua SSL
- Kiểm tra Caster có hỗ trợ SSL không (thường port 2102 hoặc 443)
- Thử bật `InsecureSkipVerify: true` nếu dùng self-signed cert

### SOCKS5 Proxy không hoạt động
- Đảm bảo proxy đang chạy và cho phép kết nối
- Format phải đúng: `socks5://host:port`
- Nếu có auth: `socks5://user:pass@host:port`

---

## 📄 So sánh phiên bản

| Tính năng | Phiên bản cũ | Phiên bản mới |
|-----------|-------------|---------------|
| Web Admin | ❌ | ✅ CRUD qua web |
| SOCKS5 Proxy | ❌ | ✅ Source + Dest |
| SSL/TLS | ❌ | ✅ Đầy đủ |
| Basic Auth | ❌ | ✅ admin/admin |
| Cross-platform | ✅ | ✅ |
| Monitor realtime | ✅ | ✅ Cải tiến |

---

## 📞 Hỗ trợ

- File config mẫu: `config_example.json`
- Log xuất ra console: `./relayrtcm` hoặc `./relayrtcm > relay.log`
- Check lỗi: Xem tab Monitor hoặc log file

---

**Phát triển bởi:** NTRIP Relay Team  
**Version:** 2.0.0  
**License:** MIT
