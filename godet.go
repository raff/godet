package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/gobs/args"
	"github.com/gobs/httpclient"
	"github.com/gobs/pretty"
	"github.com/gorilla/websocket"
)

func decode(resp *httpclient.HttpResponse, v interface{}) error {
	err := json.NewDecoder(resp.Body).Decode(v)
	resp.Close()

	return err
}

func unmarshal(payload []byte) (map[string]interface{}, error) {
	var response map[string]interface{}
	err := json.Unmarshal(payload, &response)
	if err != nil {
		log.Println("error unmarshaling", string(payload), len(payload), err)
	}
	return response, err
}

//
// DevTools version info
//
type Version struct {
	Browser         string `json:"Browser"`
	ProtocolVersion string `json:"Protocol-Version"`
	UserAgent       string `json:"User-Agent"`
	V8Version       string `json:"V8-Version"`
	WebKitVersion   string `json:"WebKit-Version"`
}

//
// Chrome open tab or page
//
type Tab struct {
	Id          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Title       string `json:"title"`
	Url         string `json:"url"`
	WsUrl       string `json:"webSocketDebuggerUrl"`
	DevUrl      string `json:"devtoolsFrontendUrl"`
}

//
// RemoteDebugger
//
type RemoteDebugger struct {
	http    *httpclient.HttpClient
	ws      *websocket.Conn
	reqid   int
	verbose bool

	sync.Mutex
	closed chan bool

	requests  chan Params
	responses map[int]chan json.RawMessage
	callbacks map[string]EventCallback
	events    chan wsMessage
}

type Params map[string]interface{}
type EventCallback func(params Params)

//
// Connect to the remote debugger and return `RemoteDebugger` object
//
func Connect(port string, verbose bool) (*RemoteDebugger, error) {
	remote := &RemoteDebugger{
		http:      httpclient.NewHttpClient("http://" + port),
		requests:  make(chan Params),
		responses: map[int]chan json.RawMessage{},
		callbacks: map[string]EventCallback{},
		events:    make(chan wsMessage, 256),
		closed:    make(chan bool),
		verbose:   verbose,
	}

	remote.http.Verbose = verbose

	// check http connection
	tabs, err := remote.TabList("")
	if err != nil {
		return nil, err
	}

	var wsUrl string

	for _, tab := range tabs {
		if tab.WsUrl != "" {
			wsUrl = tab.WsUrl
			break
		}
	}

	// check websocket connection
	if remote.ws, _, err = websocket.DefaultDialer.Dial(wsUrl, nil); err != nil {
		return nil, err
	}

	go remote.readMessages()
	go remote.sendMessages()
	go remote.processEvents()
	return remote, nil
}

func (remote *RemoteDebugger) Close() error {
	close(remote.closed)
	err := remote.ws.Close()
	return err
}

type wsMessage struct {
	Id     int             `json:"id"`
	Result json.RawMessage `json:"result"`

	Method string          `json:"Method"`
	Params json.RawMessage `json:"Params"`
}

func (remote *RemoteDebugger) sendRequest(method string, params Params) (map[string]interface{}, error) {
	remote.Lock()
	reqid := remote.reqid
	remote.responses[reqid] = make(chan json.RawMessage, 1)
	remote.reqid++
	remote.Unlock()

	command := Params{
		"id":     reqid,
		"method": method,
		"params": params,
	}

	remote.requests <- command

	reply := <-remote.responses[reqid]
	remote.Lock()
	remote.responses[reqid] = nil
	remote.Unlock()

	if reply != nil {
		return unmarshal(reply)
	}

	return nil, nil
}

func (remote *RemoteDebugger) sendMessages() {
	for message := range remote.requests {
		bytes, err := json.Marshal(message)
		if err != nil {
			log.Println("error marshaling message", err)
			continue
		}

		if remote.verbose {
			log.Println("SEND", string(bytes))
		}

		err = remote.ws.WriteMessage(websocket.TextMessage, bytes)
		if err != nil {
			log.Println("error sending message", err)
		}
	}
}

func (remote *RemoteDebugger) readMessages() {
loop:
	for {
		select {
		case <-remote.closed:
			break loop

		default:
			_, bytes, err := remote.ws.ReadMessage()
			if err != nil {
				log.Println("read error", err)
				if websocket.IsUnexpectedCloseError(err) {
					break loop
				}
			} else {
				var message wsMessage

				//
				// unmarshall message
				//
				if err := json.Unmarshal(bytes, &message); err != nil {
					log.Println("error unmarshaling", string(bytes), len(bytes), err)
				} else if message.Method != "" {
					if remote.verbose {
						log.Println("EVENT", message.Method, string(message.Params))
						log.Println(len(remote.events))
					}

					select {
					case remote.events <- message:

					case <-remote.closed:
						break loop
					}
				} else {
					//
					// should be a method reply
					//
					if remote.verbose {
						log.Println("REPLY", message.Id, string(message.Result))
					}

					remote.Lock()
					ch := remote.responses[message.Id]
					remote.Unlock()

					if ch != nil {
						ch <- message.Result
					}
				}
			}
		}
	}

	close(remote.events)
}

func (remote *RemoteDebugger) processEvents() {
	for ev := range remote.events {
		remote.Lock()
		cb := remote.callbacks[ev.Method]
		remote.Unlock()

		if cb != nil {
			var params Params
			if err := json.Unmarshal(ev.Params, &params); err != nil {
				log.Println("error unmarshaling", string(ev.Params), len(ev.Params), err)
			} else {
				cb(params)
			}
		}
	}
}

