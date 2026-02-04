package main

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"time"
)

// Demo kết nối đến NTRIP Caster miễn phí
func main() {
	// Thông tin caster
	host := "14.225.255.95"
	port := 2101
	mountPoint := "6801"
	user := "guest"     // Thử với user phổ biến
	pass := "guest123"  // Thử với pass phổ biến

	log.Printf("Đang kết nối đến %s:%d/%s...", host, port, mountPoint)

	// 1. Kết nối TCP
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", host, port), 15*time.Second)
	if err != nil {
		log.Fatalf("Lỗi kết nối: %v", err)
	}
	defer conn.Close()
	log.Println("✓ Kết nối TCP thành công!")

	// 2. Tạo HTTP GET request theo chuẩn NTRIP
	auth := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
	request := fmt.Sprintf(
		"GET /%s HTTP/1.1\r\n"+
			"Host: %s\r\n"+
			"Ntrip-Version: Ntrip/2.0\r\n"+
			"User-Agent: NTRIP DemoClient/1.0\r\n"+
			"Authorization: Basic %s\r\n"+
			"Connection: close\r\n"+
			"\r\n",
		mountPoint, host, auth)

	// 3. Gửi request
	if _, err := conn.Write([]byte(request)); err != nil {
		log.Fatalf("Lỗi gửi request: %v", err)
	}
	log.Println("✓ Đã gửi NTRIP request")

	// 4. Đọc response header
	reader := bufio.NewReader(conn)
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("Lỗi đọc response: %v", err)
	}

	log.Printf("Server response: %s", statusLine)

	// Đọc hết các header còn lại
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		fmt.Print(line)
		if line == "\r\n" || line == "\n" {
			break // Hết header
		}
	}

	// 5. Kiểm tra kết quả
	if contains(statusLine, "200 OK") || contains(statusLine, "ICY 200") {
		log.Println("\n✓✓✓ KẾT NỐI THÀNH CÔNG! ✓✓✓")
		log.Println("Bắt đầu nhận dữ liệu RTCM...")

		// Đọc thử 10 giây dữ liệu
		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		buf := make([]byte, 4096)
		totalBytes := 0

		for i := 0; i < 20; i++ { // Đọc 20 lần
			n, err := reader.Read(buf)
			if err != nil {
				if err == io.EOF {
					log.Println("Server đóng kết nối")
				} else {
					log.Printf("Lỗi đọc: %v", err)
				}
				break
			}
			totalBytes += n
			log.Printf("Nhận được %d bytes (Tổng: %d bytes)", n, totalBytes)
		}

		log.Printf("\n✓ Tổng dữ liệu nhận được: %d bytes", totalBytes)
		if totalBytes > 0 {
			log.Println("✓ Caster hoạt động bình thường!")
		} else {
			log.Println("⚠ Không nhận được dữ liệu. Có thể caster đang offline hoặc không có trạm phát.")
		}
	} else {
		log.Printf("✗ KẾT NỐI THẤT BẠI: %s", statusLine)
		log.Println("\nCác nguyên nhân có thể:")
		log.Println("  - Mountpoint không tồn tại")
		log.Println("  - Caster yêu cầu xác thực")
		log.Println("  - Server offline")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s[:len(substr)] == substr || 
		len(s) > len(substr) && s[len(s)-len(substr):] == substr ||
		findInString(s, substr))
}

func findInString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
