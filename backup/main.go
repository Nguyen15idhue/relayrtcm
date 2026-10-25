package main

import (
	"bufio"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/proxy"
)

// ================= CẤU HÌNH HỆ THỐNG =================
const (
	ConfigFile  = "config.json"
	MonitorPort = ":8081"

	// Timeout & Interval - OPTIMIZED FOR STABLE LONG-RUNNING CONNECTIONS
	NormalRetryDelay     = 5 * time.Second  // Retry delay cho lỗi bình thường
	BlockRetryDelay      = 30 * time.Second // Chờ khi bị Server đá (EOF/Auth fail)
	ShortSessionDelay    = 20 * time.Second // Chờ lâu hơn nếu session < 60s (tránh retry loop)
	ReadTimeout          = 90 * time.Second // Giảm xuống 90s (phát hiện dead connection nhanh hơn)
	DialTimeout          = 30 * time.Second // Tăng lên 30s (VPS có thể lag)
	ProxyDialTimeout     = 10 * time.Second // Timeout riêng cho proxy dial (fast fail)
	SendNMEAInterval     = 8 * time.Second  // Gửi NMEA mỗi 8s (thường xuyên hơn để keep-alive)
	TCPKeepAlive         = 30 * time.Second // TCP keepalive để giữ connection
	MaxRetryBackoff      = 60 * time.Second // Max delay khi retry
	MinStableSessionTime = 60 * time.Second // Session phải chạy > 60s mới coi là stable

	// Buffer Size: 32KB là tối ưu cho luồng TCP
	BufferSize = 32 * 1024
)

// GGA Fix Quality codes (chuẩn NMEA 0183)
const (
	FixSingle   = 1 // GPS fix (Single)
	FixDGPS     = 2 // DGPS / RTD (Real-Time Differential)
	FixRTKFixed = 4 // RTK Fixed
	FixRTKFloat = 5 // RTK Float
)

// Fake position drift (di chuyển mượt quanh vị trí gốc)
const (
	PosMaxRadiusKm  = 10.0 // Bán kính tối đa quanh vị trí gốc (km)
	PosMoveRadiusKm = 2.0  // Quãng đường tối đa mỗi chuyến di chuyển (km)
	PosMinSpeedMps  = 1.0  // Tốc độ di chuyển nhỏ nhất (m/s)
	PosMaxSpeedMps  = 4.0  // Tốc độ di chuyển lớn nhất (m/s)
	PosArriveMeters = 40.0 // Khoảng cách coi như đã tới waypoint (m)
	PosIdleMinSec   = 60   // Thời gian đứng yên tối thiểu giữa 2 lần di chuyển (giây)
	PosIdleMaxSec   = 300  // Thời gian đứng yên tối đa giữa 2 lần di chuyển (giây)
)

// ================= MEMORY POOL (Tối ưu RAM) =================
var bufPool = sync.Pool{
	New: func() interface{} {
		// Cấp phát mảng byte một lần, tái sử dụng mãi mãi
		b := make([]byte, BufferSize)
		return &b
	},
}

// ================= DEVICE PROFILES (Ngụy trang như Rover thực) =================
type DeviceProfile struct {
	DeviceName      string // Tên thiết bị (không bao gồm version)
	VersionTemplate string // Template cho version (dùng để generate random)
	NtripVersion    string
	Connection      string
	NMEAInterval    time.Duration
	NMEAJitter      time.Duration
	HDOPRange       [2]float64 // Min, Max
	SatsRange       [2]int     // Min, Max
	SendGSA         bool
	SendRMC         bool
	InitialDelay    time.Duration
}

// Database các thiết bị GNSS thực tế trên thị trường (format chuẩn)
var deviceProfiles = []DeviceProfile{
	{"GNSSInternetRadio", "major.minor.patch", "Ntrip/2.0", "close", 10 * time.Second, 2 * time.Second, [2]float64{0.8, 1.2}, [2]int{10, 14}, true, false, 0},
	{"EFIX eField", "major.minor.patch.date", "Ntrip/2.0", "close", 12 * time.Second, 3 * time.Second, [2]float64{0.7, 1.0}, [2]int{12, 16}, true, true, 1 * time.Second},
	{"CHC LandStar", "major.minor.patch.date", "Ntrip/2.0", "close", 11 * time.Second, 2 * time.Second, [2]float64{0.9, 1.3}, [2]int{9, 13}, false, true, 0},
	{"CHC i83", "major.minor.patch", "Ntrip/2.0", "close", 15 * time.Second, 4 * time.Second, [2]float64{0.8, 1.4}, [2]int{10, 15}, true, false, 2 * time.Second},
	{"COMNAV T300", "major.minor.patch", "Ntrip/2.0", "close", 9 * time.Second, 1 * time.Second, [2]float64{0.7, 1.1}, [2]int{11, 15}, false, false, 1 * time.Second},
	{"Trimble DA2", "major.minor", "Ntrip/1.0", "keep-alive", 10 * time.Second, 2 * time.Second, [2]float64{0.6, 0.9}, [2]int{12, 18}, true, true, 0},
	{"Hi-Target V90", "major.minor.patch", "Ntrip/2.0", "close", 13 * time.Second, 3 * time.Second, [2]float64{0.8, 1.2}, [2]int{10, 14}, false, true, 1 * time.Second},
	{"South S82T", "major.minor.patch", "Ntrip/2.0", "close", 11 * time.Second, 2 * time.Second, [2]float64{0.9, 1.3}, [2]int{9, 13}, true, false, 0},
	{"Stonex S900A", "major.minor.patch", "Ntrip/2.0", "close", 14 * time.Second, 3 * time.Second, [2]float64{0.8, 1.4}, [2]int{10, 14}, false, false, 2 * time.Second},
	{"UniStrong G970", "major.minor.patch", "Ntrip/2.0", "close", 10 * time.Second, 2 * time.Second, [2]float64{0.7, 1.1}, [2]int{11, 15}, true, true, 1 * time.Second},
	{"Emlid ReachRS2", "vmajor.minor.patch", "Ntrip/2.0", "close", 12 * time.Second, 2 * time.Second, [2]float64{0.8, 1.1}, [2]int{11, 14}, false, false, 1 * time.Second},
	{"Leica GS18", "major.minor.patch", "Ntrip/2.0", "keep-alive", 11 * time.Second, 3 * time.Second, [2]float64{0.6, 0.9}, [2]int{13, 18}, true, true, 0},
}

// ================= DATA STRUCTURES =================
type ConfigStation struct {
	ID        string  `json:"id"`
	Enable    bool    `json:"enable"`
	SrcHost   string  `json:"src_host"`
	SrcPort   int     `json:"src_port"`
	SrcMount  string  `json:"src_mount"`
	SrcUser   string  `json:"src_user"`
	SrcPass   string  `json:"src_pass"`
	SrcProxy  string  `json:"src_proxy"`   // SOCKS5 proxy cho source (VD: "socks5://127.0.0.1:1080")
	SrcUseSSL bool    `json:"src_use_ssl"` // Kết nối SSL/TLS tới source
	DstHost   string  `json:"dst_host"`
	DstPort   int     `json:"dst_port"`
	DstMount  string  `json:"dst_mount"`
	DstUser   string  `json:"dst_user"`
	DstPass   string  `json:"dst_pass"`
	DstProxy  string  `json:"dst_proxy"`   // SOCKS5 proxy cho destination
	DstUseSSL bool    `json:"dst_use_ssl"` // Kết nối SSL/TLS tới destination
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	// UserAgentPrefix: tiền tố User-Agent gửi tới caster (VD: "NTRIP").
	// Rỗng = giữ nguyên User-Agent mặc định của device profile.
	UserAgentPrefix string `json:"user_agent_prefix,omitempty"`
	// NMEAIntervalSec: 0 = auto theo device profile, >0 = chu kỳ gửi GGA/NMEA (giây)
	NMEAIntervalSec int `json:"nmea_interval_sec,omitempty"`
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

// StationDetailResponse là dữ liệu trả về cho popup View.
type StationDetailResponse struct {
	ID                string           `json:"id"`
	Enabled           bool             `json:"enabled"`
	Status            string           `json:"status"`
	Uptime            string           `json:"uptime"`
	ConnectedAt       *time.Time       `json:"connected_at,omitempty"`
	CurrentSessionSec float64          `json:"current_session_sec"`
	BytesForwarded    int64            `json:"bytes_forwarded"`
	LastDataSec       float64          `json:"last_data_sec"`
	RetryCount        int              `json:"retry_count"`
	TotalSessions     int              `json:"total_sessions"`
	OnlinePercent     float64          `json:"online_percent"`
	CurrentFix        string           `json:"current_fix"`
	Sats              int              `json:"sats"`
	HDOP              float64          `json:"hdop"`
	Window            string           `json:"window"`
	Source            string           `json:"source"`
	Destination       string           `json:"destination"`
	Lat               float64          `json:"lat"`
	Lon               float64          `json:"lon"`
	EventCounts       EventCounts      `json:"event_counts"`
	Timeline          []TimelinePoint  `json:"timeline"`
	Events            []HistoryEvent   `json:"events"`
	Sessions          []HistorySession `json:"sessions"`
}

type Worker struct {
	cfg          ConfigStation
	ctx          context.Context
	cancel       context.CancelFunc
	status       *StationStatus
	statusMu     sync.RWMutex // Bảo vệ các field string trong status (Status, LastMessage, StartTime)
	configHash   string
	wg           sync.WaitGroup // Đợi các goroutine con dọn dẹp xong
	lastDataTime int64          // Unix timestamp lần nhận data cuối (atomic)
	// Anti-detection: Mỗi worker có device profile riêng
	device    DeviceProfile
	userAgent string     // User-Agent đầy đủ (device + version)
	rand      *rand.Rand // Random generator riêng cho mỗi worker
	randMu    sync.Mutex // Bảo vệ w.rand (không thread-safe)
	hdop      float64    // HDOP cố định cho worker này
	sats      int        // Số vệ tinh cố định
	// Fake fix quality (mô phỏng RTK Fixed/Float/DGPS theo thời gian ngẫu nhiên)
	fixMu         sync.Mutex
	fixQuality    int       // Trạng thái fix hiện tại (0 = chưa khởi tạo)
	fixNextChange time.Time // Thời điểm đổi trạng thái fix kế tiếp
	// Fake position (di chuyển mượt quanh vị trí gốc, tối đa PosMaxRadiusKm)
	posMu          sync.Mutex
	curLat         float64   // Vĩ độ giả lập hiện tại
	curLon         float64   // Kinh độ giả lập hiện tại
	targetLat      float64   // Waypoint đang hướng tới
	targetLon      float64   // Waypoint đang hướng tới
	posInit        bool      // Đã khởi tạo vị trí giả lập chưa
	moving         bool      // Đang di chuyển hay đang đứng yên
	idleUntil      time.Time // Đứng yên tới thời điểm này rồi mới di chuyển
	posLastUpdate  time.Time // Lần cập nhật vị trí cuối (tính quãng đường theo dt)
	speedMps       float64   // Tốc độ di chuyển (m/s)
	lastSpeedKnots float64   // Tốc độ hiện tại cho GPRMC (knots)
	lastCourseDeg  float64   // Hướng di chuyển hiện tại cho GPRMC (độ)
	// Retry optimization
	retryCount  int       // Số lần retry liên tiếp
	lastSuccess time.Time // Lần kết nối thành công cuối
	// Lịch sử SQLite (nil nếu DB lỗi/không mở được)
	history   *HistoryStore
	sessionID int64 // ID session đang mở trong DB (chỉ dùng trong goroutine Start)
}

// ================= STATUS ACCESSORS (Thread-safe) =================
func (w *Worker) setStatus(s string) {
	w.statusMu.Lock()
	w.status.Status = s
	w.statusMu.Unlock()
}

func (w *Worker) setStatusMsg(s, msg string) {
	w.statusMu.Lock()
	w.status.Status = s
	w.status.LastMessage = msg
	w.statusMu.Unlock()
}

// snapshot trả về bản copy an toàn của status để đọc (tránh data race)
func (w *Worker) snapshot() StationStatus {
	w.statusMu.RLock()
	defer w.statusMu.RUnlock()
	return StationStatus{
		ID:             w.status.ID,
		Status:         w.status.Status,
		BytesForwarded: atomic.LoadInt64(&w.status.BytesForwarded),
		LastMessage:    w.status.LastMessage,
		StartTime:      w.status.StartTime,
	}
}

// ================= RAND ACCESSORS (Thread-safe) =================
// math/rand.Rand KHÔNG an toàn khi dùng đồng thời từ nhiều goroutine.
func (w *Worker) randIntn(n int) int {
	w.randMu.Lock()
	defer w.randMu.Unlock()
	return w.rand.Intn(n)
}

func (w *Worker) randInt63n(n int64) int64 {
	w.randMu.Lock()
	defer w.randMu.Unlock()
	return w.rand.Int63n(n)
}

func (w *Worker) randFloat64() float64 {
	w.randMu.Lock()
	defer w.randMu.Unlock()
	return w.rand.Float64()
}

type StationManager struct {
	mu          sync.RWMutex
	workers     map[string]*Worker
	configs     []ConfigStation // Cache config trong RAM (bảo vệ bởi mu)
	lastModTime time.Time
}

var manager = &StationManager{
	workers: make(map[string]*Worker),
}

// historyStore là nil nếu SQLite không mở được; khi đó relay vẫn chạy bình thường.
var historyStore *HistoryStore

// ================= MAIN ENTRY =================
func main() {
	// Cấu hình log hiển thị thời gian và dòng code lỗi
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Println("=== NTRIP RELAY SYSTEM (ULTIMATE STABILITY) ===")

	// Mở lịch sử SQLite trước khi khởi động worker. Lỗi DB không làm dừng relay.
	if store, err := OpenHistoryStore("history.db"); err != nil {
		log.Printf("[History] Cannot open history.db: %v (history disabled)", err)
	} else {
		historyStore = store
		go startHistoryBackground(store)
		log.Printf("[History] SQLite ready: %s", store.path)
	}

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

// startHistoryBackground chạy sampler online mỗi phút và prune mỗi giờ.
func startHistoryBackground(store *HistoryStore) {
	go func() {
		sampleTicker := time.NewTicker(historySampleInterval)
		pruneTicker := time.NewTicker(historyPruneInterval)
		defer sampleTicker.Stop()
		defer pruneTicker.Stop()
		for {
			select {
			case <-sampleTicker.C:
				if err := store.SampleOnline(); err != nil {
					log.Printf("[History] Sample online failed: %v", err)
				}
			case <-pruneTicker.C:
				if err := store.Prune(); err != nil {
					log.Printf("[History] Prune failed: %v", err)
				}
			}
		}
	}()
}

// ================= CONFIG FILE I/O (Thread-safe + Atomic) =================
// configFileMu bảo vệ việc đọc/ghi file config giữa các goroutine.
// Chỉ giữ khóa quanh thao tác file, KHÔNG lồng với manager.mu.
var configFileMu sync.Mutex

// readConfigFile đọc + parse config.json an toàn.
func readConfigFile() ([]ConfigStation, error) {
	configFileMu.Lock()
	defer configFileMu.Unlock()

	file, err := os.ReadFile(ConfigFile)
	if err != nil {
		return nil, err
	}

	var configs []ConfigStation
	if err := json.Unmarshal(file, &configs); err != nil {
		return nil, err
	}
	return configs, nil
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
	configs, err := readConfigFile()
	if err != nil {
		log.Printf("[System] Read config failed: %v", err)
		return
	}

	// 3. Cập nhật Workers
	manager.mu.Lock()
	defer manager.mu.Unlock()

	// Cập nhật cache config cho /status (tránh đọc file mỗi request)
	manager.configs = configs

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
				if historyStore != nil {
					msg := "Configuration changed - restarting"
					if !cfg.Enable {
						msg = "Station disabled"
					}
					if err := historyStore.AddEvent(cfg.ID, "config", msg, 0); err != nil {
						log.Printf("[%s] History config event failed: %v", cfg.ID, err)
					}
				}
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

			// Chọn device profile dựa trên hash ID (deterministic nhưng unique)
			idHash := getMD5Hash(cfg)[:8]
			profileIdx := 0
			for _, b := range idHash {
				profileIdx += int(b)
			}
			profileIdx = profileIdx % len(deviceProfiles)
			device := deviceProfiles[profileIdx]

			// Tạo random generator riêng cho worker (seed từ ID)
			seed := int64(0)
			for j, b := range idHash {
				seed += int64(b) << (j * 8)
			}
			rng := rand.New(rand.NewSource(seed))

			// Random HDOP và số vệ tinh trong range của device
			hdop := device.HDOPRange[0] + rng.Float64()*(device.HDOPRange[1]-device.HDOPRange[0])
			sats := device.SatsRange[0] + rng.Intn(device.SatsRange[1]-device.SatsRange[0]+1)

			// Generate random version dựa trên template, sau đó ghép tiền tố cấu hình
			userAgent := buildUserAgent(cfg.UserAgentPrefix, generateUserAgent(device, rng))

			w := &Worker{
				cfg:        cfg,
				ctx:        ctx,
				cancel:     cancel,
				status:     status,
				configHash: hash,
				device:     device,
				userAgent:  userAgent,
				rand:       rng,
				hdop:       hdop,
				sats:       sats,
				history:    historyStore,
			}
			manager.workers[cfg.ID] = w
			if historyStore != nil {
				if err := historyStore.AddEvent(cfg.ID, "config", "Worker initialized", 0); err != nil {
					log.Printf("[%s] History init event failed: %v", cfg.ID, err)
				}
			}
			go w.Start() // Chạy vòng lặp chính
			log.Printf("[%s] Worker initialized (Device: %s, HDOP: %.2f, Sats: %d, NMEA interval: %v)", cfg.ID, userAgent, hdop, sats, w.nmeaInterval())
		}
	}

	// Xóa các worker bị xóa khỏi config
	for id, worker := range manager.workers {
		if !activeIDs[id] {
			log.Printf("[%s] Removed from config. Stopping...", id)
			worker.cancel()
			worker.wg.Wait()
			delete(manager.workers, id)
			if historyStore != nil {
				if err := historyStore.DeleteStation(id); err != nil {
					log.Printf("[%s] Delete history failed: %v", id, err)
				}
			}
		}
	}
}

