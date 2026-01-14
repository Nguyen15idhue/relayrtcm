import socket
import base64
import time
import select
import sys

# ========== CONFIG ==========
SRC_HOST = "rtktk.online"
SRC_PORT = 1509
SRC_MOUNT = "YBI_VanYen"
SRC_USER = "nguyen"
SRC_PASS = "12345678"

DST_HOST = "servers.onocoy.com"
DST_PORT = 2101
DST_MOUNT = "YBVY_Relay"
DST_USER = "PlainlyFairFirefly"
DST_PASS = "Nguyen1509232"

# Tọa độ giả lập để gửi về Source (Cần thiết cho VRS hoặc giữ kết nối)
# Bạn nên sửa lại cho gần đúng vị trí trạm gốc của bạn
NMEA_LAT = 21.0
NMEA_LON = 105.0
SEND_NMEA_INTERVAL = 10  # Gửi NMEA mỗi 10 giây

BUFFER_SIZE = 4096  # Giảm buffer để xử lý nhanh hơn
RECONNECT_DELAY = 5
# ============================

def get_nmea_gga():
    """Tạo chuỗi NMEA $GPGGA đơn giản dựa trên thời gian hiện tại"""
    now = time.gmtime()
    # Format: HHMMSS.00
    timestamp = f"{now.tm_hour:02}{now.tm_min:02}{now.tm_sec:02}.00"
    
    # Tạo chuỗi chưa có checksum
    # 1: Fix Quality (1=GPS)
    # 08: Số vệ tinh
    # 1.0: HDOP
    # 100.0,M: Độ cao
    lat_deg = int(abs(NMEA_LAT))
    lat_min = (abs(NMEA_LAT) - lat_deg) * 60
    lat_str = f"{lat_deg:02}{lat_min:07.4f},{'N' if NMEA_LAT >= 0 else 'S'}"
    
    lon_deg = int(abs(NMEA_LON))
    lon_min = (abs(NMEA_LON) - lon_deg) * 60
    lon_str = f"{lon_deg:03}{lon_min:07.4f},{'E' if NMEA_LON >= 0 else 'W'}"

    raw = f"GPGGA,{timestamp},{lat_str},{lon_str},1,08,1.0,100.0,M,-5.0,M,,"
    
    # Tính checksum
    checksum = 0
    for char in raw:
        checksum ^= ord(char)
    
    return f"${raw}*{checksum:02X}\r\n".encode()

def make_ntrip_request(mount, user, passwd, is_post=False):
    auth = base64.b64encode(f"{user}:{passwd}".encode()).decode()
    agent = "NTRIP PythonProxy/1.0"
    
    if is_post:
        return (
            f"POST /{mount} HTTP/1.1\r\n"
            f"Host: {DST_HOST}\r\n"
            f"User-Agent: {agent}\r\n"
            f"Authorization: Basic {auth}\r\n"
            f"Transfer-Encoding: chunked\r\n" # NTRIP 2.0 / HTTP 1.1 thường thích cái này hơn
            f"Connection: close\r\n"
            f"\r\n"
        ).encode()
    else:
        return (
            f"GET /{mount} HTTP/1.0\r\n"
            f"User-Agent: {agent}\r\n"
            f"Authorization: Basic {auth}\r\n"
            f"Connection: close\r\n"
            f"\r\n"
        ).encode()

def connect_socket(host, port, label):
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(10) # Timeout kết nối
        s.connect((host, port))
        s.settimeout(None) # Chuyển về chế độ blocking (sẽ dùng select để handle)
        s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1) # Tắt thuật toán Nagle để giảm delay
        return s
    except Exception as e:
        print(f"[{label}] Connect failed: {e}")
        return None

