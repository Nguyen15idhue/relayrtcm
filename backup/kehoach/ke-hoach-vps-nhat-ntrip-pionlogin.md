# Kế hoạch triển khai VPS Nhật cho NTRIP Relay và PionLogin

## 0. Thông tin tài liệu

| Mục | Nội dung |
|---|---|
| Tên kế hoạch | 1 VPS Nhật cho NTRIP Relay + proxy PionLogin |
| Phiên bản | 1.1 |
| Ngày lập | 2026-10-24 |
| Phạm vi | Hạ tầng VPS, WireGuard, SOCKS5/HTTP proxy, cấu hình relay, cấu hình PionLogin |
| Đối tượng | Quản trị Windows VPS chạy relay NTRIP và người dùng PionLogin |
| Trạng thái | Sẵn sàng triển khai |

## 1. Mục tiêu

1. 15 trạm NTRIP Nhật Bản có IP Nhật cho cả hai chân:
   - Source: `ntrip1.bizstation.jp:2101`
   - Destination: `servers.onocoy.com:2101`
2. 15 profile PionLogin (15 tài khoản onocoy) mở được website onocoy bằng IP Nhật.
3. Các trạm Việt Nam tiếp tục đi trực tiếp, không bị ảnh hưởng.
4. Chi phí thấp, dễ vận hành, có rollback.
5. Không sửa code relay; chỉ thay đổi cấu hình theo trạm.

## 2. Quyết định đã chốt

| Hạng mục | Quyết định |
|---|---|
| Nhà cung cấp | Vultr |
| Gói | Cloud Compute - Shared CPU - `vc2-1c-1gb` |
| Vị trí | Tokyo, JP (APAC) |
| Hệ điều hành | Debian 12 x64 (bookworm) |
| IPv4 | Public IPv4 |
| Backup | Automatic Backups, +$1/tháng |
| Giá | $5.00 + $1.00 = **$6.00/tháng** |
| Proxy relay | WireGuard + Dante nội bộ `10.66.0.1:1080/1081` |
| Proxy PionLogin | Gost public `207.148.103.189:1082` (SOCKS5) và `207.148.103.189:8080` (HTTP), có user/pass |
| Số trạm NTRIP | 15 trạm JP |
| Số profile PionLogin | 15 profile, 15 tài khoản onocoy |
| IP dùng chung | Có; tất cả profile và trạm dùng chung 1 IPv4 |
| Trạm Việt Nam | Giữ nguyên, không qua Nhật |

### 2.1. IP thực tế

| Thành phần | IP |
|---|---|
| VPS Nhật (Vultr Tokyo) | **207.148.103.189** |
| Windows VPS Việt Nam (relay) | **103.166.183.250** |

Đã xác minh:
- `207.148.103.189` thuộc Vultr Tokyo, Nhật Bản (AS20473).
- `103.166.183.250` thuộc Hà Nội, Việt Nam (AS135905).

## 3. Kiến trúc

```text
Windows VPS Việt Nam (relay NTRIP)
│
├─ Trạm VN: src_proxy=""  dst_proxy=""  ──────────────► Internet (IP VN)
│
└─ 15 trạm JP:
     source (bizstation) ──► 10.66.0.1:1080 ─┐
     dest   (onocoy)     ──► 10.66.0.1:1081 ─┤
                                             │ WireGuard 10.66.0.0/24
                                             ▼
                                  Vultr Tokyo 10.66.0.1
                                  ├─ Dante :1080  (source, no auth)
                                  ├─ Dante :1081  (dest, no auth)
                                  ├─ Gost  :1082  (SOCKS5, auth)
                                  └─ Gost  :8080  (HTTP, auth)
                                             │
                                             ▼
                                  bizstation / onocoy (IP Nhật)

PC cá nhân (PionLogin)
└─ 15 profile ──► 207.148.103.189:1082 (SOCKS5) hoặc 207.148.103.189:8080 (HTTP)
                  └─ Toàn bộ phiên trình duyệt đi qua IP Nhật
```