// ================= HELPER: ERROR CLASSIFICATION =================
// Phân loại lỗi để quyết định retry strategy
func isTemporaryError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()

	// Network temporary errors - retry nhanh
	temporaryKeywords := []string{
		"timeout",
		"connection reset",
		"connection refused",
		"no route to host",
		"network is unreachable",
		"temporary failure",
		"dial tcp",
		"i/o timeout",
	}

	for _, keyword := range temporaryKeywords {
		if strings.Contains(strings.ToLower(errStr), keyword) {
			return true
		}
	}

	return false
}

func isPermanentError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()

	// Server block/auth errors - retry chậm
	permanentKeywords := []string{
		"401",
		"403",
		"404",
		"rejected",
		"unauthorized",
		"forbidden",
		"authentication failed",
		"eof", // Server đóng connection sớm
		"forcibly closed",
	}

	for _, keyword := range permanentKeywords {
		if strings.Contains(strings.ToLower(errStr), keyword) {
			return true
		}
	}

	return false
}

// ================= WORKER CORE LOGIC =================
func (w *Worker) Start() {
	w.wg.Add(1)
	defer w.wg.Done()

	w.statusMu.Lock()
	w.status.StartTime = time.Now()
	w.statusMu.Unlock()

	// Random delay trước khi connect lần đầu (tránh tất cả connect cùng lúc)
	if w.device.InitialDelay > 0 {
		initDelay := w.device.InitialDelay + time.Duration(w.randIntn(3000))*time.Millisecond
		w.setStatus(fmt.Sprintf("Waiting %.1fs", initDelay.Seconds()))
		select {
		case <-time.After(initDelay):
		case <-w.ctx.Done():
			w.setStatus("Stopped")
			return
		}
	}

	// Vòng lặp vĩnh cửu (cho đến khi config tắt)
	for {
		// Kiểm tra xem có lệnh dừng không
		select {
		case <-w.ctx.Done():
			w.setStatus("Stopped")
			return
		default:
		}

		sessionStart := time.Now()

		// --- BẮT ĐẦU PHIÊN LÀM VIỆC ---
		err := w.runSession()
		// -----------------------------

		// Tính thời gian phiên vừa chạy
		runDuration := time.Since(sessionStart)

		// Worker bị dừng do config/disable: không coi là lỗi.
		if w.ctx.Err() != nil {
			w.historyEndSession("stopped", "configuration changed or relay stopped")
			w.historyAddEvent("stopped", "Worker stopped: configuration changed or relay stopped", 0)
			w.setStatus("Stopped")
			return
		}

		if w.sessionID != 0 {
			result := "success"
			message := ""
			if err != nil {
				result = "error"
				message = err.Error()
			}
			w.historyEndSession(result, message)
		}

		if err != nil {
			// Logic xử lý lỗi thông minh
			lastMsg := err.Error()
			w.historyAddEvent(classifyHistoryEvent(err), err.Error(), runDuration.Seconds())

			delay := NormalRetryDelay

			// Phân loại lỗi để quyết định retry strategy
			if isPermanentError(err) {
				// Lỗi nghiêm trọng (auth failed, 403, etc) → Chờ lâu
				delay = BlockRetryDelay
				lastMsg += " (Server Block - Wait 30s)"
				w.retryCount++
			} else if runDuration < MinStableSessionTime {
				// Session quá ngắn (< 60s) → Có vấn đề → Chờ lâu hơn để tránh retry loop
				delay = ShortSessionDelay
				lastMsg += fmt.Sprintf(" (Unstable - Session %.0fs < 60s - Wait 20s)", runDuration.Seconds())
				w.retryCount++
				log.Printf("[%s] ⚠️  Short session detected: %.1fs (expected >60s). Possible: bad credentials, mount not found, or network issue.", w.cfg.ID, runDuration.Seconds())
			} else if isTemporaryError(err) {
				// Lỗi tạm thời (network timeout) → Retry với exponential backoff
				w.retryCount++
				if w.retryCount > 1 {
					// 2s -> 4s -> 8s -> 16s -> max 60s (fast recovery)
					backoff := time.Duration(math.Min(
						float64(NormalRetryDelay.Seconds()*math.Pow(2, float64(w.retryCount-1))),
						float64(MaxRetryBackoff.Seconds()),
					)) * time.Second
					delay = backoff
					lastMsg += fmt.Sprintf(" (Network - Backoff %v)", delay)
				}
			} else {
				// Lỗi không xác định
				w.retryCount++
				if runDuration < MinStableSessionTime {
					// Session ngắn + lỗi lạ → Chờ lâu
					delay = ShortSessionDelay
					lastMsg += fmt.Sprintf(" (Unstable Session - Wait 20s, Retry %d)", w.retryCount)
				} else {
					// Session dài nhưng bị lỗi → Retry với backoff
					delay = NormalRetryDelay * time.Duration(w.retryCount)
					if delay > MaxRetryBackoff {
						delay = MaxRetryBackoff
					}
					lastMsg += fmt.Sprintf(" (Unknown - Retry %d)", w.retryCount)
				}
			}

			// Cập nhật status 1 lần (thread-safe)
			w.setStatusMsg("Error", lastMsg)

			// Thêm random jitter vào delay (±10%) để tránh pattern
			jitter := time.Duration(w.randIntn(int(delay.Milliseconds())/10)) * time.Millisecond
			if w.randIntn(2) == 0 {
				delay += jitter
			} else {
				delay -= jitter
			}

			log.Printf("[%s] Error: %v. Retry in %v", w.cfg.ID, err, delay)
			w.historyAddEvent("retry", fmt.Sprintf("Retry in %v", delay), 0)

			// Chờ trước khi thử lại (có thể bị cancel giữa chừng)
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
				// Hết giờ, thử lại
			case <-w.ctx.Done():
				timer.Stop()
				return
			}
		} else {
			// Session kết thúc không có lỗi (có thể manual stop hoặc context cancel)
			if runDuration >= MinStableSessionTime {
				// Session chạy lâu → coi là thành công
				w.retryCount = 0
				w.lastSuccess = time.Now()
				log.Printf("[%s] ✅ Session completed successfully after %.1fs", w.cfg.ID, runDuration.Seconds())
			}
			// Nếu session ngắn mà không có err thì có thể là test hoặc manual stop
		}
	}
}

