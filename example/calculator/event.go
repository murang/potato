package main

import "example/nicepb/nice"

func OnEvent(evt any) {
	switch event := evt.(type) {
	case string:
		println("get event:", event)
	case *nice.EventHello:
		println("get event hello:", event.SayHello)
	}
}