### 3.1. Nguyên tắc

- WireGuard chỉ dùng cho relay NTRIP; không full tunnel.
- Windows relay chỉ route `10.66.0.1/32` qua tunnel.
- Proxy NTRIP `1080/1081` chỉ nghe trong tunnel, không lộ Internet.
- Proxy PionLogin `1082/8080` là proxy công khai có xác thực.
- Các trạm VN không đi qua VPS Nhật.

## 4. Điều kiện tiên quyết

- [ ] Tài khoản Vultr và phương thức thanh toán.
- [ ] SSH key trên PC:
  ```powershell
  ssh-keygen -t ed25519 -C "relay-jp"
  ```
- [ ] IP công cộng Windows VPS: `103.166.183.250`.
- [ ] Danh sách 15 trạm JP: `id`, `src_host`, `src_mount`, `dst_host`, `dst_mount`.
- [ ] 15 tài khoản onocoy đăng nhập được.
- [ ] PionLogin đã cài trên PC.
- [ ] Quyền truy cập NSSM trên Windows VPS.
- [ ] Backup `config.json` hiện tại.
- [ ] Cửa sổ bảo trì 2-3 giờ và 48 giờ theo dõi.

## 5. Giai đoạn 1 - Tạo VPS Vultr

### 5.1. Tạo instance

1. Vultr Console → **Deploy New Server**.
2. Chọn **Cloud Compute - Shared CPU**.
3. Location: **Tokyo, JP**.
4. Plan: **vc2-1c-1gb**.
5. Image: **Debian 12 x64 (bookworm)**.
6. IPv4: **Public IPv4**.
7. Bật **Automatic Backups** (+$1/tháng).
8. Thêm SSH public key.
9. Deploy và ghi lại:
   - `207.148.103.189`
   - mật khẩu root ban đầu (nếu có)

### 5.2. Firewall

Khuyến nghị dùng UFW trên VPS. Có thể dùng thêm Vultr Firewall Group.

Nếu dùng Vultr Firewall Group:

| Protocol | Port | Source |
|---|---|---|
| TCP | 22 | IP quản trị của bạn |
| UDP | 51820 | `103.166.183.250` |
| TCP | 1082 | Anywhere |
| TCP | 8080 | Anywhere |

Lưu ý:
- Không mở `1080/1081` ra Internet.
- Nếu IP quản trị thay đổi, cập nhật lại rule SSH.

## 6. Giai đoạn 2 - Cài đặt và hardening

### 6.1. Cập nhật hệ thống

```bash
apt update && apt upgrade -y
apt install -y curl ufw fail2ban unattended-upgrades wireguard dante-server
timedatectl set-timezone Asia/Tokyo
hostnamectl set-hostname jp-relay
```

### 6.2. Tạo user quản trị

```bash
adduser admin
usermod -aG sudo admin
rsync --archive --chown=admin:admin ~/.ssh /home/admin
```

Test đăng nhập bằng `admin` trước khi siết SSH.

### 6.3. Siết SSH

Tạo `/etc/ssh/sshd_config.d/99-hardening.conf`:

```ini
PermitRootLogin no
PasswordAuthentication no
PubkeyAuthentication yes
```

```bash
systemctl restart ssh
systemctl enable --now fail2ban
dpkg-reconfigure -plow unattended-upgrades
```

### 6.4. UFW

```bash
ufw default deny incoming
ufw default allow outgoing
ufw allow 22/tcp
ufw allow from 103.166.183.250 to any port 51820 proto udp
ufw allow 1082/tcp
ufw allow 8080/tcp
ufw enable
ufw status numbered
```

### 6.5. Kiểm tra IPv6

Vultr có thể cấp IPv6 ngoài ý muốn. Kiểm tra:

```bash
ip -6 addr
```

Nếu không cần IPv6:
- Không cấu hình IPv6 cho Dante.
- Hoặc tắt IPv6 trên VPS để ép egress IPv4.

