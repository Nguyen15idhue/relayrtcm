# Kế hoạch: Nút View, lịch sử kết nối/lỗi và lưu trữ SQLite

- **Ngày lập:** 2026-10-20
- **File áp dụng:** `main.go`, file mới `history.go`, `go.mod`, `go.sum`, `README.md`
- **Mục tiêu:** Thêm nút View cho từng station; popup hiển thị thời gian kết nối, session, timeout/lỗi gần nhất; lưu lịch sử bằng SQLite để xem được 24h/7d/30d kể cả sau khi restart service.
- **Không làm:** Đồ thị vệ tinh.

## 1. Quyết định đã chốt

1. Lưu lịch sử bằng **SQLite**, không dùng RAM.
2. File DB: `history.db` cùng thư mục `config.json`.
3. Giữ bucket hoạt động **30 ngày**.
4. Giới hạn mỗi trạm: **500 events**, **200 sessions**.
5. UI hiển thị **50 events**, **30 sessions** gần nhất.
6. Xóa station khỏi config → xóa lịch sử của station đó.
7. Disable station → giữ lịch sử.
8. Không lưu mẫu vệ tinh, không vẽ đồ thị.
9. Driver: `modernc.org/sqlite` phiên bản **v1.45.0** (pure Go, tương thích Go 1.24.5).
10. Build bằng `go build -ldflags "-s -w" -o relay_new.exe .`, không build riêng `main.go`.

## 2. Quan hệ với kế hoạch NMEA interval

File liên quan: `kehoach/ke-hoach-nmea-interval-dong.md`.

### 2.1. Kết luận

**Không xung đột về thiết kế.** Hai kế hoạch giải quyết hai chức năng khác nhau:

- Kế hoạch NMEA interval: thêm `nmea_interval_sec` vào config, sửa heartbeat GGA, restart worker khi đổi interval.
- Kế hoạch View/SQLite: thêm lịch sử sự kiện/session, API chi tiết, nút View và popup.

Tuy nhiên có **chồng lấn khi sửa code** trong `main.go` và UI, nên cần merge cẩn thận.

### 2.2. Ma trận chồng lấn

| Hạng mục | Kế hoạch NMEA | Kế hoạch View/SQLite | Kết luận |
|---|---|---|---|
| `ConfigStation` | Thêm `NMEAIntervalSec` | Không thêm field config | Không xung đột |
| `getMD5Hash` | Hash đổi do field mới | Không tác động | Không xung đột |
| `main()` | Không sửa | Khởi tạo DB trước `reloadConfig` | Cần chèn đúng thứ tự |
| `reloadConfig` | Đổi interval → restart worker | Giữ history khi restart; xóa history khi station bị remove | Cần phối hợp lifecycle |
| Tạo worker | Log interval | Gắn `HistoryStore`/station ID | Cùng block, merge chung |
| `Start` | Không sửa logic chính | Ghi event/session, xử lý cancel do config | Cần phối hợp |
| `runSession` | Sửa heartbeat interval | Ghi `connected`, session start/end | Không xung đột |
| `runSession` cancel | Không đề cập | Phải ghi `stopped`, không ghi lỗi | Cần phối hợp |
| UI Add/Edit modal | Thêm ô interval | Không sửa | Merge |
| UI Manage table | Thêm cột GGA | Thêm nút View | Merge |
| JS `editStation`/submit | Sửa | Không sửa | Merge |
| JS mới | Không có | `viewStation`, polling, render | Không xung đột |
| Build command | Dùng `go build ... .` | Thêm `history.go`, cần build `.` | Thống nhất |
| `README.md` | Không nhắc | Phải sửa build `main.go` → `.` | Cập nhật |
| Raw string HTML | Thêm field | Thêm modal/JS | Không dùng backtick trong JS |

### 2.3. Các điểm phải phối hợp

1. **Thứ tự khởi tạo trong `main()`**
   - Mở SQLite trước khi `reloadConfig()` để worker đầu tiên có thể ghi history.
   - Nếu mở DB lỗi, relay vẫn chạy; history bị disable.

