# godet
Remote client for Chrome DevTools

## Installation

    $ go get github.com/raff/godet
    
## Documentation
http://godoc.org/github.com/raff/godet

## Example
A pretty complete example is available at `cmd/godet/main.go`

```go
    import "github.com/raff/godet"

    // connect to Chrome instance
    remote, err = godet.Connect("localhost:9222", true)

    // disconnect when done
    defer remote.Close()

    // get browser and protocol version
    version, err := remote.Version()
    fmt.Println(version)

    // get list of open tabs
    tabs, err := remote.TabList("")
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
    err = remote.SetBlockedURLs("*.jpg", "*.png", "*.gif")
    
    // create new tab
    tab, err = remote.NewTab("https://www.google.com")

    // enable event processing
    remote.RuntimeEvents(true)
    remote.NetworkEvents(true)
    remote.PageEvents(true)
    remote.DOMEvents(true)
    remote.LogEvents(true)

    // navigate in existing tab
    err = remote.ActivateTab(tabs[0])

    // re-enable events when changing active tab
    remote.AllEvents(true) // enable all events

    err = remote.Navigate("https://www.google.com")

    // evaluate Javascript expression in existing context
    res, err = remote.EvaluateWrap(`
        console.log("hello from godet!")
        return 42;
    `)

    fmt.Println(res)
```
