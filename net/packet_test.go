package net

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/murang/potato/pb"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func init() {
	pb.RegisterMsg(0xf0d1, reflect.TypeOf(&wrapperspb.StringValue{}))
	pb.RegisterMsgPair(0xf0d2, reflect.TypeOf(&wrapperspb.Int32Value{}), nil)
}

func TestProtobufRejectsShortMessageID(t *testing.T) {
	for name, codec := range map[string]ICodec{"pb": &PbCodec{}, "pair": &PbPairCodec{}} {
		for size := 0; size < lenMsgId; size++ {
			t.Run(fmt.Sprintf("%s/%d", name, size), func(t *testing.T) {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("short packet panicked: %v", r)
					}
				}()
				if _, err := codec.Decode(make([]byte, size)); !errors.Is(err, ErrMinPacket) {
					t.Fatalf("got %v, want ErrMinPacket", err)
				}
			})
		}
	}
}

func TestPacketSizeBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, maxPackSize, maxPackSize + 1} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			body := bytes.Repeat([]byte{7}, size)
			var wire bytes.Buffer
			err := WritePacket(&wire, body)
			if size > maxPackSize {
				if !errors.Is(err, ErrMaxPacket) || wire.Len() != 0 {
					t.Fatalf("oversized write: error=%v, bytes=%d", err, wire.Len())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := ReadPacket(&wire)
			if err != nil || !bytes.Equal(got, body) {
				t.Fatalf("round trip: len=%d, error=%v", len(got), err)
			}
		})
	}
}

func FuzzProtobufDecode(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0})
	f.Add([]byte{0, 0, 0, 1, 0xff})
	f.Add([]byte{0, 0, 0xf0, 0xd1, 0x0a, 1, 'x'})
	f.Add([]byte{0, 0, 0xf0, 0xd2, 0x08, 1})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = (&PbCodec{}).Decode(data)
		_, _ = (&PbPairCodec{}).Decode(data)
	})
}

func FuzzReadPacket(f *testing.F) {
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 1, 7})
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, maxPackSize+1)
	f.Add(header)
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := ReadPacket(bytes.NewReader(data))
		if err == nil && len(got) > maxPackSize {
			t.Fatal("packet exceeded the limit")
		}
	})
}