## 7. Giai đoạn 3 - WireGuard

### 7.1. Tạo key

```bash
umask 077
cd /etc/wireguard
wg genkey | tee server.key | wg pubkey > server.pub
wg genkey | tee relay.key | wg pubkey > relay.pub
cat server.pub
cat relay.pub
```

Lưu `relay.key` để đưa sang Windows. Không gửi qua kênh không an toàn.

### 7.2. Cấu hình server

`/etc/wireguard/wg0.conf`:

```ini
[Interface]
Address = 10.66.0.1/24
ListenPort = 51820
PrivateKey = <SERVER_PRIVATE_KEY>

[Peer]
PublicKey = <RELAY_PUBLIC_KEY>
AllowedIPs = 10.66.0.2/32
```

```bash
systemctl enable --now wg-quick@wg0
wg show
```

Không cần `ip_forward` hoặc NAT vì Dante kết thúc TCP tại VPS.

## 8. Giai đoạn 4 - Dante nội bộ cho relay

### 8.1. Xác định interface

```bash
ip -br a
```

Ghi lại tên interface thật, ví dụ `eth0` hoặc `ens3`.

### 8.2. Cấu hình

`/etc/danted.conf`:

```ini
logoutput: /var/log/danted.log
errorlog: /var/log/danted.errlog

internal: 10.66.0.1 port = 1080
internal: 10.66.0.1 port = 1081
external: <ETH0>

socksmethod: none
clientmethod: none
user.privileged: root
user.unprivileged: nobody

timeout.negotiate: 30
timeout.io: 0

client pass {
    from: 10.66.0.0/24 to: 0.0.0.0/0
    log: error
}

socks pass {
    from: 10.66.0.0/24 to: 0.0.0.0/0
    command: connect
    log: error
}
```

```bash
systemctl enable --now danted
ss -lntp | grep -E '1080|1081'
ufw allow in on wg0 to any port 1080 proto tcp
ufw allow in on wg0 to any port 1081 proto tcp
```

### 8.3. Kỳ vọng

- `1080` và `1081` listen trên `10.66.0.1`.
- Không listen trên `0.0.0.0`.
- `timeout.io: 0` để không cắt phiên NTRIP dài.

### 8.4. Fallback

Nếu bản Dante không nhận 2 dòng `internal`:
- Tạo 2 file cấu hình riêng.
- Tạo 2 systemd service `danted-src` và `danted-dst`.
- Cùng IP, khác port.

## 9. Giai đoạn 5 - Gost public cho PionLogin

### 9.1. Cài gost

Mở https://github.com/go-gost/gost/releases và tải asset Linux amd64.

```bash
curl -L -o /tmp/gost.tar.gz <GOST_LINUX_AMD64_URL>
tar -xzf /tmp/gost.tar.gz -C /usr/local/bin gost
chmod +x /usr/local/bin/gost
gost -V
```

### 9.2. Tạo mật khẩu

Chỉ dùng chữ và số để tránh lỗi URL:

```bash
openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 32
```

Ghi lại `<USER>` và `<PASS>`.

### 9.3. Systemd service

`/etc/systemd/system/gost-browser.service`:

