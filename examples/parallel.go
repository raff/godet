//go:build ignore
// +build ignore

package main

import (
	"fmt"
	"github.com/raff/godet"
	"sync"
	"time"
)

var (
	urlList = []string{
		"https://github.com",
		"https://github.com/gobs/httpclient",
		"https://github.com/gobs/simplejson",
		"https://github.com/gobs/jsonpath",
		"https://github.com/gobs/cmd",
		"https://github.com/raff/godet",
		"https://github.com/raff/glin",
		"https://github.com/raff/goble",
		"https://github.com/raff/elseql",
		"https://github.com/raff/statemachine",
		"https://github.com/raff/zipsaver",
		"https://github.com/raff/walkngo",
	}
)

func processPage(id int, url string) {
	var remote *godet.RemoteDebugger
	var err error

	//
	// the Connect may temporary fail so retry a few times
	//
	for i := 0; i < 10; i++ {
		if i > 0 {
			time.Sleep(500 * time.Millisecond)
		}

		remote, err = godet.Connect("localhost:9222", false)
		if err == nil {
			break
		}

		fmt.Println(id, "connect", err)
	}

	if err != nil {
		fmt.Println(id, "cannot connect to browser")
		return
	}

	fmt.Println(id, "connected")
	defer remote.Close()

	done := make(chan bool)

	//
	// this should wait until the page request has loaded (if the page has multiple frames there
	// may be more "frameStoppedLoading" events and the check should be more complicated)
	//
	remote.CallbackEvent("Page.frameStoppedLoading", func(params godet.Params) {
		fmt.Println(id, "page loaded", params)
		done <- true
	})

	tab, err := remote.NewTab(url)
	if err != nil {
		fmt.Println(id, "cannot create tab:", err)
		return
	}

	defer func() {
		remote.CloseTab(tab)
		fmt.Println(id, "done")
	}()

	// events needs to be associated to current tab (enable AFTER NewTab)
	remote.PageEvents(true)

	_ = <-done

	// here the page should be ready
	// add code to process content or take screenshot

	filename := fmt.Sprintf("%d.png", id)
	remote.SaveScreenshot(filename, 0644, 0, true)
}

func main() {
	var wg sync.WaitGroup

	for x := 0; x < 10; x++ {

		// now open new list
		for p := range urlList {
			wg.Add(1)
			go func(page int) {
				id := x*100 + page
				processPage(id, urlList[page])
				wg.Done()
			}(p)
		}

		wg.Wait()
		fmt.Println(x, "------------------------------------------")
	}

	fmt.Println("DONE.")
}
