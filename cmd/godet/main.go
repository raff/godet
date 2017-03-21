package main

import (
	"flag"
	"fmt"
	"log"
	"os/exec"
	"runtime"
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
    } else {
        return s
    }
}

func main() {
	var chromeapp string

	switch runtime.GOOS {
	case "darwin":
		chromeapp = "open /Applications/Google\\ Chrome.app --args"

	case "linux":
		for _, c := range []string{"chromium",
			"google-chrome-beta",
			"google-chrome-unstable",
			"google-chrome-stable"} {
			if _, err := exec.LookPath(c); err == nil {
				chromeapp = c
			}
		}

	case "windows":
	}

	if chromeapp != "" {
		chromeapp += " --remote-debugging-port=9222 --disable-extensions --disable-gpu --headless about:blank"
	}

	cmd := flag.String("cmd", chromeapp, "command to execute to start the browser")
	port := flag.String("port", "localhost:9222", "Chrome remote debugger port")
	verbose := flag.Bool("verbose", false, "verbose logging")
	version := flag.Bool("version", false, "display remote devtools version")
	listtabs := flag.Bool("tabs", false, "show list of open tabs")
	filter := flag.String("filter", "page", "filter tab list")
	domains := flag.Bool("domains", false, "show list of available domains")
	requests := flag.Bool("requests", false, "show request notifications")
	responses := flag.Bool("responses", false, "show response notifications")
	allEvents := flag.Bool("all-events", false, "enable all events")
	logev := flag.Bool("log", false, "show log/console messages")
	query := flag.String("query", "", "query against current document")
	eval := flag.String("eval", "", "evaluate expression")
	screenshot := flag.Bool("screenshot", false, "take a screenshot")
	flag.Parse()

	if *cmd != "" {
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

	v, err := remote.Version()
	if err != nil {
		log.Fatal("cannot get version: ", err)
	}

	if *version {
		pretty.PrettyPrint(v)
	} else {
		log.Println("connected to", v.Browser, ", protocol v.", v.ProtocolVersion)
	}

	if *listtabs {
		tabs, err := remote.TabList(*filter)
		if err != nil {
			log.Fatal("cannot get list of tabs: ", err)
		}

		pretty.PrettyPrint(tabs)
	}

	if *domains {
		d, err := remote.GetDomains()
		if err != nil {
			log.Fatal("cannot get domains: ", err)
		}

		pretty.PrettyPrint(d)
	}

	remote.CallbackEvent(godet.EventClosed, func(params godet.Params) {
		log.Println("RemoteDebugger connection terminated.")
		done <- true
	})

	if *requests {
		remote.CallbackEvent("Network.requestWillBeSent", func(params godet.Params) {
			log.Println("requestWillBeSent",
				params["type"],
				params["documentURL"],
				params["request"].(map[string]interface{})["url"])
		})
	}

	if *responses {
		remote.CallbackEvent("Network.responseReceived", func(params godet.Params) {
			log.Println("responseReceived",
				params["type"],
				params["response"].(map[string]interface{})["url"])

			if params["type"].(string) == "Image" {
				go func() {
					req := params["requestId"].(string)
					res, err := remote.GetResponseBody(req)
					if err != nil {
						log.Println("Error getting responseBody", err)
					} else {
						log.Println("ResponseBody", len(res), limit(string(res), 10))
					}
				}()
			}
		})
	}

	if *logev {
		remote.CallbackEvent("Log.entryAdded", func(params godet.Params) {
			entry := params["entry"].(map[string]interface{})
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

	if *screenshot {
                remote.CallbackEvent("DOM.documentUpdated", func(params godet.Params) {
                        log.Println("document updated. taking screenshot...")
		        remote.SaveScreenshot("screenshot.png", 0644, 0, false)
                })
	}


	if *allEvents {
		remote.AllEvents(true)
	} else {
		remote.RuntimeEvents(true)
		remote.NetworkEvents(true)
		remote.PageEvents(true)
		remote.DOMEvents(true)
		remote.LogEvents(true)
	}

	if flag.NArg() > 0 {
		p := flag.Arg(0)

		tabs, err := remote.TabList("page")
		if err != nil {
			log.Fatal("cannot get tabs: ", err)
		}

		if len(tabs) == 0 {
			_, err = remote.NewTab(p)
		} else {
			err := remote.ActivateTab(tabs[0])
			if err == nil {
				_, err = remote.Navigate(p)
			}
		}

		if err != nil {
			log.Println("error loading page", err)
		}
	}

	if *query != "" {
		res, err := remote.GetDocument()
		if err != nil {
			log.Fatal("error getting document: ", err)
		}

		if *verbose {
			pretty.PrettyPrint(res)
		}

		doc := simplejson.AsJson(res)
		id := doc.GetPath("root", "nodeId").MustInt(-1)
		res, err = remote.QuerySelector(id, *query)
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
	}

	if *eval != "" {
		res, err := remote.EvaluateWrap(*eval)
		if err != nil {
			log.Fatal("error in evaluate: ", err)
		}

		pretty.PrettyPrint(res)
	}

	<-done
	log.Println("Closing")
}
