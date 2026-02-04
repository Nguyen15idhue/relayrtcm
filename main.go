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

// ================= CẤU HÌNH HỆ THỐNG =================
const (
	ConfigFile  = "config.json"
	MonitorPort = ":8081"

	// Timeout & Interval
	NormalRetryDelay = 5 * time.Second  // Chờ khi đứt mạng bình thường
	BlockRetryDelay  = 30 * time.Second // Chờ khi bị Server đá (EOF/Auth fail)
	ReadTimeout      = 60 * time.Second // Nếu nguồn im lặng 60s -> Reset
	DialTimeout      = 15 * time.Second // Timeout khi kết nối TCP
	SendNMEAInterval = 10 * time.Second // Gửi NMEA mỗi 10s

	// Buffer Size: 32KB là tối ưu cho luồng TCP
	BufferSize = 32 * 1024
)

// ================= MEMORY POOL (Tối ưu RAM) =================
var bufPool = sync.Pool{
	New: func() interface{} {
		// Cấp phát mảng byte một lần, tái sử dụng mãi mãi
		b := make([]byte, BufferSize)
		return &b
	},
}

// ================= DATA STRUCTURES =================
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
	Order          int       `json:"-"`
}

type Worker struct {
	cfg          ConfigStation
	ctx          context.Context
	cancel       context.CancelFunc
	status       *StationStatus
	configHash   string
	wg           sync.WaitGroup // Đợi các goroutine con dọn dẹp xong
	lastDataTime int64          // Unix timestamp lần nhận data cuối (atomic)
}

type StationManager struct {
	mu          sync.RWMutex
	workers     map[string]*Worker
	lastModTime time.Time
}

var manager = &StationManager{
	workers: make(map[string]*Worker),
}

// ================= MAIN ENTRY =================
func main() {
	// Cấu hình log hiển thị thời gian và dòng code lỗi
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("=== NTRIP RELAY SYSTEM (ULTIMATE STABILITY) ===")

	// Khởi động Web Monitor
	go startMonitorServer()

	// Load config lần đầu
	reloadConfig()

	// Theo dõi file config mỗi 5 giây
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		reloadConfig()
	}
}

// ================= CONFIG MANAGER =================
func reloadConfig() {
	// 1. Kiểm tra nhanh xem file có đổi không (tiết kiệm CPU)
	stat, err := os.Stat(ConfigFile)
	if err != nil {
		log.Printf("[System] Cannot check config file: %v", err)
		return
	}

	manager.mu.Lock()
	if !stat.ModTime().After(manager.lastModTime) {
		manager.mu.Unlock()
		return // File chưa sửa, thoát ngay
	}
	manager.lastModTime = stat.ModTime()
	manager.mu.Unlock() // Mở khóa để đọc file

	// 2. Đọc và Parse
	file, err := os.ReadFile(ConfigFile)
	if err != nil {
		log.Printf("[System] Read config failed: %v", err)
		return
	}

	var configs []ConfigStation
	if err := json.Unmarshal(file, &configs); err != nil {
		log.Printf("[System] JSON parse failed: %v", err)
		return
	}

	// 3. Cập nhật Workers
	manager.mu.Lock()
	defer manager.mu.Unlock()

	activeIDs := make(map[string]bool)
	log.Println("[System] Configuration changed. Applying...")

	for i, cfg := range configs {
		activeIDs[cfg.ID] = true
		hash := getMD5Hash(cfg)

		worker, exists := manager.workers[cfg.ID]
		if exists {
			// Cập nhật thứ tự hiển thị
			worker.status.Order = i

			// Nếu config quan trọng thay đổi -> Restart worker
			if worker.configHash != hash || !cfg.Enable {
				log.Printf("[%s] Config changed. Restarting worker...", cfg.ID)
				worker.cancel()  // Gửi lệnh dừng
				worker.wg.Wait() // Chờ dừng hẳn
				delete(manager.workers, cfg.ID)
				exists = false
			}
		}

		if !exists && cfg.Enable {
			// Khởi tạo Worker mới
			ctx, cancel := context.WithCancel(context.Background())
			status := &StationStatus{ID: cfg.ID, Status: "Starting", Order: i}
			w := &Worker{
				cfg:        cfg,
				ctx:        ctx,
				cancel:     cancel,
				status:     status,
				configHash: hash,
			}
			manager.workers[cfg.ID] = w
			go w.Start() // Chạy vòng lặp chính
			log.Printf("[%s] Worker initialized.", cfg.ID)
		}
	}

	// Xóa các worker bị xóa khỏi config
	for id, worker := range manager.workers {
		if !activeIDs[id] {
			log.Printf("[%s] Removed from config. Stopping...", id)
			worker.cancel()
			worker.wg.Wait()
			delete(manager.workers, id)
		}
	}
}

