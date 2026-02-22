# 🚀 WARP Config Generator Scripts

Scripts tự động tạo Cloudflare WARP/WireGuard configs cho NTRIP Relay.

---

## 📋 CÁC SCRIPT

### **1. create_warp_configs.ps1** - Tạo ít configs (1-50)
Tạo 1 hoặc nhiều WARP configs.

**Cú pháp:**
```powershell
.\create_warp_configs.ps1 [-Count <số_lượng>] [-OutputFile <tên_file>]
```

**Ví dụ:**
```powershell
# Tạo 1 config
.\create_warp_configs.ps1

# Tạo 10 configs
.\create_warp_configs.ps1 -Count 10

# Tạo 5 configs, lưu vào file khác
.\create_warp_configs.ps1 -Count 5 -OutputFile my_configs.txt
```

---

### **2. create_mass_warp.ps1** - Tạo nhiều configs (50-300+)
Tạo hàng loạt configs với batch processing để tránh rate limit.

**Cú pháp:**
```powershell
.\create_mass_warp.ps1 [-Total <số_lượng>] [-BatchSize <kích_thước_batch>] [-DelayBetweenBatches <giây>]
```

**Ví dụ:**
```powershell
# Tạo 300 configs (default)
.\create_mass_warp.ps1

# Tạo 100 configs, mỗi batch 20 configs, đợi 30s giữa các batch
.\create_mass_warp.ps1 -Total 100 -BatchSize 20 -DelayBetweenBatches 30

# Tạo 50 configs nhanh (batch 5, delay 10s)
.\create_mass_warp.ps1 -Total 50 -BatchSize 5 -DelayBetweenBatches 10
```

---

## 🔧 SETUP

### **Bước 1: Enable PowerShell scripts**
```powershell
Set-ExecutionPolicy -ExecutionPolicy RemoteSigned -Scope CurrentUser
```

### **Bước 2: Chạy script**
```powershell
cd f:\3.Laptrinh\TEST\relayrtcm\backup
.\create_warp_configs.ps1 -Count 1
```

**Script sẽ tự động:**
1. ✅ Download wgcf.exe (nếu chưa có)
2. ✅ Register WARP account
3. ✅ Generate WireGuard config
4. ✅ Parse sang format pipe-separated
5. ✅ Save vào file

---

## 📤 OUTPUT FILES

### **Sau khi chạy, bạn sẽ có:**

| File | Mô tả |
|------|-------|
| `warp_configs.txt` | Configs dạng pipe-separated (copy vào config.json) |
| `warp_config_1.conf` | WireGuard config file #1 (test bằng GUI) |
| `warp_config_2.conf` | WireGuard config file #2 |
| ... | ... |
| `wgcf.exe` | wgcf tool (giữ lại để tạo thêm configs sau) |

### **Format trong warp_configs.txt:**
```
PrivateKey|PublicKey|Endpoint|ClientIP
PrivateKey|PublicKey|Endpoint|ClientIP
...
```

---

## 🧪 TEST CONFIG TRƯỚC KHI DÙNG

**QUAN TRỌNG:** Test config bằng WireGuard GUI trước!

### **Bước 1: Install WireGuard GUI**
Download: https://www.wireguard.com/install/

### **Bước 2: Import config**
1. Mở WireGuard GUI
2. Click **"Add Tunnel"** → **"Add from file"**
3. Chọn `warp_config_1.conf`
4. Click **"Activate"**

### **Bước 3: Test connectivity**
```powershell
# Test DNS
nslookup google.com

# Test HTTP
curl http://google.com

# Test IP (xem có qua WARP không)
curl https://api.ipify.org
```

**Nếu tất cả hoạt động** ✅ → Config OK, dùng được!

**Nếu timeout** ❌ → Config lỗi, tạo lại:
```powershell
.\create_warp_configs.ps1 -Count 1 -OutputFile warp_new.txt
```

---

## 📝 SỬ DỤNG CONFIG TRONG RELAY

### **Copy config vào config.json:**

Mở `warp_configs.txt`, copy dòng config:
```
oCzLvxAM/UdY8bg8fX73Kkf8t6RHx95p4nEbMTwr0Ug=|bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=|162.159.192.1:2408|172.16.0.2
```