2. **Restart worker khi đổi NMEA interval**
   - Đổi interval làm `configHash` thay đổi → worker bị cancel và tạo lại.
   - Trước khi worker cũ dừng, phải đóng session đang mở (`end_ts`) để sampler không double-count online.
   - History trong SQLite giữ nguyên vì khóa theo station ID, không theo worker.

3. **Cancel do config không được ghi là lỗi**
   - Khi `w.ctx` bị cancel do đổi config/disable, ghi event `stopped` và đóng session với result `stopped`.
   - Không ghi `timeout`/`error`, tránh popup View hiện lỗi giả.

4. **Xóa station**
   - `reloadConfig` đã cancel + `wg.Wait()` worker.
   - Sau khi worker dừng hẳn, gọi `store.DeleteStation(id)` để xóa toàn bộ history.
   - Chỉ xóa khi ID không còn trong config; disable không xóa.

5. **UI**
   - Kế hoạch NMEA thêm ô interval vào modal Add/Edit và cột GGA ở bảng Manage.
   - Kế hoạch View thêm nút View và modal riêng.
   - Không sửa cùng một dòng nếu có thể; merge theo từng block.
   - Không dùng backtick/template literal trong JS vì `htmlContent` là Go raw string.

6. **Build**
   - Sau khi thêm `history.go`, lệnh `go build ... main.go` sẽ thiếu file.
   - Dùng `go build -ldflags "-s -w" -o relay_new.exe .`.
   - Cập nhật README.

### 2.4. Thứ tự triển khai đề xuất

Nên làm **kế hoạch NMEA interval trước**, vì phạm vi nhỏ và độc lập. Sau đó làm View/SQLite trên nền code đã ổn định, giảm rủi ro merge ở UI và worker.

Thứ tự:

1. NMEA interval: config, helper heartbeat, UI field, validate.
2. Build/test NMEA interval.
3. SQLite/View: dependency, `history.go`, instrument worker, API, UI View.
4. Build/test tổng thể.

## 3. Driver và ràng buộc build

- Go hiện tại: 1.24.5, Windows amd64, `CGO_ENABLED=0`.
- Dùng `modernc.org/sqlite v1.45.0`.
  - Là bản mới nhất còn tương thích Go 1.24.
  - Từ v1.46.x yêu cầu Go 1.25, không dùng.
- Không dùng `github.com/mattn/go-sqlite3` vì cần CGO/gcc.
- Binary tăng khoảng 10–20MB; chấp nhận được với relay chạy service.
- Cần mạng ở lần `go get` đầu tiên.

## 4. Thiết kế SQLite

Tạo file mới `history.go` (package `main`) chứa `HistoryStore`.

### 4.1. Schema

```sql
CREATE TABLE IF NOT EXISTS stations (
  id TEXT PRIMARY KEY,
  first_seen_ts INTEGER NOT NULL,
  last_seen_ts INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  station_id TEXT NOT NULL,
  ts INTEGER NOT NULL,
  type TEXT NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  duration_sec REAL NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_events_station_ts
  ON events(station_id, ts DESC);

CREATE TABLE IF NOT EXISTS sessions (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  station_id TEXT NOT NULL,
  start_ts INTEGER NOT NULL,
  end_ts INTEGER,
  duration_sec REAL NOT NULL DEFAULT 0,
  result TEXT NOT NULL DEFAULT 'active',
  message TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_sessions_station_start
  ON sessions(station_id, start_ts DESC);

CREATE TABLE IF NOT EXISTS activity_buckets (
  station_id TEXT NOT NULL,
  hour_ts INTEGER NOT NULL,
  online_sec INTEGER NOT NULL DEFAULT 0,
  sessions INTEGER NOT NULL DEFAULT 0,
  errors INTEGER NOT NULL DEFAULT 0,
  timeouts INTEGER NOT NULL DEFAULT 0,
  auth_errors INTEGER NOT NULL DEFAULT 0,
  proxy_errors INTEGER NOT NULL DEFAULT 0,
  nmea_errors INTEGER NOT NULL DEFAULT 0,
  retries INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (station_id, hour_ts)
);
```

- Thời gian lưu dạng Unix timestamp UTC.
- `hour_ts` là Unix giây đầu giờ.
- Dùng `PRAGMA user_version` để quản lý migration.