// ================= WORKER CORE LOGIC =================
func (w *Worker) Start() {
	w.wg.Add(1)
	defer w.wg.Done()

	w.status.StartTime = time.Now()

	// Vòng lặp vĩnh cửu (cho đến khi config tắt)
	for {
		// Kiểm tra xem có lệnh dừng không
		select {
		case <-w.ctx.Done():
			w.status.Status = "Stopped"
			return
		default:
		}

		sessionStart := time.Now()

		// --- BẮT ĐẦU PHIÊN LÀM VIỆC ---
		err := w.runSession()
		// -----------------------------

		if err != nil {
			// Logic xử lý lỗi thông minh
			w.status.Status = "Error"
			w.status.LastMessage = err.Error()

			// Tính thời gian phiên vừa chạy
			runDuration := time.Since(sessionStart)

			delay := NormalRetryDelay

			// Nếu lỗi Auth, EOF, hoặc phiên chạy quá ngắn (<10s) -> Nghi ngờ bị chặn
			if strings.Contains(err.Error(), "EOF") ||
				strings.Contains(err.Error(), "forcibly closed") ||
				strings.Contains(err.Error(), "rejected") ||
				runDuration < 10*time.Second {

				delay = BlockRetryDelay
				w.status.LastMessage += " (Anti-Ban Wait 30s)"
			}

			log.Printf("[%s] Error: %v. Retry in %v", w.cfg.ID, err, delay)

			// Chờ trước khi thử lại (có thể bị cancel giữa chừng)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
				// Hết giờ, thử lại
			case <-w.ctx.Done():
				timer.Stop()
				return
			}
		}
	}
}

