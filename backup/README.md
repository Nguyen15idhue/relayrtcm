
---

### PHẦN 1: CHUẨN BỊ MÔI TRƯỜNG

#### 1. Cài đặt ngôn ngữ Go (Golang)
*   Tải về: [https://go.dev/dl/](https://go.dev/dl/) (Chọn bản **Microsoft Windows**).
*   Cài đặt: Cứ ấn **Next** liên tục cho đến khi xong (**Finish**).

#### 2. Tải công cụ quản lý Service (NSSM)
*   Tải về: [https://nssm.cc/download](https://nssm.cc/download) (Chọn bản mới nhất, vd: `nssm-2.24.zip`).
*   Giải nén ra, vào thư mục `win64`, copy file **`nssm.exe`**.

#### 3. Tạo thư mục làm việc
Để tránh lỗi quyền hạn, hãy làm ở ổ C gốc.
1.  Tạo thư mục: **`C:\NtripRelay`**
2.  Dán file **`nssm.exe`** vừa copy vào đây.
3.  Tạo thêm thư mục con tên là **`logs`** (để chứa file báo lỗi).

Lúc này cấu trúc thư mục `C:\NtripRelay` sẽ có:
*   `logs/` (Thư mục rỗng)
*   `nssm.exe`

---

### PHẦN 2: TẠO MÃ NGUỒN VÀ CẤU HÌNH

Tại thư mục `C:\NtripRelay`, bạn tạo 2 file sau:

#### 1. File cấu hình: `config.json`
Mở Notepad, dán nội dung dưới đây và lưu tên là `config.json` (chọn Save as type: All Files).

```json
[
  {
    "id": "tram_mau_1",
    "enable": true,
    "src_host": "rtktk.online",
    "src_port": 1509,
    "src_mount": "YBI_VanYen",
    "src_user": "nguyen",
    "src_pass": "12345678",
    "dst_host": "servers.onocoy.com",
    "dst_port": 2101,
    "dst_mount": "YBVY_Relay",
    "dst_user": "PlainlyFairFirefly",
    "dst_pass": "Nguyen1509232",
    "lat": 21.0,
    "lon": 105.0
  }
]
```

#### 2. File mã nguồn: `main.go`
Tạo file tên là `main.go`, dán toàn bộ code (phiên bản đã sửa lỗi ngắt kết nối và tối ưu) dưới đây:

```go
package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ConfigFile       = "config.json"
	MonitorPort      = ":8080"
	ReconnectDelay   = 5 * time.Second
	BlockWaitDelay   = 30 * time.Second
	SendNMEAInterval = 10 * time.Second
	ReadTimeout      = 15 * time.Second
	DialTimeout      = 10 * time.Second
)

type ConfigStation struct {
	ID       string  `json:"id"`
	Enable   bool    `json:"enable"`
	SrcHost  string  `json:"src_host"`
	SrcPort  int     `json:"src_port"`
	SrcMount string  `json:"src_mount"`
	SrcUser  string  `json:"src_user"`
	SrcPass  string  `json:"src_pass"`
	DstHost  string  `json:"dst_host"`
	DstPort  int     `json:"dst_port"`
	DstMount string  `json:"dst_mount"`
	DstUser  string  `json:"dst_user"`
	DstPass  string  `json:"dst_pass"`
	Lat      float64 `json:"lat"`
	Lon      float64 `json:"lon"`
}

type StationStatus struct {
	ID             string    `json:"id"`
	Status         string    `json:"status"`
	BytesForwarded int64     `json:"bytes_forwarded"`
	Uptime         string    `json:"uptime"`
	LastMessage    string    `json:"last_message"`
	StartTime      time.Time `json:"-"`
}

type Worker struct {
	cfg        ConfigStation
	ctx        context.Context
	cancel     context.CancelFunc
	status     *StationStatus
	configHash string
}

type StationManager struct {
	mu      sync.RWMutex
	workers map[string]*Worker
}

var manager = &StationManager{
	workers: make(map[string]*Worker),
}

func main() {
	log.Println("=== NTRIP RELAY SYSTEM STARTED ===")
	go startMonitorServer()
	reloadConfig()
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		reloadConfig()
	}
}

func reloadConfig() {
	file, err := os.ReadFile(ConfigFile)
	if err != nil {
		log.Printf("[System] Cannot read config: %v", err)
		return
	}
	var configs []ConfigStation
	if err := json.Unmarshal(file, &configs); err != nil {
		log.Printf("[System] JSON error: %v", err)
		return
	}
	manager.mu.Lock()
	defer manager.mu.Unlock()
	activeIDs := make(map[string]bool)
	for _, cfg := range configs {
		activeIDs[cfg.ID] = true
		hash := getMD5Hash(cfg)
		worker, exists := manager.workers[cfg.ID]
		if exists {
			if worker.configHash != hash || !cfg.Enable {
				log.Printf("[%s] Config changed. Restarting...", cfg.ID)
				worker.cancel()
				delete(manager.workers, cfg.ID)
				exists = false
			}
		}
		if !exists && cfg.Enable {
			ctx, cancel := context.WithCancel(context.Background())
			status := &StationStatus{ID: cfg.ID, Status: "Starting"}
			w := &Worker{cfg: cfg, ctx: ctx, cancel: cancel, status: status, configHash: hash}
			manager.workers[cfg.ID] = w
			go w.Start()
			log.Printf("[%s] Started.", cfg.ID)
		}
	}
	for id, worker := range manager.workers {
		if !activeIDs[id] {
			log.Printf("[%s] Removed. Stopping...", id)
			worker.cancel()
			delete(manager.workers, id)
		}
	}
}

func (w *Worker) Start() {
	w.status.StartTime = time.Now()
	for {
		select {
		case <-w.ctx.Done():
			w.status.Status = "Stopped"
			return
		default:
			err := w.runSession()
			if err != nil {
				w.status.Status = "Error"
				w.status.LastMessage = err.Error()
				delay := ReconnectDelay
				if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "reset by peer") {
					delay = BlockWaitDelay
					w.status.LastMessage += " (Waiting 30s...)"
				}
				log.Printf("[%s] Error: %v. Retry in %v...", w.cfg.ID, err, delay)
				select {
				case <-time.After(delay):
				case <-w.ctx.Done():
					return
				}
			}
		}
	}
}

func (w *Worker) runSession() error {
	w.status.Status = "Connecting Source"
	srcConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", w.cfg.SrcHost, w.cfg.SrcPort), DialTimeout)
	if err != nil {
		return fmt.Errorf("connect source: %w", err)
	}
	defer srcConn.Close()

	authSrc := basicAuth(w.cfg.SrcUser, w.cfg.SrcPass)
	reqSrc := fmt.Sprintf("GET /%s HTTP/1.1\r\nHost: %s\r\nNtrip-Version: Ntrip/2.0\r\nUser-Agent: NTRIP GoRelay/2.0\r\nAuthorization: Basic %s\r\nConnection: close\r\n\r\n", w.cfg.SrcMount, w.cfg.SrcHost, authSrc)
	if _, err := srcConn.Write([]byte(reqSrc)); err != nil {
		return err
	}
	if err := checkResponse(srcConn); err != nil {
		return fmt.Errorf("source auth: %w", err)
	}
	srcConn.Write([]byte(generateNMEA(w.cfg.Lat, w.cfg.Lon)))

	w.status.Status = "Connecting Dest"
	dstConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", w.cfg.DstHost, w.cfg.DstPort), DialTimeout)
	if err != nil {
		return fmt.Errorf("connect dest: %w", err)
	}
	defer dstConn.Close()

	authDst := basicAuth(w.cfg.DstUser, w.cfg.DstPass)
	reqDst := fmt.Sprintf("POST /%s HTTP/1.1\r\nHost: %s\r\nNtrip-Version: Ntrip/2.0\r\nUser-Agent: NTRIP GoRelay/2.0\r\nAuthorization: Basic %s\r\nContent-Type: application/octet-stream\r\nConnection: close\r\n\r\n", w.cfg.DstMount, w.cfg.DstHost, authDst)
	if _, err := dstConn.Write([]byte(reqDst)); err != nil {
		return err
	}
	if err := checkResponse(dstConn); err != nil {
		return fmt.Errorf("dest auth: %w", err)
	}

	w.status.Status = "Running"
	w.status.LastMessage = "Streaming OK"
	log.Printf("[%s] RELAYING: %s -> %s", w.cfg.ID, w.cfg.SrcMount, w.cfg.DstMount)

	errChan := make(chan error, 2)
	go func() {
		ticker := time.NewTicker(SendNMEAInterval)
		defer ticker.Stop()
		for {
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
				if _, err := srcConn.Write([]byte(generateNMEA(w.cfg.Lat, w.cfg.Lon))); err != nil {
					return
				}
			}
		}
	}()
	go func() {
		buf := make([]byte, 1024)
		for {
			_, err := dstConn.Read(buf)
			if err != nil {
				errChan <- fmt.Errorf("dest closed: %v", err)
				return
			}
		}
	}()

	buf := make([]byte, 8192)
	for {
		srcConn.SetReadDeadline(time.Now().Add(ReadTimeout))
		n, err := srcConn.Read(buf)
		if err != nil {
			return fmt.Errorf("read source: %v", err)
		}
		if n > 0 {
			dstConn.SetWriteDeadline(time.Now().Add(DialTimeout))
			_, err := dstConn.Write(buf[:n])
			if err != nil {
				return fmt.Errorf("write dest: %v", err)
			}
			dstConn.SetWriteDeadline(time.Time{})
			atomic.AddInt64(&w.status.BytesForwarded, int64(n))
		}
		select {
		case err := <-errChan:
			return err
		default:
		}
	}
}

func checkResponse(conn net.Conn) error {
	reader := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF { return fmt.Errorf("server closed connection immediately (EOF)") }
		return err
	}
	for {
		l, err := reader.ReadString('\n')
		if err != nil { break }
		if l == "\r\n" || l == "\n" { break }
	}
	if !strings.Contains(line, "200 OK") && !strings.Contains(line, "ICY 200") {
		return fmt.Errorf("server rejected: %s", strings.TrimSpace(line))
	}
	conn.SetReadDeadline(time.Time{})
	return nil
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func generateNMEA(lat, lon float64) string {
	now := time.Now().UTC()
	timestamp := fmt.Sprintf("%02d%02d%02d.00", now.Hour(), now.Minute(), now.Second())
	latStr := toDegMinDir(lat, true)
	lonStr := toDegMinDir(lon, false)
	raw := fmt.Sprintf("GPGGA,%s,%s,%s,1,10,1.0,100.0,M,-5.0,M,,", timestamp, latStr, lonStr)
	var checksum byte
	for i := 0; i < len(raw); i++ {
		checksum ^= raw[i]
	}
	return fmt.Sprintf("$%s*%02X\r\n", raw, checksum)
}

func toDegMinDir(val float64, isLat bool) string {
	absVal := math.Abs(val)
	deg := int(absVal)
	min := (absVal - float64(deg)) * 60
	dir := ""
	if isLat {
		if val >= 0 { dir = "N" } else { dir = "S" }
		return fmt.Sprintf("%02d%07.4f,%s", deg, min, dir)
	} else {
		if val >= 0 { dir = "E" } else { dir = "W" }
		return fmt.Sprintf("%03d%07.4f,%s", deg, min, dir)
	}
}

func getMD5Hash(c ConfigStation) string {
	data, _ := json.Marshal(c)
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:])
}

func startMonitorServer() {
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		var stats []StationStatus
		for _, worker := range manager.workers {
			s := *worker.status
			s.Uptime = time.Since(worker.status.StartTime).Round(time.Second).String()
			stats = append(stats, s)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})
	log.Fatal(http.ListenAndServe(MonitorPort, nil))
}
```

---

### PHẦN 3: BIÊN DỊCH FILE CHẠY (.EXE)

1.  Bấm phím Windows, gõ **`cmd`**, mở **Command Prompt**.
2.  Gõ các lệnh sau để tạo file chạy:
    ```cmd
    cd C:\NtripRelay
    go build -ldflags "-s -w" -o relay.exe main.go
    ```
    *Lưu ý: Sau khi chạy xong, trong thư mục sẽ xuất hiện file `relay.exe`.*

---

### PHẦN 4: CÀI ĐẶT TỰ ĐỘNG CHẠY (QUAN TRỌNG)

1.  Mở **CMD** với quyền **Administrator** (Bấm Windows -> gõ cmd -> Chuột phải chọn Run as Administrator).
2.  Chạy lần lượt 2 lệnh sau:
    ```cmd
    cd C:\NtripRelay
    nssm.exe install NtripRelayService
    ```
    *(Một bảng cấu hình sẽ hiện ra)*.

3.  **Điền thông tin vào bảng:**
    *   **Thẻ Application:**
        *   Path: `C:\NtripRelay\relay.exe`
        *   Startup directory: `C:\NtripRelay`  <-- **(Bắt buộc phải đúng)**
    *   **Thẻ I/O:** (Chuyển sang tab này)
        *   Output (stdout): `C:\NtripRelay\logs\output.log`
        *   Error (stderr): `C:\NtripRelay\logs\error.log`
    *   Bấm nút **Install service**.

4.  **Bật chương trình:**
    Quay lại cửa sổ CMD, gõ lệnh:
    ```cmd
    nssm start NtripRelayService
    ```
    *(Nếu báo `NtripRelayService: Unexpected status SERVICE_PAUSED` kệ nó, miễn là nó chạy).*

---

### PHẦN 5: KIỂM TRA VÀ SỬ DỤNG

1.  **Kiểm tra:** Mở trình duyệt trên VPS, gõ: `http://localhost:8080/status`. Nếu thấy dữ liệu JSON là thành công.
2.  **Xem log lỗi:** Mở file `C:\NtripRelay\logs\error.log` bằng Notepad.

---

### PHẦN 6: QUẢN LÝ HÀNG NGÀY

#### 1. Cách thêm trạm mới (Không cần tắt chương trình)
*   Mở file `config.json` bằng Notepad.
*   Thêm trạm mới vào trong dấu ngoặc vuông `[ ]`. Nhớ có dấu phẩy `,` ngăn cách giữa các trạm.
*   Lưu file lại (Ctrl + S).
*   Chờ 10 giây, web giám sát sẽ tự hiện trạm mới.

#### 2. Cách cập nhật file EXE mới (Nếu bạn sửa code)
Khi bạn sửa code trong `main.go` và build lại ra `relay.exe` mới, bạn làm như sau:

1.  Mở CMD (Admin), gõ lệnh dừng:
    ```cmd
    ,.\nssm stop NtripRelayService
    ```
2.  Copy file `relay.exe` mới đè lên file cũ trong `C:\NtripRelay`.
3.  Gõ lệnh bật lại:
    ```cmd
    .\nssm start NtripRelayService
    ```