```ini
[Unit]
Description=Gost public proxy for PionLogin
After=network-online.target
Wants=network-online.target

[Service]
User=nobody
ExecStart=/usr/local/bin/gost -L socks5://<USER>:<PASS>@:1082 -L http://<USER>:<PASS>@:8080
Restart=always
RestartSec=3
LimitNOFILE=65535
NoNewPrivileges=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

```bash
systemctl daemon-reload
systemctl enable --now gost-browser
ss -lntp | grep -E '1082|8080'
journalctl -u gost-browser -f
```

### 9.4. Test từ PC

```powershell
curl.exe --proxy socks5h://<USER>:<PASS>@207.148.103.189:1082 https://ipinfo.io/json
curl.exe --proxy http://<USER>:<PASS>@207.148.103.189:8080 https://ipinfo.io/json
```

Kỳ vọng: `country = JP`.

### 9.5. Bảo mật

- Dùng mật khẩu 32 ký tự, không tái sử dụng.
- Không mở proxy không auth.
- Không dùng `ufw limit` cho 1082/8080 vì trình duyệt mở nhiều kết nối.
- Theo dõi log, đổi mật khẩu/port nếu bị quét.
- Credential SOCKS5/HTTP gửi cleartext; nội dung onocoy là HTTPS nên vẫn mã hóa.
- Nếu cần mã hóa credential, nâng cấp HTTPS proxy có TLS và domain.

## 10. Giai đoạn 6 - Windows relay client

### 10.1. Cài WireGuard

Cài WireGuard for Windows.

### 10.2. Tạo tunnel

`C:\WireGuard\jp.conf`:

```ini
[Interface]
PrivateKey = <RELAY_PRIVATE_KEY>
Address = 10.66.0.2/24

[Peer]
PublicKey = <SERVER_PUBLIC_KEY>
Endpoint = 207.148.103.189:51820
AllowedIPs = 10.66.0.1/32
PersistentKeepalive = 25
```

- Không đặt DNS.
- `AllowedIPs` chỉ `10.66.0.1/32`.
- Không dùng `0.0.0.0/0`.

### 10.3. Cài làm service

```powershell
& "C:\Program Files\WireGuard\wireguard.exe" /installtunnelservice "C:\WireGuard\jp.conf"
Get-Service -Name "WireGuardTunnel*"
```

### 10.4. Test

```powershell
curl.exe --socks5-hostname 10.66.0.1:1080 https://ipinfo.io/json
curl.exe --socks5-hostname 10.66.0.1:1081 https://ipinfo.io/json
curl.exe https://ipinfo.io/json
```

Kỳ vọng:
- Hai lệnh đầu: JP.
- Lệnh cuối: VN.

### 10.5. Phụ thuộc service

Tùy chọn:

```powershell
nssm set NtripRelay DependOnService "WireGuardTunnel$jp"
```

Kiểm tra tên service thực tế bằng `Get-Service -Name "WireGuardTunnel*"`.

## 11. Giai đoạn 7 - Cấu hình relay cho 15 trạm

### 11.1. Backup

```powershell
Copy-Item config.json "config.json.bak_$(Get-Date -Format yyyyMMdd_HHmm)"
```

### 11.2. Mapping

Với 15 trạm JP:

```json
"src_proxy": "10.66.0.1:1080",
"dst_proxy": "10.66.0.1:1081"
```

Với trạm VN:

```json
"src_proxy": "",
"dst_proxy": ""
```

### 11.3. Lưu ý định dạng proxy

- Dùng `host:port`.
- Không dùng `socks5://user:pass@host:port` cho relay.
- `parseProxyURL` ưu tiên format 4 phần `host:port:user:pass`.
- Proxy nội bộ không cần auth nên chỉ dùng `10.66.0.1:1080`.

### 11.4. Restart

```powershell
nssm restart NtripRelay
Get-Content logs\error.log -Tail 100 -Wait
```

### 11.5. Kiểm tra

- 1 trạm JP kết nối thành công.
- Log có `Connected successfully via proxy`.
- Không có `parse proxy` hoặc `dial via proxy` lặp liên tục.

## 12. Giai đoạn 8 - PionLogin 15 profile

### 12.1. Cấu hình proxy trong profile

| Trường | SOCKS5 | HTTP |
|---|---|---|
| Type | SOCKS5 | HTTP |
| Host | `207.148.103.189` | `207.148.103.189` |
| Port | `1082` | `8080` |
| Username | `<USER>` | `<USER>` |
| Password | `<PASS>` | `<PASS>` |

Khuyến nghị dùng SOCKS5 cho antidetect browser.