// Hàm xử lý kết nối chính
func (w *Worker) runSession() error {
	// Dùng Dialer để có thể cancel kết nối đang pending
	dialer := net.Dialer{Timeout: DialTimeout}

	// 1. KẾT NỐI SOURCE (NGUỒN)
	w.status.Status = "Connecting Source"
	srcConn, err := dialer.DialContext(w.ctx, "tcp", fmt.Sprintf("%s:%d", w.cfg.SrcHost, w.cfg.SrcPort))
	if err != nil {
		return fmt.Errorf("dial source: %w", err)
	}
	defer srcConn.Close()

	// Gửi Header GET
	authSrc := basicAuth(w.cfg.SrcUser, w.cfg.SrcPass)
	reqSrc := fmt.Sprintf("GET /%s HTTP/1.1\r\nHost: %s\r\nNtrip-Version: Ntrip/2.0\r\nUser-Agent: NTRIP GoRelay/2.0\r\nAuthorization: Basic %s\r\nConnection: close\r\n\r\n",
		w.cfg.SrcMount, w.cfg.SrcHost, authSrc)
	if _, err := srcConn.Write([]byte(reqSrc)); err != nil {
		return fmt.Errorf("send request source: %w", err)
	}

	// [QUAN TRỌNG] Bufio bọc lấy srcConn. Cần giữ cái Reader này dùng mãi mãi.
	srcReader := bufio.NewReaderSize(srcConn, BufferSize)
	if err := checkResponse(srcReader, srcConn); err != nil {
		return fmt.Errorf("source auth: %w", err)
	}

	// Gửi NMEA mở hàng (ban đầu là Single vì chưa có data)
	srcConn.Write([]byte(generateNMEA(w.cfg.Lat, w.cfg.Lon, false)))

	// 2. KẾT NỐI DESTINATION (ĐÍCH)
	w.status.Status = "Connecting Dest"
	dstConn, err := dialer.DialContext(w.ctx, "tcp", fmt.Sprintf("%s:%d", w.cfg.DstHost, w.cfg.DstPort))
	if err != nil {
		return fmt.Errorf("dial dest: %w", err)
	}
	defer dstConn.Close()

	// Gửi Header POST
	authDst := basicAuth(w.cfg.DstUser, w.cfg.DstPass)
	reqDst := fmt.Sprintf("POST /%s HTTP/1.1\r\nHost: %s\r\nNtrip-Version: Ntrip/2.0\r\nUser-Agent: NTRIP GoRelay/2.0\r\nAuthorization: Basic %s\r\nContent-Type: application/octet-stream\r\nConnection: close\r\n\r\n",
		w.cfg.DstMount, w.cfg.DstHost, authDst)
	if _, err := dstConn.Write([]byte(reqDst)); err != nil {
		return fmt.Errorf("send request dest: %w", err)
	}

	// Check Dest trả lời
	dstReader := bufio.NewReader(dstConn)
	if err := checkResponse(dstReader, dstConn); err != nil {
		return fmt.Errorf("dest auth: %w", err)
	}

	// 3. CHUYỂN TRẠNG THÁI STREAMING
	w.status.Status = "Running"
	w.status.LastMessage = "Streaming OK"
	log.Printf("[%s] CONNECTED: %s -> %s", w.cfg.ID, w.cfg.SrcMount, w.cfg.DstMount)

	// Channel báo lỗi từ các luồng phụ
	errChan := make(chan error, 1)

	// -- Luồng phụ 1: Gửi NMEA Heartbeat --
	go func() {
		ticker := time.NewTicker(SendNMEAInterval)
		defer ticker.Stop()
		for {
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
				// Kiểm tra xem có data gần đây không (trong 30s)
				lastData := atomic.LoadInt64(&w.lastDataTime)
				hasRecentData := (time.Now().Unix() - lastData) <= 30
				
				// Tạo NMEA với fix quality phù hợp
				msg := generateNMEA(w.cfg.Lat, w.cfg.Lon, hasRecentData)
				srcConn.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if _, err := srcConn.Write([]byte(msg)); err != nil {
					// Lỗi gửi NMEA -> Coi như mất kết nối Source
					select {
					case errChan <- fmt.Errorf("nmea write error: %v", err):
					default:
					}
					return
				}
				srcConn.SetWriteDeadline(time.Time{})
				
				// Cập nhật timestamp khi gửi NMEA thành công
				if hasRecentData {
					atomic.StoreInt64(&w.lastDataTime, time.Now().Unix())
				}
			}
		}
	}()

	// -- Luồng phụ 2: Đọc phản hồi từ Dest (để phát hiện nếu Dest ngắt) --
	go func() {
		// Dùng buffer nhỏ từ pool để đọc bỏ
		bufPtr := bufPool.Get().(*[]byte)
		defer bufPool.Put(bufPtr)
		buf := *bufPtr

		for {
			// Đọc không timeout, chỉ đợi lỗi
			_, err := dstConn.Read(buf)
			if err != nil {
				select {
				case errChan <- fmt.Errorf("dest connection closed: %v", err):
				default:
				}
				return
			}
		}
	}()

	// -- Luồng chính: Đọc Source -> Ghi Dest --
	bufPtr := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufPtr)
	buf := *bufPtr

	for {
		// Set Timeout đọc: Nếu 60s mà Source không gửi byte nào -> Kill
		srcConn.SetReadDeadline(time.Now().Add(ReadTimeout))

		// ĐỌC TỪ BUFFER READER (Không phải conn)
		n, err := srcReader.Read(buf)
		if err != nil {
			return fmt.Errorf("read source: %v", err)
		}

		if n > 0 {
			// Ghi sang Dest (có timeout ghi)
			dstConn.SetWriteDeadline(time.Now().Add(DialTimeout))
			_, err := dstConn.Write(buf[:n])
			if err != nil {
				return fmt.Errorf("write dest: %v", err)
			}
			dstConn.SetWriteDeadline(time.Time{}) // Xóa timeout

			// Cập nhật thống kê (Atomic để an toàn thread)
			atomic.AddInt64(&w.status.BytesForwarded, int64(n))
			
			// Cập nhật timestamp nhận data (để GGA biết fix quality)
			atomic.StoreInt64(&w.lastDataTime, time.Now().Unix())
		}

		// Kiểm tra lỗi từ các luồng phụ
		select {
		case err := <-errChan:
			return err
		case <-w.ctx.Done():
			return context.Canceled
		default:
			// Không có lỗi, tiếp tục vòng lặp
		}
	}
}

