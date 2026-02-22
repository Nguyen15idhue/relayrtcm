# 🔍 Proxy Timeout Troubleshooting Guide

## 🧪 BƯỚC 1: TEST PROXY

### Chỉnh sửa test_proxy.go:

```go
// Dòng 19-20: Nhập proxy của bạn
proxyConfig := "your-proxy.com:1080:username:password"

// Hoặc format khác:
// proxyConfig := "socks5://username:password@your-proxy.com:1080"

// Dòng 23-27: Điền thông tin test
testHost := "crtk.net"      // Hoặc host thật của bạn
testPort := 2101
testMount := "RUDV"
testUser := "centipede"
testPass := "centipede"
```

### Chạy test:

```powershell
go run test_proxy.go
```

### Kết quả mong đợi:

```
✅ Proxy server reachable
✅ SOCKS5 handshake works  
✅ Can connect to target via proxy
✅ NTRIP authentication works
✅ Data stream OK
```

---

## ❌ NẾU BỊ TIMEOUT - NGUYÊN NHÂN & FIX

### **1. Timeout ở TEST 2 (Cannot reach proxy server)**

**Nguyên nhân:**
- Proxy IP/port sai
- Firewall block
- Proxy provider offline

**Fix:**
```powershell
# Test proxy thủ công
telnet proxy-host port
# Hoặc
Test-NetConnection proxy-host -Port port
```

**Liên hệ proxy provider nếu không reach được.**

---

### **2. Timeout ở TEST 3 (SOCKS5 handshake)**

**Nguyên nhân:**
- Username/password sai
- Proxy không phải SOCKS5 (là HTTP proxy)
- Auth method không đúng

**Fix:**

#### A. Kiểm tra proxy type:
```powershell
# Nếu provider cho HTTP proxy thay vì SOCKS5:
# → KHÔNG DÙNG ĐƯỢC với NTRIP (cần TCP, không phải HTTP)
# → Yêu cầu provider cấp SOCKS5 proxy
```

#### B. Test auth:
```go
// Thử không auth trước
proxyConfig := "proxy-host:port"  // Không có user:pass
```

#### C. Format khác nhau:
```go
// Thử các format:
"host:port:user:pass"
"socks5://user:pass@host:port"
"socks5://host:port"  // Nếu không cần auth
```

---

### **3. Timeout ở TEST 3 (Dial via proxy)**

**Nguyên nhân:**
- Proxy BLOCK target host/port
- Proxy chỉ cho phép HTTP/HTTPS (port 80, 443)
- Target không reachable từ proxy location

**Fix:**

#### A. Test với target khác:
```go
testHost := "google.com"
testPort := 80
```

Nếu Google works nhưng NTRIP server không → **Proxy chặn port 2101**

**Giải pháp:**
- Yêu cầu proxy provider whitelist port 2101
- Hoặc đổi proxy provider (cần SOCKS5 không giới hạn port)

#### B. Proxy providers KHÔNG GIỜ HẠN PORT:
- ✅ **Luminati / Bright Data** - All ports
- ✅ **Smartproxy** - All ports  
- ✅ **IPRoyal** - All ports
- ❌ **Webshare** - HTTP/HTTPS only (port 80, 443)
- ❌ **ProxyMesh** - HTTP only

---

### **4. Slow/Unstable Connection**

**Nguyên nhân:**
- Proxy location xa target
- Proxy overloaded
- Cheap proxy quality kém

**Fix:**

#### A. Tăng timeout trong code:
```go
// main.go - Tăng các timeout
const (
    ProxyDialTimeout = 30 * time.Second  // Từ 10s → 30s
    DialTimeout      = 45 * time.Second  // Từ 30s → 45s
    ReadTimeout      = 180 * time.Second // Từ 120s → 180s
)
```

#### B. Test latency proxy:
```powershell
# Ping proxy server
ping proxy-host

# Nếu latency >200ms → proxy xa
```

#### C. Chọn proxy gần target:
```
Target: US → Chọn US proxy
Target: EU → Chọn EU proxy
Target: Asia → Chọn Asia proxy
```

#### D. Sticky sessions:
```json
{
  "src_proxy": "sticky-session-proxy:1080:user:pass:session123"
}
```

---

### **5. Proxy bị ban/blacklist**

**Nguyên nhân:**
- Target server detect proxy và block
- Quá nhiều connections từ 1 IP

**Fix:**

#### A. Rotating proxies:
```json
// Mỗi worker 1 proxy khác nhau
{
  "id": "RELAY_1",
  "src_proxy": "proxy1:1080:user:pass"
},
{
  "id": "RELAY_2",  
  "src_proxy": "proxy2:1080:user:pass"
}
```