Nếu PionLogin có type **HTTPS**:
- Đó là TLS tới proxy.
- Gost HTTP thường chưa hỗ trợ.
- Chọn **HTTP** hoặc **SOCKS5**.

### 12.2. Quy trình test

1. Cấu hình 1 profile.
2. Mở `https://ipinfo.io/json`, kiểm tra `country = JP`.
3. Kiểm tra DNS leak/WebRTC nếu PionLogin có.
4. Đăng nhập onocoy.
5. Test 2-3 tài khoản onocoy trước.
6. Nếu ổn, nhân bản cấu hình proxy cho đủ 15 profile.

### 12.3. Cảnh báo rủi ro

15 tài khoản onocoy cùng 1 IP Nhật là rủi ro liên kết tài khoản.

Khuyến nghị:
- Test 2-3 tài khoản trước.
- Theo dõi cảnh báo/khóa.
- Nếu bị liên kết, tách IP cho PionLogin.
- Không tạo đủ 15 profile ngay từ đầu.

## 13. Giai đoạn 9 - Kiểm thử và nghiệm thu

### 13.1. Thứ tự kiểm thử

1. VPS ra Internet:
   ```bash
   curl -s https://ipinfo.io/json
   ```
   Kỳ vọng: JP.

2. WireGuard:
   ```bash
   wg show
   ```
   Kỳ vọng: handshake dưới 2 phút.

3. Dante nội bộ từ Windows:
   ```powershell
   curl.exe --socks5-hostname 10.66.0.1:1080 https://ipinfo.io/json
   curl.exe --socks5-hostname 10.66.0.1:1081 https://ipinfo.io/json
   ```
   Kỳ vọng: JP.

4. Proxy công khai từ PC:
   ```powershell
   curl.exe --proxy socks5h://<USER>:<PASS>@207.148.103.189:1082 https://ipinfo.io/json
   ```
   Kỳ vọng: JP.

5. Onocoy qua proxy:
   ```powershell
   curl.exe --proxy socks5h://<USER>:<PASS>@207.148.103.189:1082 -I https://onocoy.com
   ```
   Kỳ vọng: 200/301/302, không lỗi mạng.

6. Relay 1 trạm JP, theo dõi 2 giờ.

7. Relay đủ 15 trạm, theo dõi 48 giờ.

8. PionLogin 1 profile, 2-3 tài khoản, rồi đủ 15.

### 13.2. Cổng kiểm tra IP Vultr

Trước khi cấu hình hàng loạt:

```bash
curl -I https://onocoy.com
curl -I http://ntrip1.bizstation.jp:2101/
```

Nếu IP bị chặn:
- Destroy instance.
- Deploy lại Tokyo để lấy IP mới.
- Hoặc chuyển provider.

Chỉ tiếp tục khi IP được chấp nhận.

### 13.3. Tiêu chí hoàn thành

- [ ] 15 trạm JP chạy 48 giờ, không còn EOF khoảng 5 phút.
- [ ] 15 trạm VN vẫn ra IP Việt Nam.
- [ ] 15 profile PionLogin ra IP Nhật.
- [ ] Onocoy mở/đăng nhập bình thường.
- [ ] `wg show` handshake ổn định.
- [ ] VPS CPU dưới 5%, RAM dưới 300 MB.
- [ ] Băng thông dưới 750 GB/tháng.
- [ ] Reboot Windows VPS, WG và relay tự lên.
- [ ] Rollback đã thử.

## 14. Giai đoạn 10 - Giám sát và vận hành

### 14.1. Lệnh kiểm tra

```bash
wg show
systemctl status wg-quick@wg0 danted gost-browser
journalctl -u danted -f
journalctl -u gost-browser -f
ss -s
vnstat
```

### 14.2. Windows

```powershell
Get-Service NtripRelay, WireGuardTunnel*
Get-Content logs\error.log -Tail 100 -Wait
```

### 14.3. Định kỳ

