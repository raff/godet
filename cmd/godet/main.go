package main

import (
	"flag"
	"log"
	"os/exec"
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

func main() {
	cmd := flag.String("cmd", "open /Applications/Google\\ Chrome.app --args --remote-debugging-port=9222 --disable-extensions --headless about:blank", "command to execute to start the browser")
	port := flag.String("port", "localhost:9222", "Chrome remote debugger port")
	verbose := flag.Bool("verbose", false, "verbose logging")
	version := flag.Bool("version", false, "display remote devtools version")
	listtabs := flag.Bool("tabs", false, "show list of open tabs")
	filter := flag.String("filter", "page", "filter tab list")
	domains := flag.Bool("domains", false, "show list of available domains")
	requests := flag.Bool("requests", false, "show request notifications")
	responses := flag.Bool("responses", false, "show response notifications")
	console := flag.Bool("console", false, "show console messages")
	query := flag.String("query", "", "query against current document")
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

	if *version {
		v, err := remote.Version()
		if err != nil {
			log.Fatal("cannot get version: ", err)
		}

		pretty.PrettyPrint(v)
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
					log.Println(remote.GetResponseBody(req))
				}()
			}
		})
	}

	if *console {
		remote.CallbackEvent("Runtime.consoleAPICalled", func(params godet.Params) {
			args := []interface{}{"CONSOLE", params["type"]}

			for _, arg := range params["args"].([]interface{}) {
				args = append(args, arg.(map[string]interface{})["value"])
			}

			log.Println(args...)
		})
	}

	remote.RuntimeEvents(true)
	remote.NetworkEvents(true)
	remote.PageEvents(true)
	remote.DOMEvents(true)

	if flag.NArg() > 0 {
		p := flag.Arg(0)

		tabs, err := remote.TabList("page")
		if err != nil {
			log.Fatal("cannot get tabs", err)
		}

		if len(tabs) == 0 {
			_, err = remote.NewTab(p)
		} else {
			err := remote.ActivateTab(tabs[0])
			if err == nil {
				err = remote.Navigate(p)
			}
		}

		if err != nil {
			log.Println("error loading page", err)
		}
	}

	if *query != "" {
		res, err := remote.GetDocument()
		if err != nil {
			log.Fatal("error getting document", err)
		}

		doc := simplejson.AsJson(res)
		id := doc.GetPath("root", "nodeId").MustInt(-1)
		res, err = remote.QuerySelector(id, *query)
		if err != nil {
			log.Fatal("error in querySelector", err)
		}

		if res == nil {
			log.Println("no result for", *query)
		} else {
			pretty.PrettyPrint(res)
		}
	}

	time.Sleep(60 * time.Second)
	log.Println("Closing")
}