#### B. Sticky sessions với timeout:
Request proxy provider cấp sticky IP (30 phút - 24h)

#### C. Residential proxies thay datacenter:
- Datacenter IPs dễ bị detect
- Residential IPs (home IPs) khó block hơn

---

## 🎯 RECOMMENDED PROXY PROVIDERS

### **Budget: $10-50/mo**

#### **IPRoyal SOCKS5**
- ✅ $7/GB, pay as you go
- ✅ All ports allowed
- ✅ Sticky sessions
- Link: https://iproyal.com/

**Test trial:** $1.75/GB starter

---

#### **Proxy-Cheap**  
- ✅ $3/GB SOCKS5
- ✅ Residential IPs
- ✅ Unlimited ports
- Link: https://www.proxy-cheap.com/

---

### **Budget: $50-100/mo**

#### **Smartproxy**
- ✅ $75/mo for 5GB
- ✅ 40M+ residential IPs
- ✅ 195 locations
- ✅ All ports, SOCKS5 support
- Link: https://smartproxy.com/

**Free trial:** 3 days / $1

---

#### **Oxylabs**
- ✅ $49/month starter
- ✅ Premium quality
- ✅ 99.9% uptime SLA
- Link: https://oxylabs.io/

---

### **Budget: $300+/mo (Enterprise)**

#### **Bright Data (Luminati)**
- ✅ 72M+ IPs
- ✅ 99.99% uptime
- ✅ Dedicated account manager
- ✅ All protocols (SOCKS5, HTTP, HTTPS)
- Link: https://brightdata.com/

**Trial:** $5 credit

---

## 🔧 OPTIMIZE TIMEOUT SETTINGS

### Nếu proxy works nhưng đôi khi timeout:

```go
// main.go - Adjust timeouts
const (
    // Proxy timeouts
    ProxyDialTimeout  = 30 * time.Second  // Tăng từ 10s
    
    // Connection timeouts
    DialTimeout       = 60 * time.Second  // Tăng từ 30s
    ReadTimeout       = 180 * time.Second // Tăng từ 120s
    
    // Retry settings
    NormalRetryDelay  = 5 * time.Second   // Tăng từ 3s
    BlockRetryDelay   = 60 * time.Second  // Tăng từ 30s
)
```

### Thêm retry logic cho proxy:

```go
// Retry dial nếu timeout
func dialWithRetry(ctx context.Context, dialer proxy.Dialer, network, addr string, retries int) (net.Conn, error) {
    var lastErr error
    
    for i := 0; i < retries; i++ {
        conn, err := dialer.Dial(network, addr)
        if err == nil {
            return conn, nil
        }
        
        lastErr = err
        
        // Exponential backoff
        sleep := time.Duration(i+1) * 2 * time.Second
        time.Sleep(sleep)
    }
    
    return nil, fmt.Errorf("failed after %d retries: %w", retries, lastErr)
}
```

---

## 📊 PROXY COMPARISON

| Provider | Type | Price | Ports | Stability | Best For |
|----------|------|-------|-------|-----------|----------|
| **IPRoyal** | Residential | $7/GB | All | ⭐⭐⭐ | Budget |
| **Smartproxy** | Residential | $75/mo | All | ⭐⭐⭐⭐ | Production |
| **Oxylabs** | Premium | $49/mo | All | ⭐⭐⭐⭐⭐ | Professional |
| **Bright Data** | Enterprise | $500/mo | All | ⭐⭐⭐⭐⭐ | Large scale |

---

## 🚨 RED FLAGS - BAD Proxies

❌ **KHÔNG DÙNG nếu proxy:**
- Chỉ support HTTP/HTTPS (không có SOCKS5)
- Block các ports không phải 80/443
- Không có sticky sessions
- Free proxies từ proxy lists
- Uptime < 95%
- Latency > 500ms

---

## ✅ CHECKLIST SAU KHI MUA PROXY

- [ ] Test với `test_proxy.go`
- [ ] Confirm SOCKS5 protocol
- [ ] Confirm all ports allowed
- [ ] Test latency < 200ms
- [ ] Test với 1 worker trước
- [ ] Monitor trong 24h
- [ ] Scale dần lên 10, 50, 300 workers

---

**Chạy test ngay:**
```powershell
# Sửa proxy config trong test_proxy.go
# Rồi chạy:
go run test_proxy.go
```

**Paste kết quả cho tôi nếu vẫn bị timeout!**