- Backup `wg0.conf`, `danted.conf`, `gost-browser.service`, `config.json`.
- Kiểm tra dung lượng log hàng tuần.
- Rotate mật khẩu proxy nếu nghi lộ.
- Kiểm tra IP có bị chặn không sau mỗi thay đổi lớn.

## 15. Giai đoạn 11 - Rollback

| Sự cố | Cách xử lý |
|---|---|
| PionLogin lỗi | Xóa proxy trong profile |
| Relay lỗi | Restore `config.json.bak_*`, restart NSSM |
| Dante/Gost lỗi | `systemctl stop gost-browser danted` |
| WireGuard lỗi | Gỡ tunnel service trên Windows |
| IP Vultr bị chặn | Destroy/redeploy Tokyo lấy IP mới |
| Cần bỏ toàn bộ | Restore config, stop service, destroy VPS |

Trạm VN không bị ảnh hưởng trong mọi trường hợp.

## 16. Rủi ro và đối sách

| Rủi ro | Mức độ | Đối sách |
|---|---|---|
| 15 tài khoản onocoy cùng 1 IP bị liên kết | Cao | Test 2-3 tài khoản; tách IP nếu có cảnh báo |
| Onocoy chặn IP datacenter qua Cloudflare | Trung bình | Test browser; dùng residential proxy cho browser nếu cần |
| Onocoy giới hạn phiên theo IP | Trung bình | Tách IP browser khỏi IP relay; giảm phiên đồng thời |
| Bizstation giới hạn 15 kết nối/1 IP | Trung bình | Test; tách IP hoặc giảm tải |
| UDP 51820 bị chặn VN sang JP | Thấp | Kiểm tra `wg show`; fallback SSH `-D` |
| Proxy công khai bị quét/abuse | Trung bình | Mật khẩu mạnh, port lạ, theo dõi log |
| Egress IPv6 ngoài ý muốn | Thấp | IPv4-only hoặc tắt IPv6 |
| Vultr khóa do public proxy | Thấp | Dùng auth, không mở proxy no-auth |
| Log đầy disk | Thấp | Logrotate, theo dõi dung lượng |

### 16.1. Khuyến nghị tách IP

Nếu muốn giảm rủi ro:
- Thêm IPv4 phụ cho PionLogin.
- Hoặc dùng VPS thứ 2 cho browser.
- Relay NTRIP giữ IP chính.
- Khi IP browser bị ban, relay vẫn hoạt động.

Chi phí tăng tùy Vultr; kiểm tra giá IPv4 phụ trong panel.

## 17. Chi phí và thời gian

### 17.1. Chi phí

| Khoản | Giá |
|---|---|
| Vultr `vc2-1c-1gb` | $5.00/tháng |
| Automatic Backups | $1.00/tháng |
| Tổng | **$6.00/tháng** |
| IPv4 phụ (nếu tách) | Theo bảng giá Vultr |
| Domain (nếu làm HTTPS proxy) | Tùy chọn |

Băng thông dự kiến:
- NTRIP: 40-80 GB/tháng.
- Browser: vài GB đến vài chục GB/tháng.
- Hạn mức: 1 TB/tháng.

### 17.2. Thời gian

| Giai đoạn | Thời gian |
|---|---|
| Chuẩn bị | 15 phút |
| Tạo VPS + hardening | 30 phút |
| WireGuard + Dante + Gost | 30-45 phút |
| Windows relay + PionLogin | 30-45 phút |
| Test 1 trạm/profile | 1-2 giờ |
| Soak 48 giờ | 2 ngày |
| Tổng | Khoảng 3 ngày |

## 18. Checklist tổng

### 18.1. Hạ tầng

- [ ] Vultr Tokyo `vc2-1c-1gb` Debian 12 đã tạo.
- [ ] Automatic Backups bật.
- [ ] SSH key-only.
- [ ] UFW/firewall đúng port.
- [ ] IPv6 đã kiểm tra.

### 18.2. Proxy

