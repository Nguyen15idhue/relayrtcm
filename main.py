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

# Tọa độ giả lập để gửi về Source
NMEA_LAT = 21.0
NMEA_LON = 105.0
SEND_NMEA_INTERVAL = 10  

BUFFER_SIZE = 4096  
RECONNECT_DELAY = 5
# ============================

def get_nmea_gga():
    """Tạo chuỗi NMEA $GPGGA"""
    now = time.gmtime()
    timestamp = f"{now.tm_hour:02}{now.tm_min:02}{now.tm_sec:02}.00"
    
    lat_deg = int(abs(NMEA_LAT))
    lat_min = (abs(NMEA_LAT) - lat_deg) * 60
    lat_str = f"{lat_deg:02}{lat_min:07.4f},{'N' if NMEA_LAT >= 0 else 'S'}"
    
    lon_deg = int(abs(NMEA_LON))
    lon_min = (abs(NMEA_LON) - lon_deg) * 60
    lon_str = f"{lon_deg:03}{lon_min:07.4f},{'E' if NMEA_LON >= 0 else 'W'}"

    # Fix Quality=1 (GPS), Sat=10, HDOP=1.0, Alt=100m
    raw = f"GPGGA,{timestamp},{lat_str},{lon_str},1,10,1.0,100.0,M,-5.0,M,,"
    
    checksum = 0
    for char in raw:
        checksum ^= ord(char)
    
    return f"${raw}*{checksum:02X}\r\n".encode()

def make_ntrip_header(mount, user, passwd, is_post=False):
    auth = base64.b64encode(f"{user}:{passwd}".encode()).decode()
    agent = "NTRIP PythonProxy/2.0"
    
    # SỬA: Dùng HTTP/1.0 hoặc 1.1 nhưng KHÔNG dùng Chunked encoding cho NTRIP Server thông thường
    # Trừ khi bạn thực sự implement logic gửi từng chunk hex.
    # NTRIP Server (Onocoy) nhận raw stream sau header.
    
    if is_post:
        return (
            f"POST /{mount} HTTP/1.0\r\n"
            f"Host: {DST_HOST}\r\n"
            f"User-Agent: {agent}\r\n"
            f"Authorization: Basic {auth}\r\n"
            f"Content-Type: application/octet-stream\r\n"
            f"Connection: close\r\n" # Giữ kết nối mở về mặt logic TCP, nhưng báo hiệu HTTP kết thúc header
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

def read_header_response(sock, label="SOCKET"):
    """Đọc phản hồi Header cho đến khi gặp \\r\\n\\r\\n"""
    header = b""
    sock.settimeout(5) # Timeout đọc header tối đa 5s
    try:
        while b"\r\n\r\n" not in header:
            chunk = sock.recv(1)
            if not chunk:
                raise Exception(f"{label} closed unexpectedly during auth")
            header += chunk
            if len(header) > 2048: # Tránh đọc vô tận nếu server lỗi
                raise Exception(f"{label} Header too large or invalid")
        
        # Kiểm tra mã 200 OK
        if b" 200 OK" not in header and b"ICY 200 OK" not in header:
            print(f"\n[{label}] Auth Failed. Response:\n{header.decode(errors='ignore')}")
            return False
        return True
    except socket.timeout:
        print(f"\n[{label}] Auth Timeout (No response)")
        return False
    finally:
        sock.settimeout(None) # Trả về chế độ blocking/select

def connect_socket(host, port, label):
    try:
        s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        s.settimeout(10)
        s.connect((host, port))
        s.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)
        return s
    except Exception as e:
        print(f"[{label}] Connect failed: {e}")
        return None

def main():
    print("=" * 60)
    print(f" NTRIP RELAY: {SRC_HOST} -> {DST_HOST}")
    print("=" * 60)

    while True:
        src = None
        dst = None
        try:
            # --- BƯỚC 1: KẾT NỐI SOURCE ---
            src = connect_socket(SRC_HOST, SRC_PORT, "SRC")
            if not src: raise Exception("Connect Source Failed")
            
            src.sendall(make_ntrip_header(SRC_MOUNT, SRC_USER, SRC_PASS, is_post=False))
            
            if not read_header_response(src, "SRC"):
                raise Exception("Source Authentication Failed")
            print("[SRC] Connected & Auth OK")

            # Gửi NMEA NGAY LẬP TỨC để kích hoạt luồng dữ liệu (quan trọng cho VRS)
            src.sendall(get_nmea_gga())
            print("[SRC] Sent initial NMEA GGA")

            # --- BƯỚC 2: KẾT NỐI DESTINATION ---
            dst = connect_socket(DST_HOST, DST_PORT, "DST")
            if not dst: raise Exception("Connect Dest Failed")
            
            dst.sendall(make_ntrip_header(DST_MOUNT, DST_USER, DST_PASS, is_post=True))
            
            # SỬA: Đợi phản hồi từ Dest trước khi đẩy data
            # Onocoy và các NTRIP Caster chuẩn sẽ trả về 200 OK trước khi nhận stream
            if not read_header_response(dst, "DST"):
                raise Exception("Destination Authentication Failed")
            print("[DST] Connected & Auth OK (Ready to push)")

            # --- BƯỚC 3: VÒNG LẶP RELAY ---
            print("[RELAY] Streaming started...")
            
            last_nmea_time = time.time()
            bytes_forwarded = 0
            start_time = time.time()
            
            while True:
                # Gửi NMEA định kỳ
                if time.time() - last_nmea_time > SEND_NMEA_INTERVAL:
                    try:
                        src.sendall(get_nmea_gga())
                        last_nmea_time = time.time()
                    except:
                        raise Exception("Send NMEA failed")

                # IO Multiplexing
                readable, _, exceptional = select.select([src, dst], [], [src, dst], 0.1)

                if src in exceptional or dst in exceptional:
                    raise Exception("Socket exception")

                # 1. Đọc từ Source -> Gửi sang Dest
                if src in readable:
                    data = src.recv(BUFFER_SIZE)
                    if not data:
                        raise Exception("Source closed connection (EOF)")
                    
                    try:
                        dst.sendall(data)
                        bytes_forwarded += len(data)
                    except:
                        raise Exception("Dest write failed (Broken Pipe)")

                # 2. Đọc từ Dest (để tránh đầy buffer TCP chiều về, dù thường là rỗng)
                if dst in readable:
                    d_data = dst.recv(1024)
                    if not d_data:
                        raise Exception("Dest closed connection (EOF)")
                    # Có thể in ra nếu Dest gửi thông báo lỗi
                    # print(f"[DST-MSG] {d_data}")

                # Log trạng thái
                duration = time.time() - start_time
                if duration > 0:
                    speed = (bytes_forwarded / 1024) / duration
                    msg = f"\r[RELAY] Forwarded: {bytes_forwarded/1024:.1f} KB | Time: {duration:.0f}s | Speed: {speed:.1f} KB/s"
                    sys.stdout.write(msg)
                    sys.stdout.flush()

        except KeyboardInterrupt:
            print("\nStopped by user.")
            break
        except Exception as e:
            print(f"\n[ERROR] {e}")
            print(f"Retry in {RECONNECT_DELAY}s...")
            if src: src.close()
            if dst: dst.close()
            time.sleep(RECONNECT_DELAY)
        finally:
            if src: src.close()
            if dst: dst.close()

if __name__ == "__main__":
    main()