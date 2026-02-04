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

// ================= CONSTANTS =================
const (
	ConfigFile       = "config.json"
	MonitorPort      = ":8081"
	ReconnectDelay   = 5 * time.Second  // Thời gian chờ khi lỗi thường
	BlockWaitDelay   = 30 * time.Second // Thời gian chờ khi bị Server chặn (EOF)
	SendNMEAInterval = 10 * time.Second
	ReadTimeout      = 15 * time.Second
	DialTimeout      = 10 * time.Second
)

// ================= STRUCTS =================
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
	cfg        ConfigStation
	ctx        context.Context
	cancel     context.CancelFunc
	status     *StationStatus
	configHash string
	order      int
}

type StationManager struct {
	mu      sync.RWMutex
	workers map[string]*Worker
}

var manager = &StationManager{
	workers: make(map[string]*Worker),
}

// ================= MAIN =================
func main() {
	log.Println("=== NTRIP RELAY SYSTEM STARTED (FIXED) ===")

	go startMonitorServer()
	reloadConfig()

	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		reloadConfig()
	}
}

// ================= MANAGER LOGIC =================
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

	for i, cfg := range configs {
		activeIDs[cfg.ID] = true
		hash := getMD5Hash(cfg)

		worker, exists := manager.workers[cfg.ID]
		if exists {
			// Cập nhật order ngay cả khi worker đã tồn tại
			worker.order = i
			worker.status.Order = i

			if worker.configHash != hash || !cfg.Enable {
				log.Printf("[%s] Config changed. Restarting...", cfg.ID)
				worker.cancel()
				delete(manager.workers, cfg.ID)
				exists = false
			}
		}

		if !exists && cfg.Enable {
			ctx, cancel := context.WithCancel(context.Background())
			status := &StationStatus{ID: cfg.ID, Status: "Starting", Order: i}
			w := &Worker{cfg: cfg, ctx: ctx, cancel: cancel, status: status, configHash: hash, order: i}
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

// ================= WORKER LOGIC =================
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

				// Logic xử lý lỗi thông minh hơn
				delay := ReconnectDelay
				if strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "reset by peer") {
					// Nếu bị server dập kết nối, chờ lâu hơn để tránh bị ban IP vĩnh viễn
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

	// 1. SOURCE CONNECTION
	srcConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", w.cfg.SrcHost, w.cfg.SrcPort), DialTimeout)
	if err != nil {
		return fmt.Errorf("connect source: %w", err)
	}
	defer srcConn.Close()

	// Header GET
	authSrc := basicAuth(w.cfg.SrcUser, w.cfg.SrcPass)
	reqSrc := fmt.Sprintf(
		"GET /%s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Ntrip-Version: Ntrip/2.0\r\n"+
			"User-Agent: NTRIP GoRelay/2.0\r\n"+
			"Authorization: Basic %s\r\n"+
			"Connection: close\r\n\r\n",
		w.cfg.SrcMount, w.cfg.SrcHost, authSrc)

	if _, err := srcConn.Write([]byte(reqSrc)); err != nil {
		return err
	}

	if err := checkResponse(srcConn); err != nil {
		return fmt.Errorf("source auth: %w", err)
	}

	srcConn.Write([]byte(generateNMEA(w.cfg.Lat, w.cfg.Lon)))

	// 2. DEST CONNECTION
	w.status.Status = "Connecting Dest"
	dstConn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", w.cfg.DstHost, w.cfg.DstPort), DialTimeout)
	if err != nil {
		return fmt.Errorf("connect dest: %w", err)
	}
	defer dstConn.Close()

	// Header POST
	authDst := basicAuth(w.cfg.DstUser, w.cfg.DstPass)
	reqDstCompatible := fmt.Sprintf(
		"POST /%s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Ntrip-Version: Ntrip/2.0\r\n"+
			"User-Agent: NTRIP GoRelay/2.0\r\n"+
			"Authorization: Basic %s\r\n"+
			"Content-Type: application/octet-stream\r\n"+
			"Connection: close\r\n\r\n",
		w.cfg.DstMount, w.cfg.DstHost, authDst)

	if _, err := dstConn.Write([]byte(reqDstCompatible)); err != nil {
		return err
	}

	if err := checkResponse(dstConn); err != nil {
		return fmt.Errorf("dest auth: %w", err)
	}

	w.status.Status = "Running"
	w.status.LastMessage = "Streaming OK"
	log.Printf("[%s] RELAYING: %s -> %s", w.cfg.ID, w.cfg.SrcMount, w.cfg.DstMount)

	// 3. STREAMING LOOP
	errChan := make(chan error, 2)

	// NMEA Heartbeat
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

	// --- ĐOẠN SỬA QUAN TRỌNG Ở ĐÂY ---
	// Read Dest: Bỏ timeout, chỉ đợi khi nào connection thực sự bị đóng (EOF/Error)
	go func() {
		buf := make([]byte, 1024)
		for {
			// SỬA: Xóa dòng dstConn.SetReadDeadline(...)
			_, err := dstConn.Read(buf)
			if err != nil {
				// Chỉ báo lỗi nếu kết nối thực sự đứt hoặc bị đóng
				errChan <- fmt.Errorf("dest connection closed: %v", err)
				return
			}
			// Nếu server có gửi gì về (hiếm), ta cứ lờ đi và đọc tiếp
		}
	}()
	// ----------------------------------

	// Main Loop: Src -> Dest
	buf := make([]byte, 8192)
	for {
		// Vẫn giữ Timeout cho Source (nếu Source im lặng 15s nghĩa là mất data -> reset)
		srcConn.SetReadDeadline(time.Now().Add(ReadTimeout))
		n, err := srcConn.Read(buf)
		if err != nil {
			return fmt.Errorf("read source: %v", err)
		}

		if n > 0 {
			// Ghi sang Dest (đặt timeout ghi để tránh treo nếu mạng lag)
			dstConn.SetWriteDeadline(time.Now().Add(DialTimeout))
			_, err := dstConn.Write(buf[:n])
			if err != nil {
				return fmt.Errorf("write dest: %v", err)
			}
			// Reset lại timeout ghi sau khi ghi xong
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

// ================= HELPERS =================
func checkResponse(conn net.Conn) error {
	reader := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	line, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return fmt.Errorf("server closed connection immediately (EOF)")
		}
		return err
	}

	// Đọc hết header còn lại
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if l == "\r\n" || l == "\n" {
			break
		}
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
		if val >= 0 {
			dir = "N"
		} else {
			dir = "S"
		}
		return fmt.Sprintf("%02d%07.4f,%s", deg, min, dir)
	} else {
		if val >= 0 {
			dir = "E"
		} else {
			dir = "W"
		}
		return fmt.Sprintf("%03d%07.4f,%s", deg, min, dir)
	}
}

func getMD5Hash(c ConfigStation) string {
	data, _ := json.Marshal(c)
	hash := md5.Sum(data)
	return hex.EncodeToString(hash[:])
}

func startMonitorServer() {
	// HTML Dashboard
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := `<!DOCTYPE html>
<html>
<head>
	<title>NTRIP Relay Monitor</title>
	<meta charset="utf-8">
	<style>
		body { font-family: Arial, sans-serif; margin: 20px; background: #f5f5f5; }
		h1 { color: #333; }
		.station { background: white; padding: 15px; margin: 10px 0; border-radius: 5px; box-shadow: 0 2px 4px rgba(0,0,0,0.1); }
		.status { display: inline-block; padding: 5px 10px; border-radius: 3px; color: white; font-weight: bold; }
		.Running { background: #4CAF50; }
		.Error { background: #f44336; }
		.Connecting { background: #FF9800; }
		.Stopped { background: #9E9E9E; }
		.stat { margin: 5px 0; }
		.label { font-weight: bold; color: #666; }
		.value { color: #333; }
		.updated { text-align: right; color: #999; font-size: 12px; margin-top: 10px; }
	</style>
</head>
<body>
	<h1>🛰️ NTRIP Relay Monitor</h1>
	<div id="stations"></div>
	<div class="updated">Last updated: <span id="lastUpdate">-</span></div>
	<script>
		function updateStatus() {
			fetch('/status')
				.then(res => res.json())
				.then(data => {
					const container = document.getElementById('stations');
					if (!data || data.length === 0) {
						container.innerHTML = '<p>No stations configured</p>';
						return;
					}
					container.innerHTML = data.map(s => ` + "`" + `
						<div class="station">
							<h3>${s.id}</h3>
							<div class="stat"><span class="status ${s.status}">${s.status}</span></div>
							<div class="stat"><span class="label">Uptime:</span> <span class="value">${s.uptime}</span></div>
							<div class="stat"><span class="label">Data:</span> <span class="value">${formatBytes(s.bytes_forwarded)}</span></div>
							<div class="stat"><span class="label">Message:</span> <span class="value">${s.last_message}</span></div>
						</div>
					` + "`" + `).join('');
					document.getElementById('lastUpdate').textContent = new Date().toLocaleTimeString();
				})
				.catch(err => {
					document.getElementById('stations').innerHTML = '<p style="color:red">Error loading data</p>';
				});
		}
		function formatBytes(bytes) {
			if (bytes === 0) return '0 B';
			const k = 1024;
			const sizes = ['B', 'KB', 'MB', 'GB'];
			const i = Math.floor(Math.log(bytes) / Math.log(k));
			return Math.round(bytes / Math.pow(k, i) * 100) / 100 + ' ' + sizes[i];
		}
		updateStatus();
		setInterval(updateStatus, 2000); // Auto refresh every 2 seconds
	</script>
</body>
</html>`
		fmt.Fprint(w, html)
	})

	// JSON API endpoint
	http.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		manager.mu.RLock()
		defer manager.mu.RUnlock()
		var stats []StationStatus
		for _, worker := range manager.workers {
			s := *worker.status
			s.Uptime = time.Since(worker.status.StartTime).Round(time.Second).String()
			stats = append(stats, s)
		}

		// Sắp xếp theo thứ tự trong config
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

	log.Printf("Monitor server starting on http://localhost%s", MonitorPort)
	log.Fatal(http.ListenAndServe(MonitorPort, nil))
}