- [ ] WireGuard `wg0` hoạt động.
- [ ] Dante listen `10.66.0.1:1080/1081`.
- [ ] `timeout.io: 0`.
- [ ] Gost public `1082/8080` có auth.
- [ ] Gost chạy dưới systemd.
- [ ] Test proxy từ PC ra IP Nhật.

### 18.3. Relay

- [ ] Windows WireGuard tunnel tự chạy.
- [ ] Test `10.66.0.1:1080/1081` từ Windows.
- [ ] 15 trạm JP đã gán proxy.
- [ ] 15 trạm VN giữ nguyên.
- [ ] Backup config trước khi sửa.

### 18.4. PionLogin

- [ ] 1 profile ra IP Nhật.
- [ ] DNS/WebRTC không leak.
- [ ] Onocoy mở được.
- [ ] 2-3 tài khoản test OK.
- [ ] 15 profile cấu hình xong.

### 18.5. Nghiệm thu

- [ ] 48 giờ không còn EOF khoảng 5 phút.
- [ ] VN không bị ảnh hưởng.
- [ ] Rollback đã thử.
- [ ] Tài liệu bàn giao đã cập nhật.

## 19. Phụ lục A - Vị trí file và cấu hình

| File | Vị trí |
|---|---|
| WireGuard server | `/etc/wireguard/wg0.conf` |
| WireGuard client | `C:\WireGuard\jp.conf` |
| Dante | `/etc/danted.conf` |
| Gost service | `/etc/systemd/system/gost-browser.service` |
| Relay config | `config.json` trong thư mục relay |
| Log relay | `logs\error.log` |
| Backup relay | `config.json.bak_*` |

## 20. Phụ lục B - Lệnh kiểm tra nhanh

```bash
# VPS
wg show
ss -lntp | grep -E '1080|1081|1082|8080'
systemctl status wg-quick@wg0 danted gost-browser
curl -s https://ipinfo.io/json

# Windows
curl.exe --socks5-hostname 10.66.0.1:1080 https://ipinfo.io/json
curl.exe --socks5-hostname 10.66.0.1:1081 https://ipinfo.io/json
curl.exe https://ipinfo.io/json
curl.exe --proxy socks5h://<USER>:<PASS>@207.148.103.189:1082 https://ipinfo.io/json
```

## 21. Phụ lục C - Xử lý sự cố

| Hiện tượng | Nguyên nhân thường gặp | Cách xử lý |
|---|---|---|
| `wg show` không handshake | UDP 51820 bị chặn | Kiểm tra Vultr Firewall/UFW |
| Không kết nối `10.66.0.1:1080` | Dante chưa chạy/sai interface | `systemctl status danted`, `ss -lntp` |
| `connection refused` tới 1082/8080 | Gost chưa chạy/UFW | `systemctl status gost-browser`, `ufw status` |
| Sai user/pass | Credential sai/ký tự đặc biệt | Đổi mật khẩu chữ-số, cập nhật profile |
| Onocoy bị challenge | IP datacenter bị đánh dấu | Test IP, đổi IP/provider, residential proxy |
| Relay vẫn EOF khoảng 5 phút | Giới hạn caster/account | Kiểm tra onocoy/bizstation, tách IP |
| DNS leak | PionLogin resolve local | Bật proxy DNS/remote DNS |
| Egress IPv6 | VPS có IPv6 | Tắt IPv6 hoặc cấu hình IPv4-only |
| Log đầy disk | Không rotate | Cấu hình logrotate, xóa log cũ |

## 22. Phụ lục D - Lịch sử thay đổi

| Ngày | Phiên bản | Nội dung |
|---|---|---|
| 2026-10-24 | 1.0 | Bản kế hoạch đầy đủ cho Vultr Tokyo, WireGuard, Dante, Gost, PionLogin |
| 2026-10-24 | 1.1 | Cập nhật IP VPS Nhật 207.148.103.189 và IP Windows VPS 103.166.183.250 |
