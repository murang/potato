package log

import (
	"bytes"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func TestFullNotificationQueueDoesNotBlockLocalLog(t *testing.T) {
	queue := make(chan string, 1)
	queue <- "busy"
	var output bytes.Buffer
	logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&output), zapcore.DebugLevel), zap.Hooks(notificationHook("test", queue)))
	done := make(chan struct{})
	go func() { logger.Error("business error"); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("notification queue blocked logger")
	}
	if !bytes.Contains(output.Bytes(), []byte("business error")) {
		t.Error("local error log was lost")
	}
	if len(queue) != 1 {
		t.Error("notification queue capacity changed")
	}
}

func TestNotificationHookFiltersSeverity(t *testing.T) {
	queue := make(chan string, 2)
	hook := notificationHook("key", queue)
	_ = hook(zapcore.Entry{Level: zapcore.InfoLevel, Message: "info"})
	_ = hook(zapcore.Entry{Level: zapcore.ErrorLevel, Message: "failure"})
	if len(queue) != 1 {
		t.Fatalf("got %d notifications", len(queue))
	}
	if got := <-queue; !bytes.Contains([]byte(got), []byte("failure")) {
		t.Errorf("wrong notification: %s", got)
	}
}
