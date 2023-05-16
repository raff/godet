//go:build ignore
// +build ignore

package main

import "fmt"
import "time"

import "github.com/raff/godet"

func main() {
	// connect to Chrome instance
	remote, _ := godet.Connect("localhost:9222", false)

	// disconnect when done
	defer remote.Close()

	// get browser and protocol version
	version, _ := remote.Version()
	fmt.Println(version)

	// install some callbacks
	remote.CallbackEvent(godet.EventClosed, func(params godet.Params) {
		fmt.Println("RemoteDebugger connection terminated.")
	})

	remote.CallbackEvent("Network.requestWillBeSent", func(params godet.Params) {
		fmt.Println("requestWillBeSent",
			params["type"],
			params["documentURL"],
			params["request"].(map[string]interface{})["url"])
	})

	remote.CallbackEvent("Network.responseReceived", func(params godet.Params) {
		fmt.Println("responseReceived",
			params["type"],
			params["response"].(map[string]interface{})["url"])
	})

	remote.CallbackEvent("Log.entryAdded", func(params godet.Params) {
		entry := params["entry"].(map[string]interface{})
		fmt.Println("LOG", entry["type"], entry["level"], entry["text"])
	})

	// enable event processing
	remote.RuntimeEvents(true)
	remote.NetworkEvents(true)
	remote.PageEvents(true)
	remote.DOMEvents(true)
	remote.LogEvents(true)

	// Navigate to mobile site
	remote.Navigate("https://search.google.com/test/mobile-friendly")

	remote.SetVisibleSize(375, 667)                           // iPhone 7
	remote.SetDeviceMetricsOverride(375, 667, 3, true, false) // iPhone 7

	time.Sleep(time.Second * 3)

	// take a screenshot
	remote.SaveScreenshot("mobile.png", 0644, 0, true)
}
