package main

import (
	"github.com/gobs/args"
	"github.com/gobs/cmd"
	"github.com/gobs/cmd/plugins/controlflow"
	"github.com/gobs/cmd/plugins/json"
	"github.com/gobs/pretty"
	"github.com/gobs/simplejson"
	"github.com/raff/godet"

	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type mmap = map[string]interface{}

func timestamp() string {
	return time.Now().Format(time.RFC3339)
}

func unquote(s string) string {
	if res, err := strconv.Unquote(strings.TrimSpace(s)); err == nil {
		return res
	}

	return s
}

func printJson(v interface{}) {
	fmt.Println(simplejson.MustDumpString(v, simplejson.Indent("  ")))
}

func parseValue(v string) (interface{}, error) {
	switch {
	case strings.HasPrefix(v, "{") || strings.HasPrefix(v, "["):
		j, err := simplejson.LoadString(v)
		if err != nil {
			return nil, fmt.Errorf("error parsing %q", v)
		} else {
			return j.Data(), nil
		}

	case strings.HasPrefix(v, `"`):
		return strings.Trim(v, `"`), nil

	case strings.HasPrefix(v, `'`):
		return strings.Trim(v, `'`), nil

	case v == "":
		return v, nil

	case v == "true":
		return true, nil

	case v == "false":
		return false, nil

	case v == "null":
		return nil, nil

	default:
		if i, err := strconv.ParseInt(v, 10, 64); err == nil {
			return i, nil
		}
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, nil
		}

		return v, nil
	}
}

func runCommand(commandString string) error {
	parts := args.GetArgs(commandString)
	command := exec.Command(parts[0], parts[1:]...)
	return command.Start()
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
		fmt.Println("error getting document: ", err)
		return -1
	}

	if verbose {
		pretty.PrettyPrint(res)
	}

	doc := simplejson.AsJson(res)
	return doc.GetPath("root", "nodeId").MustInt(-1)
}

func chromeApp() (chromeapp string) {
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

		chromeapp += " --remote-debugging-port=9222 --hide-scrollbars --wbsi --disable-extensions --disable-gpu about:blank"
	}

	return
}

func main() {
	chromeapp := chromeApp()

	cmdApp := flag.String("cmd", chromeapp, "command to execute to start the browser")
	headless := flag.Bool("headless", true, "headless mode")
	port := flag.String("port", "localhost:9222", "Chrome remote debugger port")
	verbose := flag.Bool("verbose", false, "verbose logging")

	/*
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
	*/

	flag.Parse()

	if *cmdApp != "" {
		if !*headless {
			*cmdApp = strings.Replace(*cmdApp, " --headless ", " ", -1)
		}

		if err := runCommand(*cmdApp); err != nil {
			fmt.Println("cannot start browser", err)
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

		fmt.Println("connect", err)
	}

	if err != nil {
		fmt.Println("cannot connect to browser")
		return
	}

	defer remote.Close()

	v, err := remote.Version()
	if err != nil {
		fmt.Println("cannot get version: ", err)
		return
	}

	fmt.Println("connected to", v.Browser, "protocol version", v.ProtocolVersion)

	remote.CallbackEvent("Network.requestWillBeSent", func(params godet.Params) {
		req := params.Map("request")

		fmt.Println(timestamp(), "requestWillBeSent",
			params["type"],
			params["documentURL"],
			"\n\t", req["method"], req["url"])

		for k, v := range req["headers"].(mmap) {
			fmt.Printf("\t%v: %v", k, v)
		}
	})

	remote.CallbackEvent("Network.responseReceived", func(params godet.Params) {
		resp := params.Map("response")
		url := resp["url"].(string)

		fmt.Println(timestamp(), "responseReceived",
			params["type"],
			limit(url, 80),
			"\n\t\t\t",
			int(resp["status"].(float64)),
			resp["mimeType"].(string))
	})

	var interrupted bool

	commander := &cmd.Cmd{
		HistoryFile: ".godship_history",
		EnableShell: true,
		Interrupt:   func(sig os.Signal) bool { interrupted = true; return false },
	}

	commander.Init(controlflow.Plugin, json.Plugin)
	commander.SetVar("print", true)

	setResult := func(v interface{}) {
		commander.SetVar("result", json.StringJson(v, true))
		json.PrintJson(v)
	}

	commander.Add(cmd.Command{
		"version",
		`version`,
		func(line string) (stop bool) {
			v, err := remote.Version()
			if err != nil {
				fmt.Println("cannot get version: ", err)
			} else {
				setResult(v)
			}

			return
		},
		nil})

	commander.Add(cmd.Command{
		"protocol",
		`protocol`,
		func(line string) (stop bool) {
			p, err := remote.Protocol()
			if err != nil {
				fmt.Println("cannot get protocol: ", err)
			} else {
				setResult(p)
			}

			return
		},
		nil})

	commander.Add(cmd.Command{
		"tabs",
		`tabs [filter]`,
		func(line string) (stop bool) {
			tabs, err := remote.TabList(line)
			if err != nil {
				fmt.Println("cannot get list of tabs: ", err)
			} else {
				setResult(tabs)
			}

			return
		},
		nil})

	commander.Add(cmd.Command{
		"verbose",
		`verbose [true|false]`,
		func(line string) (stop bool) {
			if line != "" {
				enable, _ := strconv.ParseBool(line)
				remote.Verbose(enable)
			}

			return
		},
		nil})

	commander.Add(cmd.Command{
		"events",
		`events [true|false]`,
		func(line string) (stop bool) {
			if line != "" {
				enable, _ := strconv.ParseBool(line)
				remote.AllEvents(enable)
			}

			return
		},
		nil})

	commander.Add(cmd.Command{
		"navigate",
		`navigate url`,
		func(line string) (stop bool) {
			if line == "" {
				return
			}

			ret, err := remote.Navigate(line)
			if err != nil {
				setResult(err)
			} else {
				setResult(ret)
			}

			return
		},
		nil})

	commander.Add(cmd.Command{
		"query",
		`query selector`,
		func(line string) (stop bool) {
			id := documentNode(remote, *verbose)

			res, err := remote.QuerySelector(id, line)
			if err != nil {
				setResult(err)
				return
			}

			if res == nil {
				setResult(nil)
				return
			}

			id = int(res["nodeId"].(float64))
			res, err = remote.ResolveNode(id)
			if err != nil {
				setResult(err)
				return
			}

			setResult(res)
			return
		},
		nil})

	commander.Add(cmd.Command{
		"eval",
		`eval javascript`,
		func(line string) (stop bool) {
			res, err := remote.Evaluate(line)
			if err != nil {
				setResult(err)
				return
			}

			setResult(res)
			return
		},
		nil})

	commander.Commands["set"] = commander.Commands["var"]

	switch flag.NArg() {
	case 1: // program name only
		break

	case 2: // one arg - expect URL or @filename
		cmd := flag.Arg(0)
		if !strings.HasPrefix(cmd, "@") {
			cmd = "base " + cmd
		}

		commander.OneCmd(cmd)

	default:
		fmt.Println("usage:", os.Args[0], "[base-url]")
		return
	}

	commander.CmdLoop()
}