// Hàm xử lý kết nối chính
func (w *Worker) runSession() error {
	// Dùng Dialer để có thể cancel kết nối đang pending

	// 1. KẾT NỐI SOURCE (NGUỒN)
	w.setStatus("Connecting Source")
	srcConn, err := connectToHost(w.ctx, w.cfg.SrcHost, w.cfg.SrcPort, w.cfg.SrcProxy, w.cfg.SrcUseSSL)
	if err != nil {
		return fmt.Errorf("dial source: %w", err)
	}
	defer srcConn.Close()

	// Gửi Header GET với User-Agent ngụy trang
	authSrc := basicAuth(w.cfg.SrcUser, w.cfg.SrcPass)
	reqSrc := fmt.Sprintf("GET /%s HTTP/1.1\r\nHost: %s\r\nNtrip-Version: %s\r\nUser-Agent: %s\r\nAuthorization: Basic %s\r\nConnection: %s\r\n\r\n",
		w.cfg.SrcMount, w.cfg.SrcHost, w.device.NtripVersion, w.userAgent, authSrc, w.device.Connection)
	if _, err := srcConn.Write([]byte(reqSrc)); err != nil {
		return fmt.Errorf("send request source: %w", err)
	}

	// [QUAN TRỌNG] Bufio bọc lấy srcConn. Cần giữ cái Reader này dùng mãi mãi.
	srcReader := bufio.NewReaderSize(srcConn, BufferSize)
	chunkedSource, err := checkResponse(srcReader, srcConn)
	if err != nil {
		return fmt.Errorf("source auth: %w", err)
	}
	// Một số caster (VD: geortk.jp/BKG) trả Transfer-Encoding: chunked.
	// Phải giải mã chunk trước khi chuyển tiếp, nếu không RTCM sẽ lẫn ký tự kích thước chunk.
	var srcStream io.Reader = srcReader
	if chunkedSource {
		srcStream = newChunkedReader(srcReader)
		log.Printf("[%s] Source uses chunked transfer encoding - decoding enabled", w.cfg.ID)
	}

	// Gửi NMEA mở hàng (ban đầu là Single vì chưa có data)
	startLat, startLon := w.resolvePosition()
	srcConn.Write([]byte(generateNMEA(startLat, startLon, false)))

	// 2. KẾT NỐI DESTINATION (ĐÍCH)
	w.setStatus("Connecting Dest")
	dstConn, err := connectToHost(w.ctx, w.cfg.DstHost, w.cfg.DstPort, w.cfg.DstProxy, w.cfg.DstUseSSL)
	if err != nil {
		return fmt.Errorf("dial dest: %w", err)
	}
	defer dstConn.Close()

	// Gửi Header POST với User-Agent ngụy trang
	authDst := basicAuth(w.cfg.DstUser, w.cfg.DstPass)
	reqDst := fmt.Sprintf("POST /%s HTTP/1.1\r\nHost: %s\r\nNtrip-Version: %s\r\nUser-Agent: %s\r\nAuthorization: Basic %s\r\nContent-Type: application/octet-stream\r\nConnection: %s\r\n\r\n",
		w.cfg.DstMount, w.cfg.DstHost, w.device.NtripVersion, w.userAgent, authDst, w.device.Connection)
	if _, err := dstConn.Write([]byte(reqDst)); err != nil {
		return fmt.Errorf("send request dest: %w", err)
	}

	// Check Dest trả lời
	dstReader := bufio.NewReader(dstConn)
	if _, err := checkResponse(dstReader, dstConn); err != nil {
		return fmt.Errorf("dest auth: %w", err)
	}

	// Đóng connection khi worker bị cancel để Read/Write đang block được giải phóng ngay.
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		select {
		case <-w.ctx.Done():
			srcConn.Close()
			dstConn.Close()
		case <-sessionDone:
		}
	}()

	// 3. CHUYỂN TRẠNG THÁI STREAMING
	w.setStatusMsg("Running", "Streaming OK")
	log.Printf("[%s] CONNECTED: %s -> %s", w.cfg.ID, w.cfg.SrcMount, w.cfg.DstMount)
	w.historyBeginSession()

	// Channel báo lỗi từ các luồng phụ
	errChan := make(chan error, 4)

	// -- Luồng phụ 1: Gửi NMEA Heartbeat với timing ngẫu nhiên --
	go func() {
		// Tính interval với jitter cho lần đầu
		nextInterval := w.nextNMEAInterval()
		ticker := time.NewTicker(nextInterval)
		defer ticker.Stop()
		for {
			select {
			case <-w.ctx.Done():
				return
			case <-ticker.C:
				// Kiểm tra xem có data gần đây không (trong 30s)
				lastData := atomic.LoadInt64(&w.lastDataTime)
				hasRecentData := (time.Now().Unix() - lastData) <= 30

				// Tạo NMEA với tham số riêng của worker này
				msg := w.generateNMEA(hasRecentData)
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

				// Reset ticker với interval mới (có jitter)
				ticker.Reset(w.nextNMEAInterval())
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
			// Đọc không timeout, chỉ đợi lỗi.
			// Dùng dstReader (đã bọc bufio) để không mất dữ liệu đã được đệm khi parse header.
			_, err := dstReader.Read(buf)
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

	lastActivity := time.Now() // Track activity để log cảnh báo

	for {
		// Set Timeout đọc: Nếu ReadTimeout mà Source không gửi byte nào -> Kill
		srcConn.SetReadDeadline(time.Now().Add(ReadTimeout))

		// ĐỌC TỪ BUFFER READER (Không phải conn)
		n, err := srcStream.Read(buf)
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

			// Update activity timestamp
			lastActivity = time.Now()
		} else {
			// n == 0 nhưng không lỗi - có thể là spurious wakeup
			// Kiểm tra xem đã lâu chưa nhận data
			if time.Since(lastActivity) > 60*time.Second {
				log.Printf("[%s] ⚠️  Warning: No data received for %.0fs", w.cfg.ID, time.Since(lastActivity).Seconds())
			}
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

// ================= NETWORK CONNECTION HELPER =================
// parseProxyURL - Parse nhiều định dạng proxy khác nhau
func parseProxyURL(proxyURL string) (addr string, auth *proxy.Auth, err error) {
	if proxyURL == "" {
		return "", nil, fmt.Errorf("empty proxy")
	}

	// Format 1: host:port:user:pass (định dạng phổ biến)
	parts := strings.Split(proxyURL, ":")
	if len(parts) == 4 {
		// host:port:user:pass
		addr = parts[0] + ":" + parts[1]
		auth = &proxy.Auth{
			User:     parts[2],
			Password: parts[3],
		}
		return addr, auth, nil
	}

	// Format 2: host:port (không auth)
	if len(parts) == 2 {
		return proxyURL, nil, nil
	}

	// Format 3: socks5://[user:pass@]host:port (chuẩn URI)
	if strings.HasPrefix(proxyURL, "socks5://") {
		proxyAddr := strings.TrimPrefix(proxyURL, "socks5://")

		// Tách user:pass nếu có
		if strings.Contains(proxyAddr, "@") {
			authParts := strings.SplitN(proxyAddr, "@", 2)
			if len(authParts) == 2 {
				userPass := strings.SplitN(authParts[0], ":", 2)
				if len(userPass) == 2 {
					auth = &proxy.Auth{
						User:     userPass[0],
						Password: userPass[1],
					}
				}
				addr = authParts[1]
				return addr, auth, nil
			}
		}
		return proxyAddr, nil, nil
	}

	return "", nil, fmt.Errorf("invalid proxy format")
}

// dialWithContextFallback - Dial qua proxy với timeout control (Phương án 2)
// Giải quyết vấn đề proxy.SOCKS5 không hỗ trợ context timeout
func dialWithContextFallback(ctx context.Context, dialer proxy.Dialer, network, addr string) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}

	resultChan := make(chan result, 1)

	// Dial trong goroutine riêng để có thể timeout
	go func() {
		conn, err := dialer.Dial(network, addr)

		// Kiểm tra context trước khi gửi kết quả
		select {
		case <-ctx.Done():
			// Context đã cancel/timeout, cleanup connection
			if conn != nil {
				conn.Close()
				log.Printf("[Proxy] Connection closed due to context cancellation: %s", addr)
			}
			return
		case resultChan <- result{conn, err}:
			// Gửi kết quả thành công
		}
	}()

	// Đợi kết quả hoặc timeout
	select {
	case res := <-resultChan:
		// Dial hoàn thành (thành công hoặc lỗi)
		return res.conn, res.err

	case <-ctx.Done():
		// Timeout hoặc cancel từ bên ngoài
		// Goroutine con sẽ tự cleanup khi nhận được ctx.Done()
		return nil, fmt.Errorf("proxy dial timeout: %w", ctx.Err())
	}
}

// connectToHost - Hàm thông minh kết nối qua Proxy + SSL
func connectToHost(ctx context.Context, host string, port int, proxyURL string, useSSL bool) (net.Conn, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	var baseConn net.Conn
	var err error

	// Bước 1: Kết nối qua SOCKS5 Proxy (nếu có)
	if proxyURL != "" {
		// Parse proxy URL (hỗ trợ nhiều định dạng)
		proxyAddr, auth, err := parseProxyURL(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy: %w", err)
		}

		// Tạo base dialer với timeout cho proxy handshake
		baseDialer := &net.Dialer{
			Timeout:   ProxyDialTimeout,
			KeepAlive: TCPKeepAlive,
		}

		// Tạo SOCKS5 dialer
		dialer, err := proxy.SOCKS5("tcp", proxyAddr, auth, baseDialer)
		if err != nil {
			return nil, fmt.Errorf("create proxy dialer: %w", err)
		}

		// Tạo context với timeout riêng cho proxy dial
		ctxProxy, cancel := context.WithTimeout(ctx, ProxyDialTimeout)
		defer cancel()

		// Kết nối qua proxy với timeout control (Phương án 2)
		log.Printf("[Proxy] Dialing via proxy %s to %s (timeout: %v)", proxyAddr, addr, ProxyDialTimeout)
		baseConn, err = dialWithContextFallback(ctxProxy, dialer, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("dial via proxy: %w", err)
		}
		log.Printf("[Proxy] Connected successfully via proxy to %s", addr)
	} else {
		// Kết nối trực tiếp với TCP optimization
		d := net.Dialer{
			Timeout:   DialTimeout,
			KeepAlive: TCPKeepAlive, // Giữ connection sống lâu
		}
		baseConn, err = d.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("dial direct: %w", err)
		}

		// Set TCP socket options để tối ưu
		if tcpConn, ok := baseConn.(*net.TCPConn); ok {
			tcpConn.SetKeepAlive(true)
			tcpConn.SetKeepAlivePeriod(TCPKeepAlive)
			tcpConn.SetNoDelay(true) // Tắt Nagle algorithm để giảm latency
		}
	}

	// Bước 2: Nếu yêu cầu SSL/TLS -> Bắt tay TLS
	if useSSL {
		tlsConfig := &tls.Config{
			ServerName:         host,
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: false, // Bật true nếu dùng self-signed cert
		}
		tlsConn := tls.Client(baseConn, tlsConfig)

		// TLS handshake với timeout
		tlsConn.SetDeadline(time.Now().Add(DialTimeout))
		if err := tlsConn.Handshake(); err != nil {
			baseConn.Close()
			return nil, fmt.Errorf("tls handshake: %w", err)
		}
		tlsConn.SetDeadline(time.Time{})
		return tlsConn, nil
	}

	return baseConn, nil
}

// ================= HELPERS =================

// checkResponse đọc status line + headers. Trả về true nếu response dùng chunked encoding.
func checkResponse(reader *bufio.Reader, conn net.Conn) (bool, error) {
	// Timeout cho việc đọc header response
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer conn.SetReadDeadline(time.Time{})

	line, err := reader.ReadString('\n')
	if err != nil {
		if err == io.EOF {
			return false, fmt.Errorf("server closed immediately (EOF) - Check credentials/mountpoint")
		}
		return false, err
	}

	// Đọc xả hết các dòng header còn lại (đến khi gặp dòng trắng)
	chunked := false
	for {
		l, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		if l == "\r\n" || l == "\n" {
			break
		}
		if strings.HasPrefix(strings.ToLower(l), "transfer-encoding:") &&
			strings.Contains(strings.ToLower(l), "chunked") {
			chunked = true
		}
	}

	// Kiểm tra mã phản hồi
	if strings.Contains(line, "200 OK") || strings.Contains(line, "ICY 200") {
		return chunked, nil
	}

	// Server báo lỗi (401, 404, 403...)
	return false, fmt.Errorf("rejected: %s", strings.TrimSpace(line))
}

// chunkedReader giải mã Transfer-Encoding: chunked từ NTRIP caster.
type chunkedReader struct {
	reader    *bufio.Reader
	remaining int64
	done      bool
}

func newChunkedReader(r *bufio.Reader) *chunkedReader {
	return &chunkedReader{reader: r}
}

func (cr *chunkedReader) Read(p []byte) (int, error) {
	if cr.done {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}

	// Đọc header của chunk kế tiếp khi chunk hiện tại đã hết
	for cr.remaining == 0 {
		line, err := cr.reader.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if idx := strings.IndexByte(line, ';'); idx >= 0 {
			line = strings.TrimSpace(line[:idx]) // bỏ chunk extension
		}
		if line == "" {
			continue
		}

		size, err := strconv.ParseInt(line, 16, 64)
		if err != nil || size < 0 {
			return 0, fmt.Errorf("invalid chunk size %q", line)
		}
		if size == 0 {
			if err := cr.discardTrailers(); err != nil {
				return 0, err
			}
			cr.done = true
			return 0, io.EOF
		}
		cr.remaining = size
	}

	if int64(len(p)) > cr.remaining {
		p = p[:cr.remaining]
	}
	n, err := cr.reader.Read(p)
	cr.remaining -= int64(n)

	if cr.remaining == 0 {
		if crlfErr := cr.discardCRLF(); err == nil {
			err = crlfErr
		}
	}
	return n, err
}

func (cr *chunkedReader) discardCRLF() error {
	line, err := cr.reader.ReadString('\n')
	if err != nil {
		return err
	}
	if line != "\r\n" && line != "\n" {
		return fmt.Errorf("invalid chunk terminator %q", line)
	}
	return nil
}

func (cr *chunkedReader) discardTrailers() error {
	for {
		line, err := cr.reader.ReadString('\n')
		if err != nil {
			return err
		}
		if line == "\r\n" || line == "\n" {
			return nil
		}
	}
}

func basicAuth(user, pass string) string {
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
}

// ================= USER-AGENT GENERATOR =================
// buildUserAgent ghép tiền tố cấu hình với User-Agent mặc định.
// Ví dụ: prefix "NTRIP" + base "GNSSInternetRadio/1.2.3" -> "NTRIP GNSSInternetRadio/1.2.3".
func buildUserAgent(prefix, base string) string {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		return base
	}
	if !strings.HasSuffix(prefix, " ") {
		prefix += " "
	}
	return prefix + base
}

func generateUserAgent(device DeviceProfile, rng *rand.Rand) string {
	version := ""

	switch device.VersionTemplate {
	case "major.minor.patch":
		// Ví dụ: 1.4.11, 2.3.5, 8.0.2
		major := rng.Intn(8) + 1 // 1-8
		minor := rng.Intn(10)    // 0-9
		patch := rng.Intn(20)    // 0-19
		version = fmt.Sprintf("%d.%d.%d", major, minor, patch)

	case "vmajor.minor.patch":
		// Ví dụ: v2.23.0, v1.15.3 (có prefix v)
		major := rng.Intn(4) + 1 // 1-4
		minor := rng.Intn(30)    // 0-29
		patch := rng.Intn(10)    // 0-9
		version = fmt.Sprintf("v%d.%d.%d", major, minor, patch)

	case "major.minor":
		// Ví dụ: 5.51, 6.20 (Trimble style)
		major := rng.Intn(8) + 3  // 3-10
		minor := rng.Intn(99) + 1 // 1-99
		version = fmt.Sprintf("%d.%d", major, minor)

	case "major.minor.patch.date":
		// Ví dụ: 7.6.0.20240712, 8.0.2.20230927
		major := rng.Intn(5) + 5 // 5-9
		minor := rng.Intn(10)    // 0-9
		patch := rng.Intn(5)     // 0-4
		// Random date trong 2 năm gần đây
		year := 2023 + rng.Intn(2) // 2023-2024
		month := rng.Intn(12) + 1  // 1-12
		day := rng.Intn(28) + 1    // 1-28 (safe for all months)
		version = fmt.Sprintf("%d.%d.%d.%04d%02d%02d", major, minor, patch, year, month, day)

	default:
		// Fallback
		version = "1.0.0"
	}

	return fmt.Sprintf("%s/%s", device.DeviceName, version)
}

// ================= NMEA INTERVAL HELPERS =================
// nmeaInterval trả về chu kỳ gửi GGA/NMEA hiện tại.
// Nếu config có nmea_interval_sec > 0 thì ưu tiên giá trị đó, ngược lại dùng device profile.
func (w *Worker) nmeaInterval() time.Duration {
	if w.cfg.NMEAIntervalSec > 0 {
		sec := w.cfg.NMEAIntervalSec
		if sec > 3600 {
			sec = 3600
		}
		return time.Duration(sec) * time.Second
	}
	return w.device.NMEAInterval
}

// nmeaJitter trả về biên độ jitter tự động, không vượt quá 20% interval.
func (w *Worker) nmeaJitter() time.Duration {
	interval := w.nmeaInterval()
	jitter := w.device.NMEAJitter
	maxJitter := interval / 5
	if jitter > maxJitter {
		jitter = maxJitter
	}
	if jitter < 0 {
		jitter = 0
	}
	return jitter
}

// nextNMEAInterval tính khoảng thời gian tới lần gửi NMEA kế tiếp (interval + jitter).
func (w *Worker) nextNMEAInterval() time.Duration {
	interval := w.nmeaInterval()
	jitter := w.nmeaJitter()
	if jitter <= 0 {
		return interval
	}
	delta := time.Duration(w.randInt63n(int64(jitter*2))) - jitter
	next := interval + delta
	if next < time.Second {
		next = time.Second
	}
	return next
}

// ================= HISTORY HELPERS =================
func (w *Worker) historyAddEvent(eventType, message string, durationSec float64) {
	if w.history == nil {
		return
	}
	if err := w.history.AddEvent(w.cfg.ID, eventType, message, durationSec); err != nil {
		log.Printf("[%s] History event failed: %v", w.cfg.ID, err)
	}
}

func (w *Worker) historyBeginSession() {
	if w.history == nil {
		return
	}
	id, err := w.history.BeginSession(w.cfg.ID)
	if err != nil {
		log.Printf("[%s] History begin session failed: %v", w.cfg.ID, err)
		return
	}
	w.sessionID = id
}

func (w *Worker) historyEndSession(result, message string) {
	if w.history == nil || w.sessionID == 0 {
		return
	}
	if err := w.history.EndSession(w.sessionID, w.cfg.ID, result, message); err != nil {
		log.Printf("[%s] History end session failed: %v", w.cfg.ID, err)
	}
	w.sessionID = 0
}

func (w *Worker) fixSnapshot() (fix int, hdop float64, sats int) {
	w.fixMu.Lock()
	fix = w.fixQuality
	w.fixMu.Unlock()
	return fix, w.hdop, w.sats
}

func fixQualityName(fix int) string {
	switch fix {
	case FixSingle:
		return "Single"
	case FixDGPS:
		return "DGPS/RTD"
	case FixRTKFixed:
		return "RTK Fixed"
	case FixRTKFloat:
		return "RTK Float"
	default:
		return "Unknown"
	}
}

