package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/gobs/args"
	"github.com/gobs/pretty"
	"github.com/gobs/simplejson"
	"github.com/raff/godet"
)

func runCommand(commandString string) error {
	parts := args.GetArgs(commandString)
	cmd := exec.Command(parts[0], parts[1:]...)
	return cmd.Start()
}

func limit(s string, l int) string {
	if len(s) > l {
		return s[:l] + "..."
	}
	return s
}

func documentNode(remote *godet.RemoteDebugger, verbose bool) int {
	res, err := remote.GetDocument()
	if err != nil {
		log.Fatal("error getting document: ", err)
	}

	if verbose {
		pretty.PrettyPrint(res)
	}

	doc := simplejson.AsJson(res)
	return doc.GetPath("root", "nodeId").MustInt(-1)
}

func main() {
	var chromeapp string

	switch runtime.GOOS {
	case "darwin":
		for _, c := range []string{
			"/Applications/Google Chrome Canary.app",
			"/Applications/Google Chrome.app",
		} {
			// MacOS apps are actually folders
			if info, err := os.Stat(c); err == nil && info.IsDir() {
				chromeapp = fmt.Sprintf("open %q --args", c)
				break
			}
		}

	case "linux":
		for _, c := range []string{
			"headless_shell",
			"chromium",
			"google-chrome-beta",
			"google-chrome-unstable",
			"google-chrome-stable"} {
			if _, err := exec.LookPath(c); err == nil {
				chromeapp = c
				break
			}
		}

	case "windows":
	}

	if chromeapp != "" {
		if chromeapp == "headless_shell" {
			chromeapp += " --no-sandbox"
		} else {
			chromeapp += " --headless"
		}

		chromeapp += " --remote-debugging-port=9222 --hide-scrollbars --bwsi --disable-extensions --disable-gpu about:blank"
	}

	cmd := flag.String("cmd", chromeapp, "command to execute to start the browser")
	headless := flag.Bool("headless", true, "headless mode")
	port := flag.String("port", "localhost:9222", "Chrome remote debugger port")
	verbose := flag.Bool("verbose", false, "verbose logging")
	version := flag.Bool("version", false, "display remote devtools version")
	protocol := flag.Bool("protocol", false, "display the DevTools protocol")
	listtabs := flag.Bool("tabs", false, "show list of open tabs")
	seltab := flag.Int("tab", 0, "select specified tab if available")
	newtab := flag.Bool("new", false, "always open a new tab")
	history := flag.Bool("history", false, "display page history")
	filter := flag.String("filter", "page", "filter tab list")
	domains := flag.Bool("domains", false, "show list of available domains")
	requests := flag.Bool("requests", false, "show request notifications")
	responses := flag.Bool("responses", false, "show response notifications")
	allEvents := flag.Bool("all-events", false, "enable all events")
	logev := flag.Bool("log", false, "show log/console messages")
	query := flag.String("query", "", "query against current document")
	eval := flag.String("eval", "", "evaluate expression")
	screenshot := flag.Bool("screenshot", false, "take a screenshot")
	pdf := flag.Bool("pdf", false, "save current page as PDF")
	control := flag.String("control", "", "control navigation (proceed,cancel,cancelIgnore)")
	block := flag.String("block", "", "block specified URLs or pattenrs. Use '|' as separator")
	intercept := flag.String("intercept", "", "enable request interception and respond according to request type - use type:response,type:response,...\n\t  type:[Document,Stylesheet,Image,Media,Font,Script,TextTrack,XHR,Fetch,EventSource,WebSocket,Manifest,Other]\n\t  response:[Failed,Aborted,TimedOut,AccessDenied,ConnectionClosed,ConnectionReset,ConnectionRefused,ConnectionAborted,ConnectionFailed,NameNotResolved,InternetDisconnected,AddressUnreachable]")
	html := flag.Bool("html", false, "get outer HTML for current page")
	setHTML := flag.String("set-html", "", "set outer HTML for current page")
	wait := flag.Bool("wait", false, "wait for more events")
	box := flag.Bool("box", false, "get box model for document")
	styles := flag.Bool("styles", false, "get computed style for document")
	pause := flag.Duration("pause", 5*time.Second, "wait this amount of time before proceeding")
	close := flag.Bool("close", false, "gracefully close browser")
	getCookies := flag.Bool("cookies", false, "get cookies for current page")
	getAllCookies := flag.Bool("all-cookies", false, "get all cookies for current page")
	flag.Parse()

	if *cmd != "" {
		if !*headless {
			*cmd = strings.Replace(*cmd, " --headless ", " ", -1)
		}

		if err := runCommand(*cmd); err != nil {
			log.Println("cannot start browser", err)
		}
	}

	var remote *godet.RemoteDebugger
	var err error

	for i := 0; i < 10; i++ {
		if i > 0 {
			time.Sleep(500 * time.Millisecond)
		}

		remote, err = godet.Connect(*port, *verbose)
		if err == nil {
			break
		}

		log.Println("connect", err)
	}

	if err != nil {
		log.Fatal("cannot connect to browser")
	}

	defer remote.Close()

	done := make(chan bool)
	shouldWait := *wait

	var pwait chan bool

	v, err := remote.Version()
	if err != nil {
		log.Fatal("cannot get version: ", err)
	}

	if *version {
		pretty.PrettyPrint(v)
	} else {
		log.Println("connected to", v.Browser, "protocol version", v.ProtocolVersion)
	}

	if *protocol {
		p, err := remote.Protocol()
		if err != nil {
			log.Fatal("cannot get protocol: ", err)
		}

		pretty.PrettyPrint(p)
		shouldWait = false
	}

	if *listtabs {
		tabs, err := remote.TabList(*filter)
		if err != nil {
			log.Fatal("cannot get list of tabs: ", err)
		}

		pretty.PrettyPrint(tabs)
		shouldWait = false
	}

	if *domains {
		d, err := remote.GetDomains()
		if err != nil {
			log.Fatal("cannot get domains: ", err)
		}

		pretty.PrettyPrint(d)
		shouldWait = false
	}

	if *history {
		curr, entries, err := remote.GetNavigationHistory()
		if err != nil {
			log.Fatal("cannot get history: ", err)
		}

		fmt.Println("current entry:", curr)
		pretty.PrettyPrint(entries)
		shouldWait = false
	}

	remote.CallbackEvent(godet.EventClosed, func(params godet.Params) {
		log.Println("RemoteDebugger connection terminated.")
		done <- true
	})

	remote.CallbackEvent("Emulation.virtualTimeBudgetExpired", func(params godet.Params) {
		pwait <- true
	})

	if *requests {
		remote.CallbackEvent("Network.requestWillBeSent", func(params godet.Params) {
			log.Println("requestWillBeSent",
				params["type"],
				params["documentURL"],
				params.Map("request")["url"])
		})
	}

	if *responses {
		remote.CallbackEvent("Network.responseReceived", func(params godet.Params) {
			resp := params.Map("response")
			url := resp["url"].(string)

			log.Println("responseReceived",
				params["type"],
				limit(url, 80),
				"\n\t\t\t",
				int(resp["status"].(float64)),
				resp["mimeType"].(string))

			/*
				if params.String("type") == "Image" {
					go func() {
						req := params.String("requestId")
						res, err := remote.GetResponseBody(req)
						if err != nil {
							log.Println("Error getting responseBody", err)
						} else {
							log.Println("ResponseBody", len(res), limit(string(res), 10))
						}
					}()
				}
			*/
		})
	}

	if *logev {
		remote.CallbackEvent("Log.entryAdded", func(params godet.Params) {
			entry := params.Map("entry")
			log.Println("LOG", entry["type"], entry["level"], entry["text"])
		})

		remote.CallbackEvent("Runtime.consoleAPICalled", func(params godet.Params) {
			l := []interface{}{"CONSOLE", params["type"].(string)}

			for _, a := range params["args"].([]interface{}) {
				arg := a.(map[string]interface{})

				if arg["value"] != nil {
					l = append(l, arg["value"])
				} else if arg["preview"] != nil {
					arg := arg["preview"].(map[string]interface{})

					v := arg["description"].(string) + "{"

					for i, p := range arg["properties"].([]interface{}) {
						if i > 0 {
							v += ", "
						}

						prop := p.(map[string]interface{})
						if prop["name"] != nil {
							v += fmt.Sprintf("%q: ", prop["name"])
						}

						v += fmt.Sprintf("%v", prop["value"])
					}

					v += "}"
					l = append(l, v)
				} else {
					l = append(l, arg["type"].(string))
				}

			}

			log.Println(l...)
		})
	}

	if *block != "" {
		blocks := strings.Split(*block, "|")
		remote.SetBlockedURLs(blocks...)
	}

	var site string

	if flag.NArg() > 0 {
		site = flag.Arg(0)

		tabs, err := remote.TabList("page")
		if err != nil {
			log.Fatal("cannot get tabs: ", err)
		}

		if len(tabs) == 0 || *newtab {
			_, err = remote.NewTab(site)
			site = ""
		} else {
			tab := *seltab
			if tab > len(tabs) {
				tab = 0
			}

			err = remote.ActivateTab(tabs[tab])
		}

		if err != nil {
			log.Fatal("error loading page: ", err)
		}
	}

	//
	// enable events AFTER creating/selecting a tab but BEFORE navigating to a page
	//
	if *allEvents {
		remote.AllEvents(true)
	} else {
		remote.RuntimeEvents(true)
		remote.NetworkEvents(true)
		remote.PageEvents(true)
		remote.DOMEvents(true)
		remote.LogEvents(true)
		remote.EmulationEvents(true)
	}

	if *control != "" {
		remote.SetControlNavigations(true)
		navigationResponse := godet.NavigationProceed

		switch *control {
		case "proceed":
			navigationResponse = godet.NavigationProceed
		case "cancel":
			navigationResponse = godet.NavigationCancel
		case "cancelIgnore":
			navigationResponse = godet.NavigationCancelAndIgnore
		}

		remote.CallbackEvent("Page.navigationRequested", func(params godet.Params) {
			log.Println("navigation requested for", params.String("url"), navigationResponse)

			remote.ProcessNavigation(params.Int("navigationId"), navigationResponse)
		})
	}

	if *intercept != "" {
		remote.EnableRequestInterception(true)
		responses := map[string]string{}

		if strings.Contains(*intercept, ":") { // type:response
			matches := regexp.MustCompile(`(\w+):(\w+),?`).FindAllStringSubmatch(*intercept, -1)

			for _, m := range matches {
				responses[m[1]] = m[2]
			}
		} // else, we just log the intercept requests

		remote.CallbackEvent("Network.requestIntercepted", func(params godet.Params) {
			iid := params.String("interceptionId")
			rtype := params.String("resourceType")
			reason := responses[rtype]

			log.Println("request intercepted for", iid, rtype, params.Map("request")["url"])
			if reason != "" {
				log.Println("  abort with reason", reason)
			}

			remote.ContinueInterceptedRequest(iid, godet.ErrorReason(reason), "", "", "", "", nil)
		})
	}

	if *pause > 0 {
		pwait = make(chan bool)

		remote.SetVirtualTimePolicy(godet.VirtualTimePolicyPauseIfNetworkFetchesPending,
			int(*pause/time.Millisecond))
	}

	if len(site) > 0 {
		_, err = remote.Navigate(site)
		if err != nil {
			log.Fatal("error loading page: ", err)
		}
	}

	if pwait != nil {
		fmt.Println("Pause", *pause)
		<-pwait
	}

	if *query != "" {
		id := documentNode(remote, *verbose)

		res, err := remote.QuerySelector(id, *query)
		if err != nil {
			log.Fatal("error in querySelector: ", err)
		}

		if res == nil {
			log.Println("no result for", *query)
		} else {
			id = int(res["nodeId"].(float64))
			res, err = remote.ResolveNode(id)
			if err != nil {
				log.Fatal("error in resolveNode: ", err)
			}

			pretty.PrettyPrint(res)
		}

		shouldWait = false
	}

	if *eval != "" {
		res, err := remote.EvaluateWrap(*eval)
		if err != nil {
			log.Fatal("error in evaluate: ", err)
		}

		pretty.PrettyPrint(res)
		shouldWait = false
	}

	if *setHTML != "" {
		id := documentNode(remote, *verbose)

		res, err := remote.QuerySelector(id, "html")
		if err != nil {
			log.Fatal("error in querySelector: ", err)
		}

		id = int(res["nodeId"].(float64))

		err = remote.SetOuterHTML(id, *setHTML)
		if err != nil {
			log.Fatal("error in setOuterHTML: ", err)
		}

		shouldWait = false
	}

	if *html {
		id := documentNode(remote, *verbose)

		res, err := remote.GetOuterHTML(id)
		if err != nil {
			log.Fatal("error in getOuterHTML: ", err)
		}

		log.Println(res)
		shouldWait = false
	}

	if *box {
		id := documentNode(remote, *verbose)

		res, err := remote.QuerySelector(id, "html")
		if err != nil {
			log.Fatal("error in querySelector: ", err)
		}

		id = int(res["nodeId"].(float64))

		res, err = remote.GetBoxModel(id)
		if err != nil {
			log.Fatal("error in getBoxModel: ", err)
		}

		pretty.PrettyPrint(res)
		shouldWait = false
	}

	if *styles {
		id := documentNode(remote, *verbose)

		res, err := remote.QuerySelector(id, "html")
		if err != nil {
			log.Fatal("error in querySelector: ", err)
		}

		id = int(res["nodeId"].(float64))

		res, err = remote.GetComputedStyleForNode(id)
		if err != nil {
			log.Fatal("error in getComputedStyleForNode: ", err)
		}

		pretty.PrettyPrint(res)
		shouldWait = false
	}

	if *screenshot {
		id := documentNode(remote, *verbose)

		res, err := remote.QuerySelector(id, "html")
		if err != nil {
			log.Fatal("error in querySelector: ", err)
		}

		id = int(res["nodeId"].(float64))

		res, err = remote.GetBoxModel(id)
		if err != nil {
			log.Fatal("error in getBoxModel: ", err)
		}

		if res == nil {
			log.Println("BoxModel not available")
		} else {
			res = res["model"].(map[string]interface{})
			width := int(res["width"].(float64))
			height := int(res["height"].(float64))

			err = remote.SetVisibleSize(width, height)
			if err != nil {
				log.Fatal("error in setVisibleSize: ", err)
			}
		}

		remote.SaveScreenshot("screenshot.png", 0644, 0, true)
		shouldWait = false
	}

	if *getCookies {
		cookies, err := remote.GetCookies(nil)
		if err != nil {
			log.Println("error getting cookies:", err)
		} else {
			pretty.PrettyPrint(cookies)
		}
		shouldWait = false
	}

	if *getAllCookies {
		cookies, err := remote.GetAllCookies()
		if err != nil {
			log.Println("error getting cookies:", err)
		} else {
			pretty.PrettyPrint(cookies)
		}
		shouldWait = false
	}

	if *pdf {
		remote.SavePDF("page.pdf", 0644)
		shouldWait = false
	}

	if *close {
		remote.CloseBrowser()
	}

	if *wait || shouldWait {
		log.Println("Wait for events...")
		<-done
	}

	log.Println("Closing")
}