def main():
    print("=" * 60)
    print(" NTRIP RELAY (LOW LATENCY + NMEA)")
    print("=" * 60)

    while True:
        src = None
        dst = None
        try:
            # 1. Kết nối SOURCE
            src = connect_socket(SRC_HOST, SRC_PORT, "SRC")
            if not src: raise Exception("Cannot connect Source")
            src.sendall(make_ntrip_request(SRC_MOUNT, SRC_USER, SRC_PASS, is_post=False))
            
            # Đọc header phản hồi từ Source
            header = b""
            while b"\r\n\r\n" not in header:
                chunk = src.recv(1)
                if not chunk: raise Exception("Source closed unexpectedly during auth")
                header += chunk
            
            if b"200 OK" not in header and b"ICY 200 OK" not in header:
                print(f"[SRC] Auth failed response: {header}")
                raise Exception("Source rejected auth")
            print("[SRC] Connected & Auth OK")

            # 2. Kết nối DESTINATION
            dst = connect_socket(DST_HOST, DST_PORT, "DST")
            if not dst: raise Exception("Cannot connect Dest")
            dst.sendall(make_ntrip_request(DST_MOUNT, DST_USER, DST_PASS, is_post=True))
            # Với POST (Server Onocoy), thường ta không đợi 200 OK ngay lập tức mà cứ đẩy data
            # Nhưng tốt nhất nên check sơ qua nếu server trả về lỗi ngay
            
            print("[RELAY] Start streaming...")
            
            last_nmea_time = 0
            bytes_forwarded = 0
            start_time = time.time()
            
            while True:
                # Gửi NMEA định kỳ cho Source để giữ kết nối và lấy data đúng vị trí
                if time.time() - last_nmea_time > SEND_NMEA_INTERVAL:
                    nmea_data = get_nmea_gga()
                    try:
                        src.sendall(nmea_data)
                        # print(f"[TICK] Sent NMEA to Source") # Uncomment để debug
                        last_nmea_time = time.time()
                    except:
                        raise Exception("Send NMEA failed")

                # Sử dụng select để đợi dữ liệu tối đa 0.1s
                # Giúp vòng lặp không bị treo nếu không có dữ liệu
                readable, _, exceptional = select.select([src, dst], [], [src, dst], 0.1)

                if src in exceptional or dst in exceptional:
                    raise Exception("Socket error (exceptional condition)")

                if src in readable:
                    data = src.recv(BUFFER_SIZE)
                    if not data:
                        raise Exception("Source closed connection")
                    
                    # Gửi sang Dest
                    try:
                        # Với Transfer-Encoding: chunked (nếu dùng HTTP 1.1), cần format lại
                        # Nhưng Onocoy NTRIP thường chấp nhận raw stream nếu dùng HTTP 1.0 hoặc NTRIP 1.0
                        # Ở đây ta gửi Raw byte-stream (cách phổ biến nhất)
                        dst.sendall(data)
                        bytes_forwarded += len(data)
                    except:
                        raise Exception("Dest closed connection")

                # Đọc phản hồi từ Dest (nếu có) để tránh đầy buffer chiều về
                if dst in readable:
                    dst_resp = dst.recv(1024)
                    if not dst_resp:
                        raise Exception("Dest closed connection (read)")
                    # Có thể in ra để debug xem Onocoy trả về gì (thường là rỗng hoặc lỗi)
                    # print(f"[DST-MSG] {dst_resp}")

                # Log trạng thái mỗi 5MB
                if bytes_forwarded > 0 and bytes_forwarded % (1024 * 1024) < BUFFER_SIZE:
                   sys.stdout.write(f"\r[RELAY] Forwarded: {bytes_forwarded/1024/1024:.2f} MB | Time: {time.time()-start_time:.0f}s")
                   sys.stdout.flush()

        except KeyboardInterrupt:
            print("\nStopped.")
            break
        except Exception as e:
            print(f"\n[ERROR] {e}")
            print(f"Reconnect in {RECONNECT_DELAY}s...")
            time.sleep(RECONNECT_DELAY)
        finally:
            if src: src.close()
            if dst: dst.close()

if __name__ == "__main__":
    main()