### 4.2. Cấu hình kết nối

```sql
PRAGMA journal_mode = WAL;
PRAGMA synchronous = NORMAL;
PRAGMA busy_timeout = 5000;
PRAGMA foreign_keys = ON;
```

- `db.SetMaxOpenConns(1)` để tránh `database is locked`.
- `db.SetMaxIdleConns(1)`.
- Chỉ dùng ổ đĩa local, không dùng network share.
- Nếu mở DB lỗi: log lỗi, relay vẫn chạy, history bị disable.
- Có thể chạy `PRAGMA quick_check` khi khởi động; nếu hỏng, đổi tên DB lỗi và tạo mới.

## 5. Ghi nhận dữ liệu

### 5.1. Sự kiện

Các loại event:

- `connected`
- `disconnected`
- `timeout`
- `auth_error`
- `proxy_error`
- `nmea_error`
- `error`
- `retry`
- `warning`
- `config`
- `stopped`

Điểm ghi:

- Worker tạo/restart: upsert `stations`, event `config`.
- Kết nối source + dest thành công (`main.go:618-620`):
  - Insert session `end_ts = NULL`, `result = 'active'`.
  - Event `connected`.
  - Tăng `sessions` trong bucket giờ hiện tại.
- `runSession` kết thúc:
  - Update session `end_ts`, `duration_sec`, `result`, `message`.
  - Event `disconnected` hoặc `stopped`.
- Lỗi/timeout/auth/proxy/NMEA:
  - Insert event.
  - Tăng cột tương ứng trong bucket.
- Retry: event `retry` kèm delay, tăng `retries`.
- Lỗi NMEA/dest đóng: ghi event ngay trong goroutine để UI thấy sớm, vì vòng lặp chính có thể block tới 90s.
- Cảnh báo không có data >60s (`main.go:728-734`): event `warning`, throttle để không spam.

### 5.2. Sampler 60 giây

Một goroutine chạy mỗi phút:

```sql
INSERT INTO activity_buckets(station_id, hour_ts, online_sec)
SELECT station_id, :hour, 60
FROM sessions WHERE end_ts IS NULL
ON CONFLICT(station_id, hour_ts)
DO UPDATE SET online_sec = online_sec + 60;

UPDATE stations SET last_seen_ts = :now
WHERE id IN (SELECT station_id FROM sessions WHERE end_ts IS NULL);
```

- `last_seen_ts` dùng để đóng session treo khi service bị kill.
- Khi khởi động lại:
  - Đóng các session `end_ts IS NULL`.
  - `end_ts = last_seen_ts`, `result = 'unknown'`, tránh tính downtime thành online.

### 5.3. Transaction

- `BeginSession`: insert session + event + tăng bucket `sessions` trong một transaction.
- `EndSession`: update session + event + đóng trạng thái trong một transaction.
- `AddEvent`: insert event + tăng counter bucket trong một transaction.
- Không giữ `manager.mu` trong lúc ghi DB.

## 6. Retention và xóa dữ liệu

- Giữ bucket/event/session **30 ngày**.
- Giới hạn mỗi trạm:
  - 500 events gần nhất.
  - 200 sessions gần nhất.
- UI chỉ query 50 events/30 sessions.
- Prune lúc khởi động và mỗi giờ.
- Định kỳ `PRAGMA wal_checkpoint(TRUNCATE)` để WAL không phình.
- Xóa station khỏi config:
  - Sau khi worker dừng, xóa `stations`, `events`, `sessions`, `activity_buckets` của ID đó.
- Disable station: giữ history.

## 7. API

Thêm route:

```go
http.HandleFunc("/api/stations/", basicAuthMiddleware(handleStationDetail))
```

`GET /api/stations/{id}/detail?window=24h|7d|30d`

Response:

- Thông tin an toàn: `id`, `enabled`, `status`, `uptime`, `connected_at`, `current_session_sec`.
- `bytes_forwarded`, `last_data_sec`, `retry_count`, `total_sessions`, `online_percent`.
- `event_counts`, `timeline`, `events`, `sessions`.
- `current_fix`, `sats`, `hdop` dạng text nếu cần.
- Không trả password/proxy credential.

Query:

