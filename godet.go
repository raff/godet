package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os/exec"
	"sync"
	"time"

	"github.com/gobs/args"
	"github.com/gobs/httpclient"
	"golang.org/x/net/websocket"
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

func (v *Version) String() string {
	return fmt.Sprintf("Browser: %v\n"+
		"Protocol Version: %v\n"+
		"User Agent: %v\n"+
		"V8 Version: %v\n"+
		"WebKit Version: %v\n",
		v.Browser,
		v.ProtocolVersion,
		v.UserAgent,
		v.V8Version,
		v.WebKitVersion)
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

func (t Tab) String() string {
	return fmt.Sprintf("Id: %v\n"+
		"Type: %v\n"+
		"Description: %v\n"+
		"Title: %v\n"+
		"Url: %v\n"+
		"WebSocket Url: %v\n"+
		"Devtools Url: %v\n",
		t.Id,
		t.Type,
		t.Description,
		t.Title,
		t.Url,
		t.WsUrl,
		t.DevUrl)
}

//
// RemoteDebugger
//
type RemoteDebugger struct {
	http    *httpclient.HttpClient
	ws      *websocket.Conn
	reqid   int
	closed  bool
	verbose bool

	sync.Mutex

	responses map[int]chan json.RawMessage
	callbacks map[string]EventCallback
}

type Params map[string]interface{}
type EventCallback func(params Params)

//
// Connect to the remote debugger and return `RemoteDebugger` object
//
func Connect(port string, verbose bool) (*RemoteDebugger, error) {
	remote := &RemoteDebugger{
		http:      httpclient.NewHttpClient("http://" + port),
		responses: map[int]chan json.RawMessage{},
		callbacks: map[string]EventCallback{},
		verbose:   verbose,
	}

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
	if remote.ws, err = websocket.Dial(wsUrl, "", "http://localhost"); err != nil {
		return nil, err
	}

	go remote.readMessages()
	return remote, nil
}

func (remote *RemoteDebugger) Close() error {
	remote.closed = true
	return remote.ws.Close()
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

	command := map[string]interface{}{
		"id":     reqid,
		"method": method,
		"params": params,
	}

	bytes, err := json.Marshal(command)
	if err != nil {
		return nil, err
	}

	if remote.verbose {
		log.Println("send", string(bytes))
	}

	_, err = remote.ws.Write(bytes)
	if err != nil {
		return nil, err
	}

	reply := <-remote.responses[reqid]
	remote.Lock()
	remote.responses[reqid] = nil
	remote.Unlock()

	if remote.verbose {
		log.Println("reply", reqid, string(reply))
	}

	if reply != nil {
		return unmarshal(reply)
	}

	return nil, nil
}

func (remote *RemoteDebugger) readMessages() {
	buf := make([]byte, 4096)
	var bytes []byte

	for !remote.closed {
		if n, err := remote.ws.Read(buf); err != nil {
			log.Println("read error", err)
			if err == io.EOF {
				break
			}
		} else {
			if n > 0 {
				bytes = append(bytes, buf[:n]...)

				// hack to check end of message
				if bytes[0] == '{' && bytes[len(bytes)-1] != '}' {
					continue
				}
			}

			var message wsMessage

			//
			// unmarshall message
			//
			if err := json.Unmarshal(bytes, &message); err != nil {
				log.Println("error unmarshaling", string(bytes), len(bytes), err)
			} else if message.Method != "" {
				if remote.verbose {
					log.Println("EVENT", message.Method, string(message.Params))
				}

				//
				// should be an event notification
				//
				remote.Lock()
				cb := remote.callbacks[message.Method]
				remote.Unlock()

				if cb != nil {
					var params Params
					if err := json.Unmarshal(message.Params, &params); err != nil {
						log.Println("error unmarshaling", string(message.Params), len(message.Params), err)
					} else {
						cb(params)
					}
				}
			} else {
				//
				// should be a method reply
				//
				remote.Lock()
				ch := remote.responses[message.Id]
				remote.Unlock()

				if ch != nil {
					ch <- message.Result
				}
			}

			bytes = nil
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

func (remote *RemoteDebugger) events(domain string, enable bool) error {
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
	return remote.events("DOM", enable)
}

func (remote *RemoteDebugger) PageEvents(enable bool) error {
	return remote.events("Page", enable)
}

func (remote *RemoteDebugger) NetworkEvents(enable bool) error {
	return remote.events("Network", enable)
}

func (remote *RemoteDebugger) RuntimeEvents(enable bool) error {
	return remote.events("Runtime", enable)
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
	filter := flag.String("filter", "page", "filter tab list")
	page := flag.String("page", "http://httpbin.org", "page to load")
	verbose := flag.Bool("verbose", false, "verbose logging")
	flag.Parse()

	if *cmd != "" {
		runCommand(*cmd)
	}

	remote, err := Connect(*port, *verbose)
	if err != nil {
		log.Fatal("connect", err)
	}

	defer remote.Close()

	fmt.Println()
	fmt.Println("Version:")
	fmt.Println(remote.Version())

	fmt.Println()
	tabs, err := remote.TabList(*filter)
	if err != nil {
		log.Fatal("cannot get list of tabs: ", err)
	}

	fmt.Println(tabs)

	fmt.Println()
	fmt.Println(remote.getDomains())

	remote.PageEvents(true)
	remote.DOMEvents(true)
	remote.RuntimeEvents(true)
	remote.NetworkEvents(true)

	remote.CallbackEvent("Network.responseReceived", func(params Params) {
		log.Println("responseReceived",
			params["type"],
			params["response"].(map[string]interface{})["url"])

		if params["type"].(string) == "Image" {
			req := params["requestId"].(string)
			go func() {
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

	l := len(tabs)
	if l > 0 {
		remote.ActivateTab(tabs[l-1])

		fmt.Println()
		fmt.Println(remote.Navigate(*page))
	}

	time.Sleep(60 * time.Second)
}