Dán vào `config.json`:
```json
{
  "id": "RELAY_1",
  "enable": true,
  "src_host": "your.ntrip.source",
  "src_port": 2101,
  "src_mount": "MOUNT1",
  "src_user": "user",
  "src_pass": "pass",
  "wg_config": "oCzLvxAM/UdY8bg8fX73Kkf8t6RHx95p4nEbMTwr0Ug=|bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=|162.159.192.1:2408|172.16.0.2",
  "dst_host": "your.destination",
  "dst_port": 2101,
  "dst_mount": "DEST1",
  "dst_user": "user",
  "dst_pass": "pass",
  "lat": 21.0,
  "lon": 105.0
}
```

### **Run relay:**
```powershell
.\relay_wg.exe
```

---

## ⚡ TẠO 300 CONFIGS

### **Lưu ý:**
- Tốn thời gian: ~2-3 giờ
- Cần internet ổn định
- Cloudflare có rate limit → phải delay giữa các batch

### **Chạy:**
```powershell
.\create_mass_warp.ps1 -Total 300
```

**Output:**
- `warp_all_configs.txt` - 300 configs
- `config_template.json` - Template config.json với 300 entries
- `warp_config_1.conf` → `warp_config_300.conf`

### **Sau khi tạo xong:**
1. Test config đầu tiên (`warp_config_1.conf`)
2. Nếu OK → Chỉnh `config_template.json` (thay host/user/pass thật)
3. Copy sang `config.json`
4. Start relay với 10 workers trước
5. Tăng dần lên 50, 100, 300

---

## 🚨 TROUBLESHOOTING

### **Lỗi: "wgcf.exe is not recognized"**
Script sẽ tự download. Nếu fail, download thủ công:
```powershell
Invoke-WebRequest -Uri "https://github.com/ViRb3/wgcf/releases/download/v2.2.22/wgcf_2.2.22_windows_amd64.exe" -OutFile wgcf.exe
```

### **Lỗi: "Register failed"**
Cloudflare rate limit. Đợi 5-10 phút rồi thử lại.

### **Lỗi: "Config file not generated"**
Check xem có file `wgcf-account.toml` trong thư mục temp không. Nếu có, xóa đi:
```powershell
Remove-Item warp_temp_* -Recurse -Force
```

### **Config test OK nhưng relay timeout**
Có thể do:
1. WARP free chỉ cho HTTP/HTTPS (không support NTRIP)
2. Firewall block
3. Endpoint không reachable

**Giải pháp:** Dùng SOCKS5 proxy thay vì WireGuard:
```json
{
  "wg_config": "",
  "src_proxy": "socks5://proxy.com:1080"
}
```

---

## 🔒 BẢO MẬT

### **⚠️ QUAN TRỌNG:**
- **KHÔNG** commit `warp_configs.txt` lên Git
- **KHÔNG** share private keys
- Mỗi relay nên dùng 1 config riêng

### **Backup configs:**
```powershell
# Nén configs
Compress-Archive -Path warp_config_*.conf -DestinationPath warp_backup.zip

# Encrypt (optional)
# Dùng 7zip hoặc BitLocker
```

---

## 📊 PERFORMANCE

### **Thời gian tạo configs:**

| Số lượng | Thời gian (ước tính) |
|----------|---------------------|
| 1-10 | 1-3 phút |
| 10-50 | 5-15 phút |
| 50-100 | 20-40 phút |
| 100-300 | 1-3 giờ |

### **Tối ưu:**
- Tăng `BatchSize` nếu không bị rate limit
- Giảm `DelayBetweenBatches` (rủi ro: bị block)
- Chạy ban đêm (ít traffic → ít bị rate limit)

---

## 💡 TIPS

1. **Test 1 config trước** - Đừng tạo 300 configs rồi mới phát hiện không hoạt động
2. **Start nhỏ** - Chạy 10 workers trước, sau đó scale up
3. **Monitor RAM** - 300 tunnels ~ 1.5-2GB RAM
4. **Rotate keys** - Tạo configs mới mỗi 30-60 ngày
5. **Backup configs** - Lưu `warp_configs.txt` an toàn

---

## 📞 HỖ TRỢ

Nếu gặp vấn đề:
1. Kiểm tra log output của script
2. Test config bằng WireGuard GUI thủ công
3. Check internet connection
4. Thử tạo 1 config đơn lẻ trước

---

**Version:** 1.0  
**Date:** 2026-02-15  
**Author:** AI Assistant