// resolveFix quyết định trạng thái fix giả lập cho worker theo thời gian ngẫu nhiên.
// Trả về fix quality, HDOP, số vệ tinh và "age of differential" (để GGA nhất quán).
func (w *Worker) resolveFix(hasData bool) (fix int, hdop float64, sats int, age string) {
	// Mất dữ liệu -> hạ về Single, reset lịch để lần có dữ liệu lại bắt đầu từ Fixed
	if !hasData {
		w.fixMu.Lock()
		w.fixQuality = FixSingle
		w.fixNextChange = time.Time{}
		w.fixMu.Unlock()
		return FixSingle, w.hdop, w.sats, ""
	}

	w.fixMu.Lock()
	now := time.Now()
	if w.fixQuality == FixSingle || w.fixQuality == 0 || now.After(w.fixNextChange) {
		// Chọn trạng thái mới theo trọng số: Fixed giữ lâu, Float/RTD chỉ là giai đoạn ngắn
		switch roll := w.randIntn(100); {
		case roll < 75: // RTK Fixed (~75%)
			w.fixQuality = FixRTKFixed
			w.fixNextChange = now.Add(time.Duration(60+w.randIntn(240)) * time.Second)
		case roll < 93: // RTK Float (~18%)
			w.fixQuality = FixRTKFloat
			w.fixNextChange = now.Add(time.Duration(10+w.randIntn(45)) * time.Second)
		default: // DGPS / RTD (~7%)
			w.fixQuality = FixDGPS
			w.fixNextChange = now.Add(time.Duration(8+w.randIntn(35)) * time.Second)
		}
	}
	fix = w.fixQuality
	w.fixMu.Unlock()

	// Tham số phụ đi kèm cho nhất quán giữa các câu NMEA
	switch fix {
	case FixRTKFloat:
		hdop = w.hdop * 1.6
		sats = w.sats - 1
		age = fmt.Sprintf("%.1f", 0.8+w.randFloat64()*1.5)
	case FixDGPS:
		hdop = w.hdop * 2.2
		sats = w.sats - 2
		age = fmt.Sprintf("%.1f", 1.5+w.randFloat64()*2.0)
	default: // RTK Fixed
		hdop = w.hdop
		sats = w.sats
		age = ""
	}
	if sats < 4 {
		sats = 4
	}
	return
}

// offsetLatLon tính tọa độ mới cách điểm gốc distKm theo hướng bearingDeg (0=Bắc, 90=Đông).
// Dùng xấp xỉ equirectangular - đủ chính xác cho khoảng cách vài km.
func offsetLatLon(lat, lon, distKm, bearingDeg float64) (float64, float64) {
	const earthRadiusKm = 6371.0
	br := bearingDeg * math.Pi / 180.0
	dLat := (distKm * math.Cos(br)) / earthRadiusKm
	dLon := (distKm * math.Sin(br)) / (earthRadiusKm * math.Cos(lat*math.Pi/180.0))
	return lat + dLat*180.0/math.Pi, lon + dLon*180.0/math.Pi
}

// resolvePosition trả về vị trí giả lập hiện tại, di chuyển mượt quanh vị trí gốc.
// Vị trí đi theo waypoint ngẫu nhiên (tối đa PosMaxRadiusKm) với tốc độ không đổi,
// tạo cảm giác rover đang di chuyển thay vì nhảy tức thì.
func (w *Worker) resolvePosition() (lat, lon float64) {
	w.posMu.Lock()
	defer w.posMu.Unlock()

	const earthRadiusKm = 6371.0
	rad := math.Pi / 180.0

	now := time.Now()
	if !w.posInit {
		w.speedMps = PosMinSpeedMps + w.randFloat64()*(PosMaxSpeedMps-PosMinSpeedMps)
		// Khởi tạo tại một điểm ngẫu nhiên quanh vị trí gốc (tránh lộ toạ độ thật ngay từ đầu)
		initKm := PosMaxRadiusKm * 0.5 * math.Sqrt(w.randFloat64())
		w.curLat, w.curLon = offsetLatLon(w.cfg.Lat, w.cfg.Lon, initKm, w.randFloat64()*360.0)
		w.pickTargetLocked()
		w.moving = false
		w.idleUntil = now.Add(w.randIdleDurationLocked())
		w.posLastUpdate = now
		w.posInit = true
		return w.curLat, w.curLon
	}

	// ĐANG ĐỨNG YÊN: chờ hết thời gian nghỉ rồi mới bắt đầu di chuyển tới waypoint
	if !w.moving {
		if now.Before(w.idleUntil) {
			w.lastSpeedKnots = 0 // đứng yên -> tốc độ 0
			return w.curLat, w.curLon
		}
		// Hết thời gian nghỉ -> chọn waypoint mới và bắt đầu di chuyển
		w.moving = true
		w.pickTargetLocked()
		w.posLastUpdate = now
		return w.curLat, w.curLon
	}

	dt := now.Sub(w.posLastUpdate).Seconds()
	if dt <= 0 {
		return w.curLat, w.curLon
	}
	w.posLastUpdate = now

	// Vector từ vị trí hiện tại tới waypoint (km)
	northKm := (w.targetLat - w.curLat) * rad * earthRadiusKm
	eastKm := (w.targetLon - w.curLon) * rad * earthRadiusKm * math.Cos(w.curLat*rad)
	distKm := math.Hypot(northKm, eastKm)

	// Đã tới waypoint -> dừng lại, đứng yên chờ lần di chuyển kế tiếp
	if distKm <= PosArriveMeters/1000.0 {
		w.moving = false
		w.idleUntil = now.Add(w.randIdleDurationLocked())
		w.lastSpeedKnots = 0
		return w.curLat, w.curLon
	}

	// Quãng đường đi được trong dt (km), không vượt quá khoảng cách tới waypoint
	moveKm := w.speedMps * dt / 1000.0
	if moveKm > distKm {
		moveKm = distKm
	}
	uN := northKm / distKm
	uE := eastKm / distKm
	w.curLat += (uN * moveKm) / (earthRadiusKm * rad)
	w.curLon += (uE * moveKm) / (earthRadiusKm * rad * math.Cos(w.curLat*rad))

	// Cập nhật tốc độ + hướng cho GPRMC
	w.lastCourseDeg = math.Atan2(uE, uN) * 180.0 / math.Pi
	if w.lastCourseDeg < 0 {
		w.lastCourseDeg += 360.0
	}
	w.lastSpeedKnots = w.speedMps * 1.94384

	// Giới hạn trong bán kính tối đa quanh vị trí gốc
	if dBase := approxDistKm(w.cfg.Lat, w.cfg.Lon, w.curLat, w.curLon); dBase > PosMaxRadiusKm {
		bearing := bearingDegFrom(w.cfg.Lat, w.cfg.Lon, w.curLat, w.curLon)
		w.curLat, w.curLon = offsetLatLon(w.cfg.Lat, w.cfg.Lon, PosMaxRadiusKm, bearing)
		w.pickTargetLocked() // quay đầu vào trong
	}

	return w.curLat, w.curLon
}

// randIdleDurationLocked trả thời gian đứng yên ngẫu nhiên. Phải gọi khi giữ posMu.
func (w *Worker) randIdleDurationLocked() time.Duration {
	sec := PosIdleMinSec + w.randIntn(PosIdleMaxSec-PosIdleMinSec+1)
	return time.Duration(sec) * time.Second
}

// pickTargetLocked chọn waypoint mới gần vị trí hiện tại (quãng đường ngắn) và
// luôn nằm trong bán kính tối đa quanh vị trí gốc. Phải gọi khi đang giữ posMu.
func (w *Worker) pickTargetLocked() {
	// Waypoint cách vị trí hiện tại tối đa PosMoveRadiusKm (phân bố đều theo diện tích)
	rKm := PosMoveRadiusKm * math.Sqrt(w.randFloat64())
	tLat, tLon := offsetLatLon(w.curLat, w.curLon, rKm, w.randFloat64()*360.0)

	// Nếu waypoint vượt bán kính tối đa quanh vị trí gốc -> kéo vào trong
	if approxDistKm(w.cfg.Lat, w.cfg.Lon, tLat, tLon) > PosMaxRadiusKm {
		bearing := bearingDegFrom(w.cfg.Lat, w.cfg.Lon, tLat, tLon)
		tLat, tLon = offsetLatLon(w.cfg.Lat, w.cfg.Lon, PosMaxRadiusKm*math.Sqrt(w.randFloat64()), bearing)
	}
	w.targetLat, w.targetLon = tLat, tLon
}

// currentMotion trả về tốc độ (knots) và hướng (độ) hiện tại cho GPRMC.
func (w *Worker) currentMotion() (speedKnots, courseDeg float64) {
	w.posMu.Lock()
	defer w.posMu.Unlock()
	return w.lastSpeedKnots, w.lastCourseDeg
}

// approxDistKm - khoảng cách xấp xỉ (equirectangular) giữa 2 toạ độ, đơn vị km.
func approxDistKm(lat1, lon1, lat2, lon2 float64) float64 {
	const earthRadiusKm = 6371.0
	rad := math.Pi / 180.0
	northKm := (lat2 - lat1) * rad * earthRadiusKm
	eastKm := (lon2 - lon1) * rad * earthRadiusKm * math.Cos(lat1*rad)
	return math.Hypot(northKm, eastKm)
}

// bearingDegFrom - hướng từ điểm 1 sang điểm 2 (0=Bắc, 90=Đông).
func bearingDegFrom(lat1, lon1, lat2, lon2 float64) float64 {
	rad := math.Pi / 180.0
	north := lat2 - lat1
	east := (lon2 - lon1) * math.Cos(lat1*rad)
	b := math.Atan2(east, north) * 180.0 / math.Pi
	if b < 0 {
		b += 360.0
	}
	return b
}

// NMEA generator với tham số riêng của từng worker
func (w *Worker) generateNMEA(hasData bool) string {
	now := time.Now().UTC()
	lat, lon := w.resolvePosition()
	latStr := toDegMinDir(lat, true)
	lonStr := toDegMinDir(lon, false)

	// Trạng thái fix giả lập (RTK Fixed/Float/DGPS) theo thời gian ngẫu nhiên
	fixQuality, hdop, sats, age := w.resolveFix(hasData)

	// Thêm biến động nhỏ cho altitude (±0.5m)
	altOffset := (w.randFloat64() - 0.5) * 1.0
	alt := 100.0 + altOffset

	// GPGGA: time,lat,dir,lon,dir,fix,sats,hdop,alt,M,sep,M,age,station
	raw := fmt.Sprintf("GPGGA,%02d%02d%02d.00,%s,%s,%d,%d,%.1f,%.1f,M,-5.0,M,%s,",
		now.Hour(), now.Minute(), now.Second(), latStr, lonStr, fixQuality, sats, hdop, alt, age)

	var checksum byte
	for i := 0; i < len(raw); i++ {
		checksum ^= raw[i]
	}
	result := fmt.Sprintf("$%s*%02X\r\n", raw, checksum)

	// Một số device gửi thêm GSA hoặc RMC
	if w.device.SendGSA && w.randIntn(3) == 0 {
		result += w.generateGSA()
	}
	if w.device.SendRMC && w.randIntn(4) == 0 {
		result += w.generateRMC()
	}

	return result
}

// Generate GPGSA sentence (DOP and active satellites)
func (w *Worker) generateGSA() string {
	raw := fmt.Sprintf("GPGSA,A,3,01,02,03,04,05,06,07,08,09,10,11,12,%.1f,%.1f,%.1f",
		w.hdop*1.8, w.hdop, w.hdop*1.5)
	var checksum byte
	for i := 0; i < len(raw); i++ {
		checksum ^= raw[i]
	}
	return fmt.Sprintf("$%s*%02X\r\n", raw, checksum)
}

// Generate GPRMC sentence (minimal navigation data)
func (w *Worker) generateRMC() string {
	now := time.Now().UTC()
	lat, lon := w.resolvePosition()
	latStr := toDegMinDir(lat, true)
	lonStr := toDegMinDir(lon, false)
	speed, course := w.currentMotion()
	raw := fmt.Sprintf("GPRMC,%02d%02d%02d.00,A,%s,%s,%.1f,%.1f,%02d%02d%02d,,,A",
		now.Hour(), now.Minute(), now.Second(), latStr, lonStr, speed, course,
		now.Day(), now.Month(), now.Year()%100)
	var checksum byte
	for i := 0; i < len(raw); i++ {
		checksum ^= raw[i]
	}
	return fmt.Sprintf("$%s*%02X\r\n", raw, checksum)
}

// Legacy function for backward compatibility (not used anymore)
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

// ================= WEB MONITOR AUTHENTICATION =================
// Cho phép override qua biến môi trường WEB_USER / WEB_PASS, mặc định admin/admin.
var (
	WebUser = getEnvDefault("WEB_USER", "admin")
	WebPass = getEnvDefault("WEB_PASS", "admin")
)

func getEnvDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func basicAuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != WebUser || pass != WebPass {
			w.Header().Set("WWW-Authenticate", `Basic realm="NTRIP Relay Monitor"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// ================= WEB MONITOR =================
func startMonitorServer() {
	// Giao diện Web (Bảo vệ bằng Basic Auth)
	http.HandleFunc("/", basicAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, htmlContent)
	}))

	// API JSON Status (Bảo vệ bằng Basic Auth) - Merge tất cả configs với worker status
	http.HandleFunc("/status", basicAuthMiddleware(func(w http.ResponseWriter, r *http.Request) {
		manager.mu.RLock()
		defer manager.mu.RUnlock()

		// Dùng cache trong RAM (được cập nhật bởi reloadConfig), tránh đọc file mỗi request
		configs := manager.configs

		// Tạo map workers để lookup nhanh
		workerMap := make(map[string]*Worker)
		for _, worker := range manager.workers {
			workerMap[worker.cfg.ID] = worker
		}

		// Merge configs với worker status
		stats := make([]StationStatus, 0, len(configs))
		for i, cfg := range configs {
			if worker, exists := workerMap[cfg.ID]; exists {
				// Worker đang chạy - lấy status thực tế (thread-safe)
				s := worker.snapshot()
				s.Uptime = time.Since(s.StartTime).Round(time.Second).String()
				s.Order = i
				stats = append(stats, s)
			} else {
				// Worker chưa khởi động hoặc bị disable
				status := "Not Started"
				message := "Waiting to start"
				if !cfg.Enable {
					status = "Disabled"
					message = "Station is disabled in config"
				}

				s := StationStatus{
					ID:             cfg.ID,
					Status:         status,
					BytesForwarded: 0,
					Uptime:         "0s",
					LastMessage:    message,
					Order:          i,
				}
				stats = append(stats, s)
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	}))

	// API CRUD Configs
	http.HandleFunc("/api/configs", basicAuthMiddleware(handleConfigs))
	http.HandleFunc("/api/configs/", basicAuthMiddleware(handleConfigItem))

	// API chi tiết/lịch sử station
	http.HandleFunc("/api/stations/", basicAuthMiddleware(handleStationDetail))

	log.Printf("Monitor Interface: http://localhost%s", MonitorPort)
	log.Fatal(http.ListenAndServe(MonitorPort, nil))
}

// ================= CONFIG API HANDLERS =================
func handleConfigs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case "GET":
		// Đọc toàn bộ config
		configs, err := readConfigFile()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(configs)

	case "POST":
		// Thêm station mới
		var newStation ConfigStation
		if err := json.NewDecoder(r.Body).Decode(&newStation); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		newStation.UserAgentPrefix = strings.TrimSpace(newStation.UserAgentPrefix)
		newStation.NMEAIntervalSec = normalizeNMEAIntervalSec(newStation.NMEAIntervalSec)

		// Đọc config hiện tại
		configs, err := readConfigFile()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Kiểm tra ID trùng
		for _, cfg := range configs {
			if cfg.ID == newStation.ID {
				http.Error(w, "ID already exists", http.StatusConflict)
				return
			}
		}

		// Thêm vào
		configs = append(configs, newStation)

		// Lưu lại
		if err := saveConfigs(configs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// Áp dụng ngay (không chờ ticker 5s), chạy nền để không block request
		go reloadConfig()

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(newStation)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleConfigItem(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Lấy ID từ URL path
	id := strings.TrimPrefix(r.URL.Path, "/api/configs/")
	if id == "" {
		http.Error(w, "ID required", http.StatusBadRequest)
		return
	}

	// Đọc config hiện tại
	configs, err := readConfigFile()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	switch r.Method {
	case "GET":
		// Lấy 1 station
		for _, cfg := range configs {
			if cfg.ID == id {
				json.NewEncoder(w).Encode(cfg)
				return
			}
		}
		http.Error(w, "Not found", http.StatusNotFound)

	case "PUT":
		// Cập nhật station
		var updated ConfigStation
		if err := json.NewDecoder(r.Body).Decode(&updated); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		updated.UserAgentPrefix = strings.TrimSpace(updated.UserAgentPrefix)
		updated.NMEAIntervalSec = normalizeNMEAIntervalSec(updated.NMEAIntervalSec)

		found := false
		for i, cfg := range configs {
			if cfg.ID == id {
				updated.ID = id // Đảm bảo không đổi ID
				configs[i] = updated
				found = true
				break
			}
		}

		if !found {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		if err := saveConfigs(configs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		go reloadConfig()

		json.NewEncoder(w).Encode(updated)

	case "DELETE":
		// Xóa station
		found := false
		newConfigs := []ConfigStation{}
		for _, cfg := range configs {
			if cfg.ID != id {
				newConfigs = append(newConfigs, cfg)
			} else {
				found = true
			}
		}

		if !found {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}

		if err := saveConfigs(newConfigs); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		go reloadConfig()

		w.WriteHeader(http.StatusNoContent)

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// ================= STATION DETAIL API =================
func handleStationDetail(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/stations/")
	if !strings.HasSuffix(path, "/detail") {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	id := strings.TrimSuffix(path, "/detail")
	if id == "" || strings.Contains(id, "/") {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	window := 24 * time.Hour
	windowLabel := "24h"
	switch r.URL.Query().Get("window") {
	case "7d":
		window = 7 * 24 * time.Hour
		windowLabel = "7d"
	case "30d":
		window = 30 * 24 * time.Hour
		windowLabel = "30d"
	}

	manager.mu.RLock()
	var cfg ConfigStation
	found := false
	for _, c := range manager.configs {
		if c.ID == id {
			cfg = c
			found = true
			break
		}
	}
	worker := manager.workers[id]
	manager.mu.RUnlock()

	if !found {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	resp := StationDetailResponse{
		ID:          cfg.ID,
		Enabled:     cfg.Enable,
		Status:      "Not Started",
		Uptime:      "0s",
		LastDataSec: -1,
		Window:      windowLabel,
		Source:      fmt.Sprintf("%s:%d/%s", cfg.SrcHost, cfg.SrcPort, cfg.SrcMount),
		Destination: fmt.Sprintf("%s:%d/%s", cfg.DstHost, cfg.DstPort, cfg.DstMount),
		Lat:         cfg.Lat,
		Lon:         cfg.Lon,
		Timeline:    []TimelinePoint{},
		Events:      []HistoryEvent{},
		Sessions:    []HistorySession{},
	}
	if !cfg.Enable {
		resp.Status = "Disabled"
	}

	now := time.Now()
	if worker != nil {
		snap := worker.snapshot()
		resp.Status = snap.Status
		if !snap.StartTime.IsZero() {
			resp.Uptime = now.Sub(snap.StartTime).Round(time.Second).String()
		}
		resp.BytesForwarded = snap.BytesForwarded

		fix, hdop, sats := worker.fixSnapshot()
		resp.CurrentFix = fixQualityName(fix)
		resp.HDOP = hdop
		resp.Sats = sats

		if lastData := atomic.LoadInt64(&worker.lastDataTime); lastData > 0 {
			resp.LastDataSec = now.Sub(time.Unix(lastData, 0)).Seconds()
			if resp.LastDataSec < 0 {
				resp.LastDataSec = 0
			}
		}
	}

	if historyStore != nil {
		hist, err := historyStore.StationDetail(id, window)
		if err != nil {
			log.Printf("[%s] Load history failed: %v", id, err)
		} else {
			resp.OnlinePercent = hist.OnlinePercent
			resp.TotalSessions = hist.TotalSessions
			resp.EventCounts = hist.EventCounts
			resp.RetryCount = hist.EventCounts.Retry
			resp.Timeline = hist.Timeline
			resp.Events = hist.Events
			resp.Sessions = hist.Sessions
			resp.ConnectedAt = hist.ActiveSessionStart
			if hist.ActiveSessionStart != nil {
				resp.CurrentSessionSec = now.Sub(*hist.ActiveSessionStart).Seconds()
				if resp.CurrentSessionSec < 0 {
					resp.CurrentSessionSec = 0
				}
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// normalizeNMEAIntervalSec chuẩn hoá giá trị cấu hình: âm -> 0 (auto), quá lớn -> 3600s.
func normalizeNMEAIntervalSec(v int) int {
	if v < 0 {
		return 0
	}
	if v > 3600 {
		return 3600
	}
	return v
}

func saveConfigs(configs []ConfigStation) error {
	data, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return err
	}

	configFileMu.Lock()
	defer configFileMu.Unlock()

	// Ghi ra file tạm rồi rename đè -> atomic, tránh hỏng config khi crash giữa chừng
	tmpFile := ConfigFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return err
	}
	if err := os.Rename(tmpFile, ConfigFile); err != nil {
		os.Remove(tmpFile)
		return err
	}
	return nil
}

// HTML Dashboard (Nhẹ & Hiện đại)
const htmlContent = `<!DOCTYPE html>
<html>
<head>
	<title>NTRIP Relay Admin</title>
	<meta charset="utf-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<style>
		* { box-sizing: border-box; margin: 0; padding: 0; }
		body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #f5f7fa; color: #333; }
		
		.container { max-width: 1400px; margin: 0 auto; padding: 20px; }
		.header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 30px; padding-bottom: 15px; border-bottom: 2px solid #3b82f6; }
		h1 { color: #1e40af; font-size: 28px; }
		.btn { padding: 10px 20px; border: none; border-radius: 6px; cursor: pointer; font-size: 14px; font-weight: 600; transition: all 0.2s; }
		.btn-primary { background: #3b82f6; color: white; }
		.btn-primary:hover { background: #2563eb; }
		.btn-success { background: #10b981; color: white; }
		.btn-danger { background: #ef4444; color: white; }
		.btn-secondary { background: #6b7280; color: white; }
		.btn-sm { padding: 6px 12px; font-size: 12px; }
		
		.tabs { display: flex; gap: 10px; margin-bottom: 20px; }
		.tab { padding: 12px 24px; background: white; border: none; border-radius: 6px 6px 0 0; cursor: pointer; font-size: 15px; font-weight: 500; color: #6b7280; }
		.tab.active { background: #3b82f6; color: white; }
		
		.panel { background: white; border-radius: 8px; padding: 20px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); }
		.hidden { display: none; }
		
		.grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(380px, 1fr)); gap: 15px; }
		.card { background: white; border-radius: 8px; padding: 15px; box-shadow: 0 1px 3px rgba(0,0,0,0.1); border-left: 5px solid #ddd; }
		.card-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; }
		.card-id { font-weight: bold; font-size: 16px; color: #1e293b; }
		.badge { padding: 4px 10px; border-radius: 12px; font-size: 12px; font-weight: 600; color: white; }
		.badge-running { background: #10b981; }
		.badge-error { background: #ef4444; }
		.badge-stopped { background: #6b7280; }
		.card-actions { display: flex; gap: 5px; margin-top: 10px; }
		.stat-row { display: flex; justify-content: space-between; margin: 5px 0; font-size: 14px; }
		.stat-label { color: #6b7280; }
		.stat-val { font-family: monospace; font-weight: 600; }
		
		.form-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 15px; }
		.form-group { margin-bottom: 15px; }
		.form-group.full { grid-column: 1 / -1; }
		label { display: block; margin-bottom: 5px; font-weight: 500; font-size: 14px; color: #374151; }
		input, select { width: 100%; padding: 10px; border: 1px solid #d1d5db; border-radius: 6px; font-size: 14px; }
		input:focus, select:focus { outline: none; border-color: #3b82f6; }
		.checkbox-group { display: flex; align-items: center; gap: 8px; }
		.checkbox-group input { width: auto; }
		
		.modal { display: none; position: fixed; top: 0; left: 0; right: 0; bottom: 0; background: rgba(0,0,0,0.5); z-index: 1000; align-items: center; justify-content: center; }
		.modal.show { display: flex; }
		.modal-content { background: white; border-radius: 12px; padding: 25px; max-width: 700px; width: 90%; max-height: 90vh; overflow-y: auto; }
		.modal-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
		.modal-title { font-size: 20px; font-weight: 700; color: #1e293b; }
		.close { cursor: pointer; font-size: 24px; color: #6b7280; }
		.modal-footer { display: flex; justify-content: flex-end; gap: 10px; margin-top: 20px; }
		
		.search-box { width: 100%; max-width: 400px; padding: 10px 15px; border: 1px solid #d1d5db; border-radius: 6px; font-size: 14px; margin-bottom: 15px; }
		.search-box:focus { outline: none; border-color: #3b82f6; }
		.pagination { display: flex; align-items: center; justify-content: space-between; margin-top: 15px; padding-top: 15px; border-top: 1px solid #e5e7eb; }
		.pagination-info { color: #6b7280; font-size: 14px; }
		.pagination-controls { display: flex; gap: 5px; align-items: center; }
		.page-btn { padding: 6px 12px; border: 1px solid #d1d5db; background: white; border-radius: 4px; cursor: pointer; font-size: 13px; }
		.page-btn:hover:not(:disabled) { background: #f3f4f6; }
		.page-btn:disabled { opacity: 0.5; cursor: not-allowed; }
		.page-btn.active { background: #3b82f6; color: white; border-color: #3b82f6; }
		.page-size-select { padding: 6px 10px; border: 1px solid #d1d5db; border-radius: 4px; font-size: 13px; }
		
		.view-modal-content { max-width: 1000px; width: 95%; }
		.view-header-meta { display: flex; flex-wrap: wrap; gap: 10px; align-items: center; margin-bottom: 16px; color: #6b7280; font-size: 13px; }
		.view-summary { display: grid; grid-template-columns: repeat(auto-fit, minmax(150px, 1fr)); gap: 10px; margin-bottom: 18px; }
		.view-stat { background: #f8fafc; border: 1px solid #e5e7eb; border-radius: 8px; padding: 10px 12px; }
		.view-stat-label { color: #6b7280; font-size: 12px; margin-bottom: 4px; }
		.view-stat-value { font-family: monospace; font-weight: 700; font-size: 15px; color: #1e293b; }
		.view-section-title { font-size: 15px; font-weight: 700; color: #1e293b; margin: 18px 0 10px; }
		.view-toolbar { display: flex; justify-content: space-between; align-items: center; gap: 10px; flex-wrap: wrap; margin-bottom: 10px; }
		.view-window-btns { display: flex; gap: 5px; }
		.view-window-btn { padding: 5px 12px; border: 1px solid #d1d5db; background: white; border-radius: 4px; cursor: pointer; font-size: 12px; }
		.view-window-btn.active { background: #3b82f6; color: white; border-color: #3b82f6; }
		.view-chips { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 10px; }
		.view-chip { padding: 4px 10px; border-radius: 12px; font-size: 12px; background: #f3f4f6; color: #374151; }
		.view-chip.timeout { background: #ffedd5; color: #9a3412; }
		.view-chip.auth { background: #fee2e2; color: #991b1b; }
		.view-chip.proxy { background: #ede9fe; color: #5b21b6; }
		.view-chip.nmea { background: #fef3c7; color: #92400e; }
		.view-chip.retry { background: #dbeafe; color: #1e40af; }
		.view-timeline { display: flex; height: 20px; border-radius: 5px; overflow: hidden; background: #e5e7eb; margin-bottom: 6px; }
		.view-timeline-seg { height: 100%; min-width: 1px; }
		.view-timeline-labels { display: flex; justify-content: space-between; color: #9ca3af; font-size: 11px; margin-bottom: 14px; }
		.view-table { width: 100%; border-collapse: collapse; font-size: 13px; }
		.view-table th { background: #f3f4f6; padding: 8px; text-align: left; color: #374151; }
		.view-table td { padding: 8px; border-bottom: 1px solid #e5e7eb; vertical-align: top; }
		.view-empty { color: #6b7280; font-size: 13px; padding: 12px 0; }
		.event-badge { display: inline-block; padding: 2px 8px; border-radius: 10px; font-size: 11px; font-weight: 600; background: #e5e7eb; color: #374151; white-space: nowrap; }
		.event-badge.connected { background: #d1fae5; color: #065f46; }
		.event-badge.timeout { background: #ffedd5; color: #9a3412; }
		.event-badge.auth_error { background: #fee2e2; color: #991b1b; }
		.event-badge.proxy_error { background: #ede9fe; color: #5b21b6; }
		.event-badge.nmea_error { background: #fef3c7; color: #92400e; }
		.event-badge.retry { background: #dbeafe; color: #1e40af; }
		.event-badge.config { background: #e0e7ff; color: #3730a3; }
		.event-badge.stopped, .event-badge.disconnected { background: #e5e7eb; color: #374151; }
		.session-result { font-weight: 600; }
		.session-result.success { color: #047857; }
		.session-result.error { color: #b91c1c; }
		.session-result.stopped, .session-result.unknown { color: #6b7280; }
	</style>
</head>
<body>
	<div class="container">
		<div class="header">
			<h1>🛰️ NTRIP Relay Admin Panel</h1>
			<div style="display: flex; gap: 10px;">
				<button class="btn btn-primary" onclick="showAddModal()">+ Add Station</button>
				<button class="btn btn-success" onclick="showImportModal()">📋 Import JSON</button>
			</div>
		</div>
		
		<div class="tabs">
			<button class="tab active" onclick="switchTab('monitor')">Monitor</button>
			<button class="tab" onclick="switchTab('manage')">Manage Stations</button>
		</div>
		
		<div id="monitor-panel" class="panel">
			<input type="text" id="monitor-search" class="search-box" placeholder="🔍 Search stations (ID, status, message...)" oninput="searchMonitor()">
			<div id="monitor-grid" class="grid">Loading...</div>
			<div id="monitor-pagination" class="pagination">
				<div class="pagination-info">
					Showing <span id="monitor-showing">0</span> of <span id="monitor-total">0</span> stations
				</div>
				<div class="pagination-controls">
					<select id="monitor-pagesize" class="page-size-select" onchange="changeMonitorPageSize()">
						<option value="10">10 per page</option>
						<option value="20" selected>20 per page</option>
						<option value="50">50 per page</option>
						<option value="100">100 per page</option>
					</select>
					<button class="page-btn" id="monitor-prev" onclick="prevMonitorPage()" disabled>← Prev</button>
					<span style="font-size: 13px; color: #374151;">Page <span id="monitor-current-page">1</span> of <span id="monitor-total-pages">1</span></span>
					<button class="page-btn" id="monitor-next" onclick="nextMonitorPage()" disabled>Next →</button>
				</div>
			</div>
		</div>
		
		<div id="manage-panel" class="panel hidden">
			<input type="text" id="manage-search" class="search-box" placeholder="🔍 Search stations (ID, host, mountpoint...)" oninput="searchManage()">
			<div id="bulk-actions" style="display: none; margin-bottom: 15px; padding: 12px; background: #f3f4f6; border-radius: 6px;">
				<div style="display: flex; align-items: center; gap: 10px; flex-wrap: wrap;">
					<span style="font-weight: 600; color: #374151;">
						<span id="selected-count">0</span> selected
					</span>
					<button class="btn btn-sm btn-success" onclick="bulkEnable()">✓ Enable</button>
					<button class="btn btn-sm btn-secondary" onclick="bulkDisable()">✗ Disable</button>
					<button class="btn btn-sm btn-danger" onclick="bulkDelete()">🗑 Delete</button>
					<button class="btn btn-sm btn-secondary" onclick="clearSelection()" style="margin-left: auto;">Clear Selection</button>
				</div>
			</div>
			<div id="manage-list"></div>
			<div id="manage-pagination" class="pagination">
				<div class="pagination-info">
					Showing <span id="manage-showing">0</span> of <span id="manage-total">0</span> stations
				</div>
				<div class="pagination-controls">
					<select id="manage-pagesize" class="page-size-select" onchange="changeManagePageSize()">
						<option value="10">10 per page</option>
						<option value="20" selected>20 per page</option>
						<option value="50">50 per page</option>
						<option value="100">100 per page</option>
					</select>
					<button class="page-btn" id="manage-prev" onclick="prevManagePage()" disabled>← Prev</button>
					<span style="font-size: 13px; color: #374151;">Page <span id="manage-current-page">1</span> of <span id="manage-total-pages">1</span></span>
					<button class="page-btn" id="manage-next" onclick="nextManagePage()" disabled>Next →</button>
				</div>
			</div>
		</div>
	</div>
	
	<!-- Modal Import JSON -->
	<div id="import-modal" class="modal">
		<div class="modal-content">
			<div class="modal-header">
				<h2 class="modal-title">Import JSON Configuration</h2>
				<span class="close" onclick="closeImportModal()">&times;</span>
			</div>
			<div style="margin-bottom: 15px;">
				<p style="color: #6b7280; font-size: 14px; margin-bottom: 10px;">
					Paste JSON array or single object. Proxy formats supported: host:port:user:pass or socks5://user:pass@host:port
				</p>
				<details style="margin-bottom: 10px;">
					<summary style="cursor: pointer; color: #3b82f6; font-weight: 500;">📖 Show format examples</summary>
					<pre style="background: #f3f4f6; padding: 10px; border-radius: 6px; font-size: 12px; overflow-x: auto; margin-top: 10px;">// Single station:
{
  "id": "VRS1",
  "enable": true,
  "src_host": "source.com",
  "src_port": 2101,
  "src_mount": "MOUNT1",
  "src_user": "user",
  "src_pass": "pass",
  "src_proxy": "171.236.42.29:27564:user:pass",
  "src_use_ssl": false,
  "dst_host": "dest.com",
  "dst_port": 2101,
  "dst_mount": "DEST",
  "dst_user": "user",
  "dst_pass": "pass",
  "dst_proxy": "",
  "dst_use_ssl": false,
  "lat": 10.0,
  "lon": 106.0,
  "user_agent_prefix": "NTRIP",
  "nmea_interval_sec": 10
}

// Multiple stations (array):
[
  { "id": "VRS1", ... },
  { "id": "VRS2", ... }
]</pre>
				</details>
			</div>
			<textarea id="json-input" rows="15" style="width: 100%; padding: 12px; border: 1px solid #d1d5db; border-radius: 6px; font-family: monospace; font-size: 13px; resize: vertical;" placeholder='Paste JSON here...

Example:
{
  "id": "MyStation",
  "enable": true,
  "src_host": "source.example.com",
  "src_port": 2101,
  ...
}'></textarea>
			<div id="import-preview" style="display: none; margin-top: 15px; padding: 12px; background: #ecfdf5; border: 1px solid #10b981; border-radius: 6px;">
				<div style="font-weight: 600; color: #059669; margin-bottom: 5px;">✓ Valid JSON detected</div>
				<div id="preview-text" style="font-size: 13px; color: #047857;"></div>
			</div>
			<div id="import-error" style="display: none; margin-top: 15px; padding: 12px; background: #fef2f2; border: 1px solid #ef4444; border-radius: 6px;">
				<div style="font-weight: 600; color: #dc2626; margin-bottom: 5px;">⚠ JSON Error</div>
				<div id="error-text" style="font-size: 13px; color: #b91c1c;"></div>
			</div>
			<div class="modal-footer">
				<button type="button" class="btn btn-secondary" onclick="closeImportModal()">Cancel</button>
				<button type="button" class="btn btn-primary" onclick="validateJSON()">Validate</button>
				<button type="button" id="import-btn" class="btn btn-success" onclick="importJSON()" disabled>Import</button>
			</div>
		</div>
	</div>
	
	<!-- Modal Add/Edit -->
	<div id="modal" class="modal">
		<div class="modal-content">
			<div class="modal-header">
				<h2 class="modal-title" id="modal-title">Add Station</h2>
				<span class="close" onclick="closeModal()">&times;</span>
			</div>
			<form id="station-form">
				<div class="form-grid">
					<div class="form-group full">
						<label>Station ID *</label>
						<input type="text" id="f-id" required>
					</div>
					<div class="form-group">
						<label>Source Host *</label>
						<input type="text" id="f-src-host" required>
					</div>
					<div class="form-group">
						<label>Source Port *</label>
						<input type="number" id="f-src-port" required>
					</div>
					<div class="form-group">
						<label>Source Mountpoint *</label>
						<input type="text" id="f-src-mount" required>
					</div>
					<div class="form-group">
						<label>Source Username</label>
						<input type="text" id="f-src-user">
					</div>
					<div class="form-group">
						<label>Source Password</label>
						<input type="password" id="f-src-pass">
					</div>
					<div class="form-group">
						<label>Source Proxy (multiple formats supported)</label>
						<input type="text" id="f-src-proxy" placeholder="171.236.42.29:27564:user:pass or socks5://user:pass@host:port">
					</div>
					<div class="form-group">
						<div class="checkbox-group">
							<input type="checkbox" id="f-src-ssl">
							<label style="margin: 0;">Use SSL/TLS for Source</label>
						</div>
					</div>
					<div class="form-group">
						<label>Destination Host *</label>
						<input type="text" id="f-dst-host" required>
					</div>
					<div class="form-group">
						<label>Destination Port *</label>
						<input type="number" id="f-dst-port" required>
					</div>
					<div class="form-group">
						<label>Destination Mountpoint *</label>
						<input type="text" id="f-dst-mount" required>
					</div>
					<div class="form-group">
						<label>Destination Username</label>
						<input type="text" id="f-dst-user">
					</div>
					<div class="form-group">
						<label>Destination Password</label>
						<input type="password" id="f-dst-pass">
					</div>
					<div class="form-group">
						<label>Destination Proxy (multiple formats supported)</label>
						<input type="text" id="f-dst-proxy" placeholder="171.236.42.29:27564:user:pass or socks5://user:pass@host:port">
					</div>
					<div class="form-group">
						<div class="checkbox-group">
							<input type="checkbox" id="f-dst-ssl">
							<label style="margin: 0;">Use SSL/TLS for Destination</label>
						</div>
					</div>
					<div class="form-group">
						<label>Latitude</label>
						<input type="number" step="0.000001" id="f-lat" value="0">
					</div>
					<div class="form-group">
						<label>Longitude</label>
						<input type="number" step="0.000001" id="f-lon" value="0">
					</div>
					<div class="form-group">
						<label>User-Agent Prefix (blank = device default)</label>
						<input type="text" id="f-ua-prefix" placeholder="NTRIP">
					</div>
					<div class="form-group">
						<label>GGA/NMEA Interval (seconds, 0 = auto)</label>
						<input type="number" min="0" max="3600" id="f-nmea-interval" value="0" placeholder="0 = auto">
					</div>
					<div class="form-group">
						<div class="checkbox-group">
							<input type="checkbox" id="f-enable" checked>
							<label style="margin: 0;">Enable Station</label>
						</div>
					</div>
				</div>
				<div class="modal-footer">
					<button type="button" class="btn btn-secondary" onclick="closeModal()">Cancel</button>
					<button type="submit" class="btn btn-primary">Save</button>
				</div>
			</form>
		</div>
	</div>

	<!-- Modal View -->
	<div id="view-modal" class="modal">
		<div class="modal-content view-modal-content">
			<div class="modal-header">
				<h2 class="modal-title" id="view-title">Station Detail</h2>
				<span class="close" onclick="closeViewModal()">&times;</span>
			</div>
			<div id="view-body">Loading...</div>
		</div>
	</div>

	<script>
		let editingId = null;
		let currentTab = 'monitor';
		let validatedStations = null;
		let selectedStations = new Set();
		let viewStationId = null;
		let viewWindow = '24h';
		let viewPollTimer = null;
		
		// Pagination state for Monitor tab
		let monitorData = [];
		let monitorFilteredData = [];
		let monitorCurrentPage = 1;
		let monitorPageSize = 20;
		
		// Pagination state for Manage tab
		let manageData = [];
		let manageFilteredData = [];
		let manageCurrentPage = 1;
		let managePageSize = 20;
		
		function switchTab(tab) {
			currentTab = tab;
			document.querySelectorAll('.tab').forEach(t => t.classList.remove('active'));
			
			// Tìm và active tab button tương ứng
			const tabs = document.querySelectorAll('.tab');
			tabs.forEach(function(t) {
				if ((tab === 'monitor' && t.textContent.includes('Monitor')) ||
					(tab === 'manage' && t.textContent.includes('Manage'))) {
					t.classList.add('active');
				}
			});
			
			if (tab === 'monitor') {
				document.getElementById('monitor-panel').classList.remove('hidden');
				document.getElementById('manage-panel').classList.add('hidden');
			} else {
				document.getElementById('monitor-panel').classList.add('hidden');
				document.getElementById('manage-panel').classList.remove('hidden');
				loadManageList();
			}
		}
		
		function formatBytes(bytes) {
			if (bytes === 0) return '0 B';
			const k = 1024;
			const sizes = ['B', 'KB', 'MB', 'GB'];
			const i = Math.floor(Math.log(bytes) / Math.log(k));
			return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
		}
		
		function updateMonitor() {
			fetch('/status')
			.then(r => r.json())
			.then(data => {
				monitorData = data || [];
				// Không reset page khi auto-refresh, chỉ apply filter
				applyMonitorFilter();
			})
			.catch(e => console.error(e));
		}
		
		function searchMonitor() {
			// User thực hiện search -> reset về trang 1
			monitorCurrentPage = 1;
			applyMonitorFilter();
		}
		
		function applyMonitorFilter() {
			// Áp dụng filter nhưng GIỮ NGUYÊN page hiện tại
			const searchTerm = document.getElementById('monitor-search').value.toLowerCase();
			
			if (!searchTerm) {
				monitorFilteredData = monitorData;
			} else {
				monitorFilteredData = monitorData.filter(s => 
					s.id.toLowerCase().includes(searchTerm) ||
					s.status.toLowerCase().includes(searchTerm) ||
					(s.last_message && s.last_message.toLowerCase().includes(searchTerm))
				);
			}
			
			displayMonitorPage();
		}
		
		function displayMonitorPage() {
			const grid = document.getElementById('monitor-grid');
			const totalItems = monitorFilteredData.length;
			
			if (totalItems === 0) {
				grid.innerHTML = '<div style="grid-column: 1/-1; text-align: center; padding: 40px; color: #6b7280;">' + 
					(monitorData.length === 0 ? 'No stations configured. Click "Add Station" to start.' : 'No stations match your search.') + 
					'</div>';
				updateMonitorPagination(0, 0);
				return;
			}
			
			const totalPages = Math.ceil(totalItems / monitorPageSize);
			if (monitorCurrentPage > totalPages) monitorCurrentPage = totalPages;
			
			const startIdx = (monitorCurrentPage - 1) * monitorPageSize;
			const endIdx = Math.min(startIdx + monitorPageSize, totalItems);
			const pageData = monitorFilteredData.slice(startIdx, endIdx);
			
			grid.innerHTML = pageData.map(s => {
				const status = s.status.split(' ')[0];
			let badgeClass = 'badge-stopped';
			if (status === 'Running') badgeClass = 'badge-running';
			else if (status === 'Error') badgeClass = 'badge-error';
			else if (status === 'Disabled') badgeClass = 'badge-stopped';
			
			return '<div class="card">' +
				'<div class="card-header">' +
					'<span class="card-id">' + s.id + '</span>' +
					'<span class="badge ' + badgeClass + '">' + s.status + '</span>' +
				'</div>' +
				'<div class="stat-row"><span class="stat-label">Uptime:</span><span class="stat-val">' + s.uptime + '</span></div>' +
				'<div class="stat-row"><span class="stat-label">Data:</span><span class="stat-val">' + formatBytes(s.bytes_forwarded) + '</span></div>' +
				(s.last_message ? '<div style="margin-top: 8px; font-size: 12px; color: #' + (status === 'Disabled' ? '6b7280' : 'ef4444') + ';">⚠️ ' + s.last_message + '</div>' : '') +
				'<div class="card-actions">' +
					'<button class="btn btn-sm btn-success" onclick="viewStation(\'' + s.id + '\')" title="View station">View</button>' +
					'<button class="btn btn-sm btn-primary" onclick="editStationFromMonitor(\'' + s.id + '\')" title="Edit station">✏️ Edit</button>' +
					'<button class="btn btn-sm btn-danger" onclick="deleteStationFromMonitor(\'' + s.id + '\')" title="Delete station">🗑 Delete</button>' +
				'</div>' +
			'</div>';
		}).join('');
		
		updateMonitorPagination(endIdx - startIdx, totalItems);
	}
	
	function updateMonitorPagination(showing, total) {
		document.getElementById('monitor-showing').textContent = showing;
		document.getElementById('monitor-total').textContent = total;
		
		const totalPages = Math.ceil(total / monitorPageSize) || 1;
		document.getElementById('monitor-current-page').textContent = total > 0 ? monitorCurrentPage : 0;
		document.getElementById('monitor-total-pages').textContent = totalPages;
		
		document.getElementById('monitor-prev').disabled = monitorCurrentPage <= 1;
		document.getElementById('monitor-next').disabled = monitorCurrentPage >= totalPages;
	}
	
	function prevMonitorPage() {
		if (monitorCurrentPage > 1) {
			monitorCurrentPage--;
			displayMonitorPage();
		}
	}
	
	function nextMonitorPage() {
			const totalPages = Math.ceil(monitorFilteredData.length / monitorPageSize);
			if (monitorCurrentPage < totalPages) {
				monitorCurrentPage++;
				displayMonitorPage();
			}
		}
		
		function changeMonitorPageSize() {
			monitorPageSize = parseInt(document.getElementById('monitor-pagesize').value);
			monitorCurrentPage = 1;
			displayMonitorPage();
		}
		
		function loadManageList() {
			fetch('/api/configs')
			.then(r => r.json())
			.then(data => {
				manageData = data || [];
				searchManage();
			});
		}
		
		function searchManage() {
			const searchTerm = document.getElementById('manage-search').value.toLowerCase();
			
			if (!searchTerm) {
				manageFilteredData = manageData;
			} else {
				manageFilteredData = manageData.filter(s => 
					s.id.toLowerCase().includes(searchTerm) ||
					s.src_host.toLowerCase().includes(searchTerm) ||
					s.src_mount.toLowerCase().includes(searchTerm) ||
					s.dst_host.toLowerCase().includes(searchTerm) ||
					s.dst_mount.toLowerCase().includes(searchTerm)
				);
			}
			
			manageCurrentPage = 1;
			clearSelection();
			displayManagePage();
		}
		
		function displayManagePage() {
			const list = document.getElementById('manage-list');
			const totalItems = manageFilteredData.length;
			
			if (totalItems === 0) {
				list.innerHTML = '<p style="text-align: center; padding: 40px; color: #6b7280;">' + 
					(manageData.length === 0 ? 'No stations. Click "Add Station".' : 'No stations match your search.') + 
					'</p>';
				updateBulkActions();
				updateManagePagination(0, 0);
				return;
			}
			
			const totalPages = Math.ceil(totalItems / managePageSize);
			if (manageCurrentPage > totalPages) manageCurrentPage = totalPages;
			
			const startIdx = (manageCurrentPage - 1) * managePageSize;
			const endIdx = Math.min(startIdx + managePageSize, totalItems);
			const pageData = manageFilteredData.slice(startIdx, endIdx);
			
			list.innerHTML = '<table style="width: 100%; border-collapse: collapse;">' +
				'<thead><tr style="background: #f3f4f6; text-align: left;">' +
				'<th style="padding: 12px; width: 40px;"><input type="checkbox" id="select-all" onchange="toggleSelectAll()"></th>' +
				'<th style="padding: 12px;">ID</th>' +
				'<th style="padding: 12px;">Source</th>' +
				'<th style="padding: 12px;">Destination</th>' +
				'<th style="padding: 12px;">UA Prefix</th>' +
				'<th style="padding: 12px;">GGA</th>' +
				'<th style="padding: 12px;">Status</th>' +
				'<th style="padding: 12px;">Actions</th>' +
				'</tr></thead><tbody>' +
				pageData.map(s => 
					'<tr style="border-bottom: 1px solid #e5e7eb;">' +
					'<td style="padding: 12px;"><input type="checkbox" class="station-checkbox" value="' + s.id + '" onchange="updateSelection()"></td>' +
					'<td style="padding: 12px; font-weight: 600;">' + s.id + '</td>' +
					'<td style="padding: 12px;">' + s.src_host + ':' + s.src_port + '/' + s.src_mount + (s.src_use_ssl ? ' 🔒' : '') + (s.src_proxy ? ' 🌐' : '') + '</td>' +
					'<td style="padding: 12px;">' + s.dst_host + ':' + s.dst_port + '/' + s.dst_mount + (s.dst_use_ssl ? ' 🔒' : '') + (s.dst_proxy ? ' 🌐' : '') + '</td>' +
					'<td style="padding: 12px;">' + (s.user_agent_prefix || '-') + '</td>' +
					'<td style="padding: 12px;">' + (s.nmea_interval_sec ? s.nmea_interval_sec + 's' : 'auto') + '</td>' +
					'<td style="padding: 12px;">' + (s.enable ? '<span class="badge badge-running">Enabled</span>' : '<span class="badge badge-stopped">Disabled</span>') + '</td>' +
					'<td style="padding: 12px;"><div style="display: flex; gap: 5px;">' +
					'<button class="btn btn-sm btn-success" onclick="viewStation(\'' + s.id + '\');">View</button>' +
					'<button class="btn btn-sm btn-primary" onclick="editStation(\'' + s.id + '\');">Edit</button>' +
					'<button class="btn btn-sm btn-danger" onclick="deleteStation(\'' + s.id + '\');">Delete</button>' +
					'</div></td>' +
					'</tr>'
				).join('') +
				'</tbody></table>';
			
			updateBulkActions();
			updateManagePagination(endIdx - startIdx, totalItems);
		}
		
		function updateManagePagination(showing, total) {
			document.getElementById('manage-showing').textContent = showing;
			document.getElementById('manage-total').textContent = total;
			
			const totalPages = Math.ceil(total / managePageSize) || 1;
			document.getElementById('manage-current-page').textContent = total > 0 ? manageCurrentPage : 0;
			document.getElementById('manage-total-pages').textContent = totalPages;
			
			document.getElementById('manage-prev').disabled = manageCurrentPage <= 1;
			document.getElementById('manage-next').disabled = manageCurrentPage >= totalPages;
		}
		
		function prevManagePage() {
			if (manageCurrentPage > 1) {
				manageCurrentPage--;
				displayManagePage();
			}
		}
		
		function nextManagePage() {
			const totalPages = Math.ceil(manageFilteredData.length / managePageSize);
			if (manageCurrentPage < totalPages) {
				manageCurrentPage++;
				displayManagePage();
			}
		}
		
		function changeManagePageSize() {
			managePageSize = parseInt(document.getElementById('manage-pagesize').value);
			manageCurrentPage = 1;
			displayManagePage();
		}
		
		function showAddModal() {
			editingId = null;
			document.getElementById('modal-title').textContent = 'Add Station';
			document.getElementById('station-form').reset();
			document.getElementById('f-enable').checked = true;
			document.getElementById('modal').classList.add('show');
		}
		
		function editStation(id) {
			editingId = id;
			document.getElementById('modal-title').textContent = 'Edit Station';
			
			fetch('/api/configs/' + id)
			.then(r => r.json())
			.then(s => {
				document.getElementById('f-id').value = s.id;
				document.getElementById('f-src-host').value = s.src_host;
				document.getElementById('f-src-port').value = s.src_port;
				document.getElementById('f-src-mount').value = s.src_mount;
				document.getElementById('f-src-user').value = s.src_user || '';
				document.getElementById('f-src-pass').value = s.src_pass || '';
				document.getElementById('f-src-proxy').value = s.src_proxy || '';
				document.getElementById('f-src-ssl').checked = s.src_use_ssl || false;
				document.getElementById('f-dst-host').value = s.dst_host;
				document.getElementById('f-dst-port').value = s.dst_port;
				document.getElementById('f-dst-mount').value = s.dst_mount;
				document.getElementById('f-dst-user').value = s.dst_user || '';
				document.getElementById('f-dst-pass').value = s.dst_pass || '';
				document.getElementById('f-dst-proxy').value = s.dst_proxy || '';
				document.getElementById('f-dst-ssl').checked = s.dst_use_ssl || false;
				document.getElementById('f-lat').value = s.lat || 0;
				document.getElementById('f-lon').value = s.lon || 0;
				document.getElementById('f-ua-prefix').value = s.user_agent_prefix || '';
				document.getElementById('f-nmea-interval').value = s.nmea_interval_sec || 0;
				document.getElementById('f-enable').checked = s.enable;
				
				document.getElementById('modal').classList.add('show');
			});
		}
		
		function deleteStation(id) {
			if (!confirm('Delete station "' + id + '"?')) return;
			
			fetch('/api/configs/' + id, { method: 'DELETE' })
			.then(r => {
				if (r.ok) {
					alert('Deleted successfully');
					loadManageList();
				}
			});
		}
		
		function closeModal() {
			document.getElementById('modal').classList.remove('show');
		}
		
		function showImportModal() {
			document.getElementById('json-input').value = '';
			document.getElementById('import-preview').style.display = 'none';
			document.getElementById('import-error').style.display = 'none';
			document.getElementById('import-btn').disabled = true;
			validatedStations = null;
			document.getElementById('import-modal').classList.add('show');
		}
		
		function closeImportModal() {
			document.getElementById('import-modal').classList.remove('show');
		}
		
		function validateJSON() {
			const input = document.getElementById('json-input').value.trim();
			const previewDiv = document.getElementById('import-preview');
			const errorDiv = document.getElementById('import-error');
			const importBtn = document.getElementById('import-btn');
			
			previewDiv.style.display = 'none';
			errorDiv.style.display = 'none';
			importBtn.disabled = true;
			validatedStations = null;
			
			if (!input) {
				errorDiv.style.display = 'block';
				document.getElementById('error-text').textContent = 'Please paste JSON configuration';
				return;
			}
			
			try {
				let data = JSON.parse(input);
				
				// Convert single object to array
				if (!Array.isArray(data)) {
					data = [data];
				}
				
				// Validate structure
				const required = ['id', 'src_host', 'src_port', 'src_mount', 'dst_host', 'dst_port', 'dst_mount'];
				const errors = [];
				
				data.forEach(function(station, idx) {
					required.forEach(function(field) {
						if (!(field in station)) {
							errors.push('Station ' + (idx + 1) + ': Missing field "' + field + '"');
						}
					});
					
					// Set defaults for optional fields
					if (!('enable' in station)) station.enable = true;
					if (!('src_user' in station)) station.src_user = '';
					if (!('src_pass' in station)) station.src_pass = '';
					if (!('src_proxy' in station)) station.src_proxy = '';
					if (!('src_use_ssl' in station)) station.src_use_ssl = false;
					if (!('dst_user' in station)) station.dst_user = '';
					if (!('dst_pass' in station)) station.dst_pass = '';
					if (!('dst_proxy' in station)) station.dst_proxy = '';
					if (!('dst_use_ssl' in station)) station.dst_use_ssl = false;
					if (!('lat' in station)) station.lat = 0;
					if (!('lon' in station)) station.lon = 0;
					if (!('user_agent_prefix' in station)) station.user_agent_prefix = '';
					station.user_agent_prefix = String(station.user_agent_prefix).trim();
					if (!('nmea_interval_sec' in station)) station.nmea_interval_sec = 0;
					station.nmea_interval_sec = Math.max(0, Math.min(3600, parseInt(station.nmea_interval_sec) || 0));
				});
				
				if (errors.length > 0) {
					errorDiv.style.display = 'block';
					document.getElementById('error-text').innerHTML = errors.join('<br>');
					return;
				}
				
				// Success
				validatedStations = data;
				previewDiv.style.display = 'block';
				const stationIds = data.map(function(s) { return s.id; }).join(', ');
				document.getElementById('preview-text').innerHTML = 
					'Found <strong>' + data.length + '</strong> station(s): ' + stationIds + '<br>' +
					'Ready to import. Click "Import" to add them to your configuration.';
				importBtn.disabled = false;
				
			} catch (e) {
				errorDiv.style.display = 'block';
				document.getElementById('error-text').textContent = 'Invalid JSON: ' + e.message;
			}
		}
		
		function importJSON() {
			if (!validatedStations || validatedStations.length === 0) {
				alert('Please validate JSON first');
				return;
			}
			
			let successCount = 0;
			let errorCount = 0;
			const errors = [];
			
			const promises = validatedStations.map(function(station) {
				return fetch('/api/configs', {
					method: 'POST',
					headers: { 'Content-Type': 'application/json' },
					body: JSON.stringify(station)
				})
				.then(function(r) {
					if (r.ok) {
						successCount++;
					} else {
						return r.text().then(function(text) {
							errorCount++;
							errors.push(station.id + ': ' + text);
						});
					}
				})
				.catch(function(e) {
					errorCount++;
					errors.push(station.id + ': ' + e.message);
				});
			});
			
			Promise.all(promises).then(function() {
				let message = 'Import completed!\n\nSuccess: ' + successCount + '\nFailed: ' + errorCount;
				if (errors.length > 0) {
					message += '\n\nErrors:\n' + errors.join('\n');
				}
				alert(message);
				
				if (successCount > 0) {
					closeImportModal();
					if (currentTab === 'manage') loadManageList();
				}
			});
		}
		
		document.getElementById('station-form').addEventListener('submit', function(e) {
			e.preventDefault();
			
			const data = {
				id: document.getElementById('f-id').value,
				enable: document.getElementById('f-enable').checked,
				src_host: document.getElementById('f-src-host').value,
				src_port: parseInt(document.getElementById('f-src-port').value),
				src_mount: document.getElementById('f-src-mount').value,
				src_user: document.getElementById('f-src-user').value,
				src_pass: document.getElementById('f-src-pass').value,
				src_proxy: document.getElementById('f-src-proxy').value,
				src_use_ssl: document.getElementById('f-src-ssl').checked,
				dst_host: document.getElementById('f-dst-host').value,
				dst_port: parseInt(document.getElementById('f-dst-port').value),
				dst_mount: document.getElementById('f-dst-mount').value,
				dst_user: document.getElementById('f-dst-user').value,
				dst_pass: document.getElementById('f-dst-pass').value,
				dst_proxy: document.getElementById('f-dst-proxy').value,
				dst_use_ssl: document.getElementById('f-dst-ssl').checked,
				lat: parseFloat(document.getElementById('f-lat').value) || 0,
				lon: parseFloat(document.getElementById('f-lon').value) || 0,
				user_agent_prefix: document.getElementById('f-ua-prefix').value.trim(),
				nmea_interval_sec: Math.max(0, Math.min(3600, parseInt(document.getElementById('f-nmea-interval').value) || 0))
			};
			
			const url = editingId ? '/api/configs/' + editingId : '/api/configs';
			const method = editingId ? 'PUT' : 'POST';
			
			fetch(url, {
				method: method,
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(data)
			})
			.then(r => {
				if (r.ok) {
					alert('Saved successfully');
					closeModal();
					if (currentTab === 'manage') loadManageList();
				} else {
					return r.text().then(text => { throw new Error(text); });
				}
			})
			.catch(e => alert('Error: ' + e.message));
		});
		
		function toggleSelectAll() {
			const selectAll = document.getElementById('select-all');
			const checkboxes = document.querySelectorAll('.station-checkbox');
			checkboxes.forEach(function(cb) {
				cb.checked = selectAll.checked;
			});
			updateSelection();
		}
		
		function updateSelection() {
			selectedStations.clear();
			const checkboxes = document.querySelectorAll('.station-checkbox:checked');
			checkboxes.forEach(function(cb) {
				selectedStations.add(cb.value);
			});
			
			updateBulkActions();
			
			const allCheckboxes = document.querySelectorAll('.station-checkbox');
			const selectAll = document.getElementById('select-all');
			if (selectAll) {
				selectAll.checked = allCheckboxes.length > 0 && checkboxes.length === allCheckboxes.length;
				selectAll.indeterminate = checkboxes.length > 0 && checkboxes.length < allCheckboxes.length;
			}
		}
		
		function updateBulkActions() {
			const bulkDiv = document.getElementById('bulk-actions');
			const countSpan = document.getElementById('selected-count');
			
			if (selectedStations.size > 0) {
				bulkDiv.style.display = 'block';
				countSpan.textContent = selectedStations.size;
			} else {
				bulkDiv.style.display = 'none';
			}
		}
		
		function clearSelection() {
			selectedStations.clear();
			document.querySelectorAll('.station-checkbox').forEach(function(cb) {
				cb.checked = false;
			});
			const selectAll = document.getElementById('select-all');
			if (selectAll) selectAll.checked = false;
			updateBulkActions();
		}
		
		function bulkEnable() {
			if (selectedStations.size === 0) return;
			if (!confirm('Enable ' + selectedStations.size + ' station(s)?')) return;
			bulkUpdateStatus(true);
		}
		
		function bulkDisable() {
			if (selectedStations.size === 0) return;
			if (!confirm('Disable ' + selectedStations.size + ' station(s)?')) return;
			bulkUpdateStatus(false);
		}
		
		function bulkUpdateStatus(enableStatus) {
			let successCount = 0;
			let errorCount = 0;
			
			const promises = Array.from(selectedStations).map(function(id) {
				return fetch('/api/configs/' + id)
					.then(function(r) { return r.json(); })
					.then(function(station) {
						station.enable = enableStatus;
						return fetch('/api/configs/' + id, {
							method: 'PUT',
							headers: { 'Content-Type': 'application/json' },
							body: JSON.stringify(station)
						});
					})
					.then(function(r) {
						if (r.ok) successCount++;
						else errorCount++;
					})
					.catch(function() {
						errorCount++;
					});
			});
			
			Promise.all(promises).then(function() {
				alert((enableStatus ? 'Enabled' : 'Disabled') + ' ' + successCount + ' station(s), Failed: ' + errorCount);
				clearSelection();
				loadManageList();
			});
		}
		
		function bulkDelete() {
			if (selectedStations.size === 0) return;
			if (!confirm('DELETE ' + selectedStations.size + ' station(s)? This cannot be undone!')) return;
			
			let successCount = 0;
			let errorCount = 0;
			
			const promises = Array.from(selectedStations).map(function(id) {
				return fetch('/api/configs/' + id, { method: 'DELETE' })
					.then(function(r) {
						if (r.ok) successCount++;
						else errorCount++;
					})
					.catch(function() {
						errorCount++;
					});
			});
			
			Promise.all(promises).then(function() {
				alert('Deleted ' + successCount + ' station(s), Failed: ' + errorCount);
				clearSelection();
				loadManageList();
			});
		}
		
		// Functions cho Monitor tab Edit/Delete buttons
		function editStationFromMonitor(id) {
			// Chuyển sang tab Manage và mở edit modal
			switchTab('manage');
			setTimeout(function() {
				editStation(id);
			}, 100);
		}
		
		function deleteStationFromMonitor(id) {
			if (!confirm('Delete station "' + id + '"? This cannot be undone!')) return;
			
			fetch('/api/configs/' + id, { method: 'DELETE' })
			.then(function(r) {
				if (r.ok) {
					alert('Deleted successfully');
					updateMonitor(); // Refresh monitor
				} else {
					return r.text().then(function(text) {
						alert('Error: ' + text);
					});
				}
			})
			.catch(function(e) {
				alert('Error: ' + e.message);
			});
		}
		
		// ================= VIEW MODAL =================
		function escapeHtml(value) {
			return String(value == null ? '' : value)
				.replace(/&/g, '&amp;')
				.replace(/</g, '&lt;')
				.replace(/>/g, '&gt;')
				.replace(/"/g, '&quot;')
				.replace(/'/g, '&#39;');
		}

		function formatDuration(sec) {
			sec = Number(sec);
			if (!isFinite(sec) || sec < 0) sec = 0;
			var total = Math.floor(sec);
			var d = Math.floor(total / 86400);
			total = total % 86400;
			var h = Math.floor(total / 3600);
			total = total % 3600;
			var m = Math.floor(total / 60);
			var s = total % 60;
			var parts = [];
			if (d > 0) parts.push(d + 'd');
			if (h > 0) parts.push(h + 'h');
			if (m > 0) parts.push(m + 'm');
			parts.push(s + 's');
			return parts.join(' ');
		}

		function formatDateTime(iso) {
			if (!iso) return '-';
			var d = new Date(iso);
			if (isNaN(d.getTime())) return '-';
			return d.toLocaleString();
		}

		function eventTypeLabel(type) {
			var labels = {
				connected: 'Connected',
				disconnected: 'Disconnected',
				timeout: 'Timeout',
				auth_error: 'Auth',
				proxy_error: 'Proxy',
				nmea_error: 'NMEA',
				retry: 'Retry',
				config: 'Config',
				stopped: 'Stopped',
				warning: 'Warning',
				error: 'Error'
			};
			return labels[type] || type || 'Event';
		}

		function viewStatusBadge(status) {
			var first = String(status || '').split(' ')[0];
			var cls = 'badge-stopped';
			if (first === 'Running') cls = 'badge-running';
			else if (first === 'Error') cls = 'badge-error';
			return '<span class="badge ' + cls + '">' + escapeHtml(status || 'Unknown') + '</span>';
		}

		function viewStat(label, value) {
			return '<div class="view-stat"><div class="view-stat-label">' + escapeHtml(label) + '</div>' +
				'<div class="view-stat-value">' + value + '</div></div>';
		}

		function viewWindowButton(label, value) {
			return '<button type="button" class="view-window-btn' + (viewWindow === value ? ' active' : '') +
				'" onclick="setViewWindow(\'' + value + '\')">' + label + '</button>';
		}

		function renderTimeline(points) {
			if (!points || !points.length) {
				return '<div class="view-empty">No connection history yet.</div>';
			}
			var html = '<div class="view-timeline">';
			points.forEach(function(p) {
				var pct = Math.max(0, Math.min(100, Number(p.online_percent) || 0));
				var noData = p.has_data === false;
				var color = noData ? '#e5e7eb' : (pct >= 80 ? '#10b981' : (pct > 0 ? '#f59e0b' : '#ef4444'));
				var title = noData ? (formatDateTime(p.start) + ' - no data') :
					(formatDateTime(p.start) + ' - online ' + pct.toFixed(1) + '%' +
						((p.errors || 0) > 0 ? ', errors: ' + p.errors : ''));
				html += '<div class="view-timeline-seg" style="flex:1;background:' + color + ';" title="' +
					escapeHtml(title) + '"></div>';
			});
			html += '</div>';
			html += '<div class="view-timeline-labels"><span>' + escapeHtml(formatDateTime(points[0].start)) +
				'</span><span>' + escapeHtml(formatDateTime(points[points.length - 1].start)) + '</span></div>';

			var markers = '';
			points.forEach(function(p, i) {
				if ((p.errors || 0) > 0) {
					var left = ((i + 0.5) / points.length) * 100;
					markers += '<span style="position:absolute;left:' + left +
						'%;top:0;width:2px;height:8px;background:#dc2626;" title="' +
						escapeHtml(p.errors + ' error(s)') + '"></span>';
				}
			});
			if (markers) {
				html += '<div style="position:relative;height:8px;margin-top:-10px;margin-bottom:10px;">' + markers + '</div>';
			}
			return html;
		}

		function renderViewEvents(events) {
			if (!events || !events.length) {
				return '<div class="view-empty">No events recorded.</div>';
			}
			var html = '<table class="view-table"><thead><tr>' +
				'<th>Time</th><th>Type</th><th>Message</th><th>Duration</th>' +
				'</tr></thead><tbody>';
			events.forEach(function(e) {
				var duration = (Number(e.duration_sec) || 0) > 0 ? escapeHtml(formatDuration(e.duration_sec)) : '-';
				html += '<tr>' +
					'<td>' + escapeHtml(formatDateTime(e.time)) + '</td>' +
					'<td><span class="event-badge ' + escapeHtml(e.type) + '">' + escapeHtml(eventTypeLabel(e.type)) + '</span></td>' +
					'<td>' + escapeHtml(e.message || '') + '</td>' +
					'<td>' + duration + '</td>' +
					'</tr>';
			});
			return html + '</tbody></table>';
		}

		function renderViewSessions(sessions) {
			if (!sessions || !sessions.length) {
				return '<div class="view-empty">No sessions recorded.</div>';
			}
			var html = '<table class="view-table"><thead><tr>' +
				'<th>Start</th><th>End</th><th>Duration</th><th>Result</th><th>Message</th>' +
				'</tr></thead><tbody>';
			sessions.forEach(function(s) {
				var result = String(s.result || '');
				var end = s.end ? escapeHtml(formatDateTime(s.end)) : 'active';
				html += '<tr>' +
					'<td>' + escapeHtml(formatDateTime(s.start)) + '</td>' +
					'<td>' + end + '</td>' +
					'<td>' + escapeHtml(formatDuration(s.duration_sec)) + '</td>' +
					'<td><span class="session-result ' + escapeHtml(result) + '">' + escapeHtml(result || '-') + '</span></td>' +
					'<td>' + escapeHtml(s.message || '') + '</td>' +
					'</tr>';
			});
			return html + '</tbody></table>';
		}

		function renderStationView(d) {
			var counts = d.event_counts || {};
			var online = Number(d.online_percent) || 0;
			var lastData = Number(d.last_data_sec);
			var lastDataText = lastData < 0 ? 'Never' : formatDuration(lastData);
			var currentSession = Number(d.current_session_sec) || 0;

			var html = '';
			html += '<div class="view-header-meta">' +
				viewStatusBadge(d.status) +
				'<span>' + escapeHtml(d.source || '') + ' &rarr; ' + escapeHtml(d.destination || '') + '</span>' +
				'<span>' + escapeHtml((Number(d.lat) || 0).toFixed(6)) + ', ' + escapeHtml((Number(d.lon) || 0).toFixed(6)) + '</span>' +
				'</div>';

			html += '<div class="view-summary">' +
				viewStat('Uptime', escapeHtml(d.uptime || '0s')) +
				viewStat('Connected since', d.connected_at ? escapeHtml(formatDateTime(d.connected_at)) : '-') +
				viewStat('Current session', currentSession > 0 ? escapeHtml(formatDuration(currentSession)) : '-') +
				viewStat('Data forwarded', escapeHtml(formatBytes(d.bytes_forwarded || 0))) +
				viewStat('Last data', escapeHtml(lastDataText)) +
				viewStat('Sessions (window)', escapeHtml(String(d.total_sessions || 0))) +
				viewStat('Online', escapeHtml(online.toFixed(1) + '%')) +
				viewStat('Fix', escapeHtml((d.current_fix || 'Unknown') + (d.sats ? ' / ' + d.sats + ' sats' : ''))) +
				'</div>';

			html += '<div class="view-toolbar">' +
				'<div><strong>Activity</strong> <span style="color:#6b7280;font-size:13px;">Window ' +
				escapeHtml(d.window || viewWindow) + '</span></div>' +
				'<div class="view-window-btns">' +
				viewWindowButton('24h', '24h') +
				viewWindowButton('7d', '7d') +
				viewWindowButton('30d', '30d') +
				'</div></div>';

			html += '<div class="view-chips">' +
				'<span class="view-chip timeout">Timeout ' + escapeHtml(String(counts.timeout || 0)) + '</span>' +
				'<span class="view-chip auth">Auth ' + escapeHtml(String(counts.auth || 0)) + '</span>' +
				'<span class="view-chip proxy">Proxy ' + escapeHtml(String(counts.proxy || 0)) + '</span>' +
				'<span class="view-chip nmea">NMEA ' + escapeHtml(String(counts.nmea || 0)) + '</span>' +
				'<span class="view-chip retry">Retry ' + escapeHtml(String(counts.retry || 0)) + '</span>' +
				'<span class="view-chip">Errors ' + escapeHtml(String(counts.errors || 0)) + '</span>' +
				'</div>';

			html += '<div class="view-section-title">Connection timeline</div>' + renderTimeline(d.timeline);
			html += '<div class="view-section-title">Recent events</div>' + renderViewEvents(d.events);
			html += '<div class="view-section-title">Recent sessions</div>' + renderViewSessions(d.sessions);
			document.getElementById('view-body').innerHTML = html;
		}

		function fetchStationDetail() {
			if (!viewStationId) return;
			fetch('/api/stations/' + encodeURIComponent(viewStationId) + '/detail?window=' + encodeURIComponent(viewWindow))
				.then(function(r) {
					if (!r.ok) throw new Error('HTTP ' + r.status);
					return r.json();
				})
				.then(function(d) {
					renderStationView(d);
				})
				.catch(function(e) {
					if (viewPollTimer) {
						clearInterval(viewPollTimer);
						viewPollTimer = null;
					}
					document.getElementById('view-body').innerHTML =
						'<div style="color:#b91c1c;padding:10px 0;">Cannot load station detail: ' +
						escapeHtml(e.message) + '</div>';
				});
		}

		function viewStation(id) {
			if (viewPollTimer) {
				clearInterval(viewPollTimer);
				viewPollTimer = null;
			}
			viewStationId = id;
			viewWindow = '24h';
			document.getElementById('view-title').textContent = id;
			document.getElementById('view-body').innerHTML = 'Loading...';
			document.getElementById('view-modal').classList.add('show');
			fetchStationDetail();
			viewPollTimer = setInterval(fetchStationDetail, 2000);
		}

		function setViewWindow(value) {
			viewWindow = value;
			fetchStationDetail();
		}

		function closeViewModal() {
			if (viewPollTimer) {
				clearInterval(viewPollTimer);
				viewPollTimer = null;
			}
			viewStationId = null;
			document.getElementById('view-modal').classList.remove('show');
		}

		updateMonitor();
		setInterval(updateMonitor, 2000);
	</script>
</body>
</html>`