// ================= HELPERS =================

func checkResponse(reader *bufio.Reader, conn net.Conn) error {
	// Timeout cho việc đọc header response
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	line, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return fmt.Errorf("server closed immediately (EOF) - Check credentials/mountpoint")
		}
		return err
	}

	// Đọc xả hết các dòng header còn lại (đến khi gặp dòng trắng)
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if l == "\r\n" || l == "\n" {
			break
		}
	}

	// Kiểm tra mã phản hồi
	if strings.Contains(line, "200 OK") || strings.Contains(line, "ICY 200") {
		return nil
	}

	// Server báo lỗi (401, 404, 403...)
	return fmt.Errorf("rejected: %s", strings.TrimSpace(line))
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

func generateNMEA(lat, lon float64, hasData bool) string {
	now := time.Now().UTC()
	latStr := toDegMinDir(lat, true)
	lonStr := toDegMinDir(lon, false)

	// Xác định fix quality: 4=RTK Fixed, 1=GPS Single
	fixQuality := 1
	if hasData {
		fixQuality = 4
	}

	// GPGGA format: $GPGGA,hhmmss.ss,lat,dir,lon,dir,fix,sats,hdop,alt,M,sep,M,,*cs
	raw := fmt.Sprintf("GPGGA,%02d%02d%02d.00,%s,%s,%d,10,1.0,100.0,M,-5.0,M,,",
		now.Hour(), now.Minute(), now.Second(), latStr, lonStr, fixQuality)

	var checksum byte
	for i := 0; i < len(raw); i++ {
		checksum ^= raw[i]
	}
	return fmt.Sprintf("$%s*%02X\r\n", raw, checksum)
}

func toDegMinDir(val float64, isLat bool) string {
	absVal := math.Abs(val)
	deg := int(absVal)
	min := (absVal - float64(deg)) * 60.0

	if isLat {
		dir := "N"
		if val < 0 {
			dir = "S"
		}
		return fmt.Sprintf("%02d%07.4f,%s", deg, min, dir)
	}
	dir := "E"
	if val < 0 {
		dir = "W"
	}
	return fmt.Sprintf("%03d%07.4f,%s", deg, min, dir)
}

func getMD5Hash(c ConfigStation) string {
	data, _ := json.Marshal(c)
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:])
}

// ================= WEB MONITOR =================
func startMonitorServer() {
	// Giao diện Web
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, htmlContent)
	})

	// API JSON
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		manager.mu.RLock()
		defer manager.mu.RUnlock()

		stats := make([]StationStatus, 0, len(manager.workers))
		for _, worker := range manager.workers {
			s := *worker.status
			s.Uptime = time.Since(worker.status.StartTime).Round(time.Second).String()
			stats = append(stats, s)
		}

		// Sắp xếp
		for i := 0; i < len(stats)-1; i++ {
			for j := i + 1; j < len(stats); j++ {
				if stats[i].Order > stats[j].Order {
					stats[i], stats[j] = stats[j], stats[i]
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})

	log.Printf("Monitor Interface: http://localhost%s", MonitorPort)
	log.Fatal(http.ListenAndServe(MonitorPort, nil))
}

