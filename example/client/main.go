package main

import (
	"encoding/binary"
	"example/nicepb/nice"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	pnet "github.com/murang/potato/net"
)

func main() {
	// 连接到服务器
	//conn, err := kcp.Dial("localhost:10086")
	conn, err := net.Dial("tcp", "localhost:10086")
	if err != nil {
		log.Fatalf("连接服务器失败: %v", err)
	}
	defer conn.Close()
	log.Println("已连接到服务器")

	// 处理退出信号
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// 启动读取服务器消息的goroutine
	go readServerMessages(conn)

	// 主循环 - 发送消息
	for {
		select {
		case <-sigCh:
			log.Println("接收到退出信号，关闭连接")
			return
		default:
			// 构造消息
			msg := &nice.C2S_Hello{
				Name: "Potato",
			}
			codec := &pnet.PbCodec{}
			bytes, err := codec.Encode(msg)
			if err != nil {
				log.Printf("序列化消息失败: %v", err)
				return
			}

			pkt := make([]byte, 4+len(bytes))

			// Length
			binary.BigEndian.PutUint32(pkt, uint32(len(bytes)))

			// Value
			copy(pkt[4:], bytes)

			// 发送消息
			if _, err := conn.Write(pkt); err != nil {
				log.Printf("发送消息失败: %v", err)
				return
			}

			log.Printf("已发送消息: %s", bytes)

			// 等待一段时间再发送下一条消息
			time.Sleep(1 * time.Second)
		}
	}
}

// 读取服务器返回的消息
func readServerMessages(conn net.Conn) {
	buf := make([]byte, 1024)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			log.Printf("读取服务器消息失败: %v", err)
			return
		}

		codec := &pnet.PbCodec{}
		msg, err := codec.Decode(buf[4:n])
		if err != nil {
			log.Printf("反序列化消息失败: %v", err)
			return
		}

		log.Printf("收到服务器消息: %+v", msg)
	}
}
