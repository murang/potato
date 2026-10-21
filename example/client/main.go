package main

import (
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

			// 与服务端共用包格式，并正确处理一次 Write 未写完全部数据的情况。
			if err := pnet.WritePacket(conn, bytes); err != nil {
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
	codec := &pnet.PbCodec{}
	for {
		// TCP 不保留消息边界：一次 Read 可能只有部分长度头，也可能含多个包。
		// ReadPacket 读满当前包并限制包长，下一轮继续处理其余包。
		data, err := pnet.ReadPacket(conn)
		if err != nil {
			log.Printf("读取服务器消息失败: %v", err)
			return
		}

		msg, err := codec.Decode(data)
		if err != nil {
			log.Printf("反序列化消息失败: %v", err)
			return
		}

		log.Printf("收到服务器消息: %+v", msg)
	}
}
