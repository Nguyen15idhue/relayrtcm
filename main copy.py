import socket
import base64
import time
import threading
from datetime import datetime

# -------- CONFIG --------
SRC_URL = "rtktk.online"
SRC_PORT = 1509
SRC_MOUNT = "YBI_VanYen"
SRC_USER = "nguyen"
SRC_PASS = "12345678"

DST_URL = "servers.onocoy.com"
DST_PORT = 2101
DST_MOUNT = "YBVY_Relay"  # Mount point tự đặt
DST_USER = "PlainlyFairFirefly"
DST_PASS = "Nguyen1509232"

GGA_INTERVAL = 22  # gửi GGA mỗi 22s
BUFFER_SIZE = 4096
TIMEOUT = 60  # timeout socket
# ------------------------

# Biến đếm để theo dõi
bytes_sent = 0
bytes_received = 0
last_send_time = None

def ntrip_connect(host, port, mount, user, passwd, is_source=False):
    """Kết nối NTRIP với xử lý lỗi cẩn thận"""
    auth = base64.b64encode(f"{user}:{passwd}".encode()).decode()
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.settimeout(TIMEOUT)
    
    print(f"  → Connecting to {host}:{port}/{mount}...")
    s.connect((host, port))
    
    # Gửi request khác nhau cho source (client) và destination (server)
    if is_source:
        # NTRIP Client - lấy dữ liệu
        req = f"GET /{mount} HTTP/1.1\r\n"
        req += f"User-Agent: NTRIP PythonRelay/1.0\r\n"
        req += f"Authorization: Basic {auth}\r\n"
        req += "\r\n"
    else:
        # NTRIP Server (Caster) - gửi dữ liệu lên
        req = f"POST /{mount} HTTP/1.1\r\n"
        req += f"Host: {host}:{port}\r\n"
        req += f"Ntrip-Version: Ntrip/2.0\r\n"
        req += f"User-Agent: NTRIP PythonRelay/1.0\r\n"
        req += f"Authorization: Basic {auth}\r\n"
        req += f"Connection: close\r\n"
        req += f"Transfer-Encoding: chunked\r\n"
        req += "\r\n"
    
    s.sendall(req.encode())
    
    # Đọc response
    resp = s.recv(1024)
    resp_str = resp.decode(errors='ignore')
    
    if b"200 OK" not in resp and "ICY 200 OK" not in resp_str:
        if "401" in resp_str or "403" in resp_str:
            raise Exception(f"Authentication failed: {resp_str[:200]}")
        elif "404" in resp_str:
            raise Exception(f"Mount point not found: {mount}")
        else:
            raise Exception(f"Connection failed: {resp_str[:200]}")
    
    print(f"  ✓ Connected successfully!")
    return s

def send_gga_periodically(dst_sock):
    """Gửi GGA định kỳ để duy trì kết nối"""
    # GGA giả định vị trí Yên Bái
    gga = "$GPGGA,123519,2148.000,N,10430.000,E,1,08,0.9,545.4,M,46.9,M,,*4E\r\n"
    while True:
        try:
            dst_sock.sendall(gga.encode())
            print(f"  → GGA sent at {datetime.now().strftime('%H:%M:%S')}")
        except Exception as e:
            print(f"  ✗ GGA send failed: {e}")
            break
        time.sleep(GGA_INTERVAL)

def main():
    global bytes_sent, bytes_received, last_send_time
    
    print("=" * 60)
    print("RTCM RELAY: rtktk.online → onocoy.com")
    print("=" * 60)
    print(f"Source: {SRC_URL}:{SRC_PORT}/{SRC_MOUNT}")
    print(f"Destination: {DST_URL}:{DST_PORT}/{DST_MOUNT}")
    print("=" * 60)
    
    reconnect_count = 0
    
    while True:
        src_sock = None
        dst_sock = None
        
        try:
            # Kết nối nguồn
            print(f"\n[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] Connecting to SOURCE...")
            src_sock = ntrip_connect(SRC_URL, SRC_PORT, SRC_MOUNT, SRC_USER, SRC_PASS, is_source=True)
            
            # Kết nối đích
            print(f"[{datetime.now().strftime('%Y-%m-%d %H:%M:%S')}] Connecting to DESTINATION (onocoy)...")
            dst_sock = ntrip_connect(DST_URL, DST_PORT, DST_MOUNT, DST_USER, DST_PASS, is_source=False)
            
            print("\n" + "=" * 60)
            print("✓ RELAY ACTIVE - Transmitting data to onocoy...")
            print("=" * 60)
            
            # Reset counters
            bytes_sent = 0
            bytes_received = 0
            reconnect_count = 0
            
            # Start thread gửi GGA
            gga_thread = threading.Thread(target=send_gga_periodically, args=(dst_sock,), daemon=True)
            gga_thread.start()
            
            # Đợi nhận data đầu tiên
            print("Waiting for RTCM data from source...")
            first_data = True
            
            # Relay dữ liệu
            while True:
                data = src_sock.recv(BUFFER_SIZE)
                
                if not data:
                    print("\n✗ Source connection closed")
                    break
                
                bytes_received += len(data)
                
                # Hiển thị thông báo khi nhận data đầu tiên
                if first_data:
                    print(f"✓ Receiving RTCM data! ({len(data)} bytes)")
                    first_data = False
                
                # Gửi data đến onocoy
                try:
                    dst_sock.sendall(data)
                    bytes_sent += len(data)
                    last_send_time = datetime.now()
                    
                    # In thống kê mỗi 10MB
                    if bytes_sent % (10 * 1024 * 1024) < BUFFER_SIZE:
                        print(f"✓ Sent: {bytes_sent / (1024*1024):.2f} MB | "
                              f"Received: {bytes_received / (1024*1024):.2f} MB | "
                              f"Time: {last_send_time.strftime('%H:%M:%S')}")
                        
                except socket.error as e:
                    print(f"\n✗ Failed to send to onocoy: {e}")
                    break
                    
        except KeyboardInterrupt:
            print("\n\n⊗ Stopped by user")
            break
            
        except Exception as e:
            reconnect_count += 1
            print(f"\n✗ Error (attempt {reconnect_count}): {e}")
            
            if reconnect_count > 10:
                print("⚠ Too many reconnect attempts. Waiting 30 seconds...")
                time.sleep(30)
                reconnect_count = 0
            else:
                print("↻ Reconnecting in 5 seconds...")
                time.sleep(5)
                
        finally:
            # Đóng kết nối
            if src_sock:
                try: 
                    src_sock.close()
                    print("  → Source socket closed")
                except: pass
                
            if dst_sock:
                try: 
                    dst_sock.close()
                    print("  → Destination socket closed")
                except: pass

if __name__ == "__main__":
    main()
