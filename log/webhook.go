package log

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"net/http"
	"os"
	"time"
)

type Notification struct {
	MsgType string               `json:"msg_type"`
	Content *NotificationContent `json:"content"`
}

type NotificationContent struct {
	Text string `json:"text"`
}

// SetErrorNotification 设置错误通知
// url: webhook url
// keyword: 通知关键字 用于机器人过滤信息
func SetErrorNotification(url, keyword string) {
	errCh := make(chan string, 64)
	Logger = Logger.WithOptions(zap.Hooks(notificationHook(keyword, errCh)))
	Sugar = Logger.Sugar()
	go runPushNotification(url, errCh)
}

// 通知采用尽力投递：队列满时丢弃通知，普通日志仍由 zap core 正常记录。
func notificationHook(keyword string, queue chan<- string) func(zapcore.Entry) error {
	executable, _ := os.Executable()
	return func(entry zapcore.Entry) error {
		if entry.Level >= zap.ErrorLevel {
			msg := fmt.Sprintf("[Error]%s %s\n%s\n%s\n%s", keyword, entry.Time.Format("2006-01-02 15:04:05.000"), executable, entry.Message, entry.Stack)
			select {
			case queue <- msg:
			default:
			}
		}
		return nil
	}
}

// 推送进程
func runPushNotification(url string, ch chan string) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "notification worker panic: %v\n", r)
		}
	}()
	client := &http.Client{
		Timeout: 3 * time.Second,
	}
	for msg := range ch {
		ntf := &Notification{
			MsgType: "text",
			Content: &NotificationContent{Text: msg},
		}
		data, _ := json.Marshal(ntf)
		contentType := "application/json"
		resp, err := client.Post(url, contentType, bytes.NewReader(data))
		if err != nil {
			fmt.Printf("runPushNotification: %s error: %v\n", msg, err)
		} else {
			_ = resp.Body.Close()
		}
	}
}
