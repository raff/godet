
[![Go Documentation](http://godoc.org/github.com/raff/godet?status.svg)](http://godoc.org/github.com/raff/godet)
[![Go Report Card](https://goreportcard.com/badge/github.com/raff/godet)](https://goreportcard.com/report/github.com/raff/godet)
[![Actions Status](https://github.com/raff/godet/workflows/Go/badge.svg)](https://github.com/raff/godet/actions)


# godet
Remote client for Chrome DevTools

## Installation

    $ go get github.com/raff/godet
    
## Documentation
http://godoc.org/github.com/raff/godet

## Example
A pretty complete example is available at [`cmd/godet/main.go`](https://github.com/raff/godet/blob/master/cmd/godet/main.go).
This example is available at [`examples/example.go`](https://github.com/raff/godet/blob/master/examples/example.go).

```go
import "github.com/raff/godet"

// connect to Chrome instance
remote, err := godet.Connect("localhost:9222", true)
if err != nil {
    fmt.Println("cannot connect to Chrome instance:", err)
    return
}

// disconnect when done
defer remote.Close()

// get browser and protocol version
version, _ := remote.Version()
fmt.Println(version)

// get list of open tabs
tabs, _ := remote.TabList("")
fmt.Println(tabs)

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

// block loading of most images
_ = remote.SetBlockedURLs("*.jpg", "*.png", "*.gif")

// create new tab
tab, _ := remote.NewTab("https://www.google.com")
fmt.Println(tab)

// enable event processing
remote.RuntimeEvents(true)
remote.NetworkEvents(true)
remote.PageEvents(true)
remote.DOMEvents(true)
remote.LogEvents(true)

// navigate in existing tab
_ = remote.ActivateTab(tabs[0])

// re-enable events when changing active tab
remote.AllEvents(true) // enable all events

_, _ = remote.Navigate("https://www.google.com")

// evaluate Javascript expression in existing context
res, _ := remote.EvaluateWrap(`
    console.log("hello from godet!")
    return 42;
`)
fmt.Println(res)

// take a screenshot
_ = remote.SaveScreenshot("screenshot.png", 0644, 0, true)

// or save page as PDF
_ = remote.SavePDF("page.pdf", 0644)

```