- Events: `ORDER BY ts DESC LIMIT 50`.
- Sessions: `ORDER BY start_ts DESC LIMIT 30`.
- Buckets: `WHERE hour_ts >= ? ORDER BY hour_ts`.
- Online %: tổng `online_sec` / thời gian cửa sổ, không vượt quá tuổi lịch sử.
- Timeline: gộp bucket còn tối đa khoảng 120 đoạn.
- `window` không hợp lệ → mặc định `24h`.

Không đưa history vào `/status` vì UI đang poll `/status` mỗi 2s (`main.go:2509-2510`).

## 8. UI

- Thêm nút View:
  - Monitor card: View / Edit / Delete.
  - Bảng Manage: View / Edit / Delete.
- Modal View:
  - Header: ID, tọa độ, trạng thái, uptime, connected since, fix/sats text.
  - Activity: online %, số event, chip Timeout/Auth/Proxy/Retry.
  - Toggle 24h / 7d / 30d.
  - Timeline online/offline + marker lỗi.
  - Bảng sự kiện gần nhất.
  - Bảng session gần nhất.
- JS:
  - `viewStation(id)`: mở modal, fetch detail, render, polling 2s.
  - `closeViewModal()`: dừng polling.
  - Đổi window: fetch lại với `?window=`.
  - Escape HTML cho ID/message.
- Không dùng backtick/template literal trong JS vì `htmlContent` là Go raw string (`main.go:1564-2513`).
- Không thêm thư viện chart; không có đồ thị vệ tinh.

## 9. Build và README

- `go get modernc.org/sqlite@v1.45.0`
- `go mod tidy`
- Build:

```powershell
go build -ldflags "-s -w" -o relay_new.exe .
```

- Cập nhật README: lệnh cũ `go build ... main.go` (`README.md:385`) phải đổi thành build cả package (`.`), vì có thêm `history.go`.

## 10. Kiểm tra

1. Build với `CGO_ENABLED=0`, chạy relay.
2. Kiểm tra DB `history.db` được tạo cùng `config.json`.
3. Chạy bình thường → View hiển thị connected, uptime, session.
4. Sai mật khẩu → event `auth_error`, retry.
5. Mất mạng/read timeout → event `timeout`.
6. Proxy lỗi → event `proxy_error`.
7. Đổi NMEA interval → worker restart; history không mất; session cũ đóng đúng.
8. Restart service → lịch sử còn; 24h/7d/30d hiển thị.
9. Kill process → session treo được đóng bằng `last_seen_ts` khi khởi động lại.
10. Disable station → giữ history; xóa station → history bị xóa.
11. Nhiều station cùng lúc → không có `database is locked`.
12. Retention prune đúng 500 events/200 sessions/30 ngày.
13. UI: View, toggle 24h/7d/30d, timeline, event/session table, đóng modal dừng polling.

## 11. Rủi ro

- SQLite single-writer; cần `busy_timeout` và `MaxOpenConns(1)`.
- DB hỏng/ổ đĩa lỗi: relay phải fail-soft.
- Backup: stop service hoặc dùng `VACUUM INTO`; không copy mỗi `.db` khi đang chạy vì có `-wal`/`-shm`.
- Binary tăng kích thước do driver pure-Go.
- Nếu quên đóng session khi worker restart, sampler có thể double-count online.

## 12. Checklist triển khai

- [ ] Làm xong kế hoạch NMEA interval (nếu chưa).
- [ ] Thêm `modernc.org/sqlite@v1.45.0`.
- [ ] Tạo `history.go`: mở DB, schema, migration, event/session/bucket.
- [ ] Khởi tạo DB trong `main()` trước `reloadConfig`.
- [ ] Gắn history vào worker, instrument `Start`/`runSession`.
- [ ] Đảm bảo cancel do config ghi `stopped` và đóng session.
- [ ] Thêm sampler 60s, orphan recovery, prune.
- [ ] Xóa history khi station bị remove.
- [ ] Thêm API `/api/stations/{id}/detail`.
- [ ] Thêm nút View + modal + timeline + bảng.
- [ ] Cập nhật build command/README.
- [ ] Build `relay_new.exe`, test theo mục 10.