// HTML Dashboard (Nhẹ & Hiện đại)
const htmlContent = `<!DOCTYPE html>
<html>
<head>
	<title>NTRIP Relay Ultimate</title>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>
		body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; margin: 0; padding: 20px; background: #f0f2f5; color: #333; }
		.container { max-width: 1200px; margin: 0 auto; }
		h1 { color: #1a73e8; margin-bottom: 20px; border-bottom: 2px solid #e1e4e8; padding-bottom: 10px; }
		.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(350px, 1fr)); gap: 15px; }
		.card { background: white; border-radius: 8px; padding: 15px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); border-left: 5px solid #ddd; transition: transform 0.2s; }
		.card:hover { transform: translateY(-2px); box-shadow: 0 4px 6px rgba(0,0,0,0.1); }
		
		.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; }
		.id { font-weight: bold; font-size: 1.1em; color: #2c3e50; }
		.badge { padding: 4px 10px; border-radius: 12px; font-size: 0.85em; font-weight: 600; color: white; }
		
		.Running { background-color: #34d399; border-left-color: #34d399; }
		.Connecting { background-color: #f59e0b; border-left-color: #f59e0b; }
		.Error { background-color: #ef4444; border-left-color: #ef4444; }
		.Stopped { background-color: #9ca3af; border-left-color: #9ca3af; }
		.Starting { background-color: #60a5fa; border-left-color: #60a5fa; }

		.stat-row { display: flex; justify-content: space-between; margin: 5px 0; font-size: 0.9em; }
		.label { color: #6b7280; }
		.val { font-family: monospace; font-weight: 600; }
		.msg { font-size: 0.8em; color: #ef4444; margin-top: 8px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
		
		.footer { margin-top: 30px; text-align: center; font-size: 0.8em; color: #9ca3af; }
	</style>
</head>
<body>
	<div class="container">
		<h1>🛰️ NTRIP Relay Monitor</h1>
		<div id="grid" class="grid">Loading...</div>
		<div class="footer">Auto-refreshing every 2s • System Ready</div>
	</div>

	<script>
		function formatBytes(bytes) {
			if (bytes === 0) return '0 B';
			const k = 1024;
			const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
			const i = Math.floor(Math.log(bytes) / Math.log(k));
			return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
		}

		function update() {
			fetch('/status')
			.then(r => r.json())
			.then(data => {
				const grid = document.getElementById('grid');
				if(!data || data.length === 0) {
					grid.innerHTML = '<div style="grid-column: 1/-1; text-align: center; padding: 20px;">No stations configured</div>';
					return;
				}
				
				grid.innerHTML = data.map(s => {
					const statusClass = s.status.split(' ')[0]; // Lấy từ đầu tiên
					let html = '<div class="card ' + statusClass + '" style="border-left-color: var(--' + statusClass + ')">' +
						'<div class="header">' +
							'<span class="id">' + s.id + '</span>' +
							'<span class="badge ' + statusClass + '">' + s.status + '</span>' +
						'</div>' +
						'<div class="stat-row">' +
							'<span class="label">Uptime:</span>' +
							'<span class="val">' + s.uptime + '</span>' +
						'</div>' +
						'<div class="stat-row">' +
							'<span class="label">Data:</span>' +
							'<span class="val">' + formatBytes(s.bytes_forwarded) + '</span>' +
						'</div>';
					if (s.last_message) {
						html += '<div class="msg" title="' + s.last_message + '">⚠️ ' + s.last_message + '</div>';
					}
					html += '</div>';
					return html;
				}).join('');
			})
			.catch(e => console.error(e));
		}
		
		update();
		setInterval(update, 2000);
	</script>
</body>
</html>`