//
// Return various version info (protocol, browser, etc.)
//
func (remote *RemoteDebugger) Version() (*Version, error) {
	resp, err := remote.http.Get("/json/version", nil, nil)
	if err != nil {
		return nil, err
	}

	var version Version

	if err = decode(resp, &version); err != nil {
		return nil, err
	}

	return &version, nil
}

//
// Return the list of open tabs/page
//
// If filter is not empty only tabs of the specified type are returned (i.e. "page")
//
func (remote *RemoteDebugger) TabList(filter string) ([]*Tab, error) {
	resp, err := remote.http.Get("/json/list", nil, nil)
	if err != nil {
		return nil, err
	}

	var tabs []*Tab

	if err = decode(resp, &tabs); err != nil {
		return nil, err
	}

	if filter == "" {
		return tabs, nil
	}

	var filtered []*Tab

	for _, t := range tabs {
		if t.Type == filter {
			filtered = append(filtered, t)
		}
	}

	return filtered, nil
}

//
// Activate specified tab
//
func (remote *RemoteDebugger) ActivateTab(tab *Tab) error {
	resp, err := remote.http.Get("/json/activate/"+tab.Id, nil, nil)
	resp.Close()
	return err
}

//
// Close specified tab
//
func (remote *RemoteDebugger) CloseTab(tab *Tab) error {
	resp, err := remote.http.Get("/json/close/"+tab.Id, nil, nil)
	resp.Close()
	return err
}

//
// Create a new tab
//
func (remote *RemoteDebugger) NewTab(url string) (*Tab, error) {
	params := Params{}
	if url != "" {
		params["url"] = url
	}
	resp, err := remote.http.Get("/json/new", params, nil)
	if err != nil {
		return nil, err
	}

	var tab Tab
	if err = decode(resp, &tab); err != nil {
		return nil, err
	}

	return &tab, nil
}

func (remote *RemoteDebugger) getDomains() (map[string]interface{}, error) {
	res, err := remote.sendRequest("Schema.getDomains", nil)
	return res, err
}

func (remote *RemoteDebugger) Navigate(url string) error {
	_, err := remote.sendRequest("Page.navigate", Params{
		"url": url,
	})

	return err
}

func (remote *RemoteDebugger) GetResponseBody(req string) (bool, string, error) {
	res, err := remote.sendRequest("Network.getResponseBody", Params{
		"requestId": req,
	})

	if err != nil {
		return false, "", err
	} else {
		return res["base64Encoded"].(bool), res["body"].(string), nil
	}
}

func (remote *RemoteDebugger) CallbackEvent(method string, cb EventCallback) {
	remote.Lock()
	remote.callbacks[method] = cb
	remote.Unlock()
}

func (remote *RemoteDebugger) domainEvents(domain string, enable bool) error {
	method := domain

	if enable {
		method += ".enable"
	} else {
		method += ".disable"
	}

	_, err := remote.sendRequest(method, nil)
	return err
}

func (remote *RemoteDebugger) DOMEvents(enable bool) error {
	return remote.domainEvents("DOM", enable)
}

func (remote *RemoteDebugger) PageEvents(enable bool) error {
	return remote.domainEvents("Page", enable)
}

func (remote *RemoteDebugger) NetworkEvents(enable bool) error {
	return remote.domainEvents("Network", enable)
}

func (remote *RemoteDebugger) RuntimeEvents(enable bool) error {
	return remote.domainEvents("Runtime", enable)
}

func runCommand(commandString string) error {
	parts := args.GetArgs(commandString)
	cmd := exec.Command(parts[0], parts[1:]...)
	err := cmd.Start()
	if err == nil {
		time.Sleep(time.Second) // give the app some time to start
	} else {
		log.Println("command start", err)
	}

	return err
}

func main() {
	cmd := flag.String("cmd", "open /Applications/Google\\ Chrome.app --args --remote-debugging-port=9222 --disable-extensions --headless about:blank", "command to execute to start the browser")
	port := flag.String("port", "localhost:9222", "Chrome remote debugger port")
	verbose := flag.Bool("verbose", false, "verbose logging")
	version := flag.Bool("version", false, "display remote devtools version")
	listtabs := flag.Bool("tabs", false, "show list of open tabs")
	filter := flag.String("filter", "page", "filter tab list")
	domains := flag.Bool("domains", false, "show list of available domains")
	flag.Parse()

	if *cmd != "" {
		runCommand(*cmd)
	}

	remote, err := Connect(*port, *verbose)
	if err != nil {
		log.Fatal("connect", err)
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
		d, err := remote.getDomains()
		if err != nil {
			log.Fatal("cannot get domains: ", err)
		}

		pretty.PrettyPrint(d)
	}

	remote.PageEvents(true)
	remote.DOMEvents(true)
	remote.RuntimeEvents(true)
	remote.NetworkEvents(true)

	remote.CallbackEvent("Network.responseReceived", func(params Params) {
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

	remote.CallbackEvent("Network.requestWillBeSent", func(params Params) {
		log.Println("requestWillBeSent",
			params["type"],
			params["documentURL"],
			params["request"].(map[string]interface{})["url"])
	})

	if flag.NArg() > 0 {
		p := flag.Arg(0)

		log.Println("loading page", p)

		tabs, err := remote.TabList("page")
		if err != nil {
			log.Fatal("cannot get tabs", err)
		}

		if len(tabs) == 0 {
			_, err = remote.NewTab(p)
		} else {
			err := remote.ActivateTab(tabs[0])
			if err == nil {
				fmt.Println(remote.Navigate(p))
			}
		}

		if err != nil {
			log.Println("error loading page", err)
		}
	}

	time.Sleep(60 * time.Second)
	log.Println("Closing")
}
