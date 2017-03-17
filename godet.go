// Package godet implements a client to interact with an instance of Chrome via the Remote Debugging Protocol.
//
// See https://developer.chrome.com/devtools/docs/debugger-protocol
package godet

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/gobs/httpclient"
	"github.com/gorilla/websocket"
)

const (
	EventClosed = "RemoteDebugger.closed"
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
// returned by Evaluate if something is wrong with the expression
//
type EvaluateError map[string]interface{}

func (err EvaluateError) Error() string {
	return err["description"].(string)
}

//
// RemoteDebugger implements an interface with Chrome DevTools
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

	getWsUrl := func() string {
		for _, tab := range tabs {
			if tab.WsUrl != "" {
				return tab.WsUrl
			}
		}

		return "ws://" + port + "/devtools/page/00000000-0000-0000-0000-000000000000"
	}

	wsUrl := getWsUrl()

	// check websocket connection
	if remote.ws, _, err = websocket.DefaultDialer.Dial(wsUrl, nil); err != nil {
		if verbose {
			log.Println("dial", wsUrl, "error", err)
		}
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

					remote.Lock()
					_, ok := remote.callbacks[message.Method]
					remote.Unlock()

					if !ok {
						continue // don't queue unrequested events
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

	remote.events <- wsMessage{Method: EventClosed, Params: []byte("{}")}
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

//
// Get the list of available DevTools domains
//
func (remote *RemoteDebugger) GetDomains() (map[string]interface{}, error) {
	res, err := remote.sendRequest("Schema.getDomains", nil)
	return res, err
}

//
// Navigate to specified URL
//
func (remote *RemoteDebugger) Navigate(url string) error {
	_, err := remote.sendRequest("Page.navigate", Params{
		"url": url,
	})

	return err
}

//
// Reload current page
//
func (remote *RemoteDebugger) Reload() error {
	_, err := remote.sendRequest("Page.reload", Params{
		"ignoreCache": true,
	})

	return err
}

//
// Given a requestId (from the Network.responseReceived payload) returns the response body
//
func (remote *RemoteDebugger) GetResponseBody(req string) ([]byte, error) {
	res, err := remote.sendRequest("Network.getResponseBody", Params{
		"requestId": req,
	})

	if err != nil {
		return nil, err
	} else if res["base64Encoded"].(bool) {
		return base64.StdEncoding.DecodeString(res["body"].(string))
	} else {
		return []byte(res["body"].(string)), nil
	}
}

//
// Get the "Document" object as a devtool node
//
func (remote *RemoteDebugger) GetDocument() (map[string]interface{}, error) {
	return remote.sendRequest("DOM.getDocument", nil)
}

//
// Get the nodeId for specified selector
//
func (remote *RemoteDebugger) QuerySelector(nodeId int, selector string) (map[string]interface{}, error) {
	return remote.sendRequest("DOM.querySelector", Params{
		"nodeId":   nodeId,
		"selector": selector,
	})
}

//
// Get a list of nodeId for the specified selectors
//
func (remote *RemoteDebugger) QuerySelectorAll(nodeId int, selector string) (map[string]interface{}, error) {
	return remote.sendRequest("DOM.querySelectorAll", Params{
		"nodeId":   nodeId,
		"selector": selector,
	})
}

//
// ResolveNode returns some information about the node
//
func (remote *RemoteDebugger) ResolveNode(nodeId int) (map[string]interface{}, error) {
	return remote.sendRequest("DOM.resolveNode", Params{
		"nodeId": nodeId,
	})
}

//
// Request a node. The response is generated as a DOM.setChildNodes event.
//
func (remote *RemoteDebugger) RequestNode(nodeId int) error {
	_, err := remote.sendRequest("DOM.requestChildNodes", Params{
		"nodeId": nodeId,
	})

	return err
}

//
// Set focus on specified node
//
func (remote *RemoteDebugger) Focus(nodeId int) error {
	_, err := remote.sendRequest("DOM.focus", Params{
		"nodeId": nodeId,
	})

	return err
}

//
// Attach input files to specified node (an input[type=file] element ?)
//
func (remote *RemoteDebugger) SetInputFiles(nodeId int, files []string) error {
	_, err := remote.sendRequest("DOM.setInputFiles", Params{
		"nodeId": nodeId,
		"files":  files,
	})

	return err
}

//
// Set the attribute with specified name and value
//
func (remote *RemoteDebugger) SetAttributeValue(nodeId int, name, value string) error {
	_, err := remote.sendRequest("DOM.setAttributeValue", Params{
		"nodeId": nodeId,
		"name":   name,
		"value":  value,
	})

	return err
}

//
// Send a character as keyboard input
//
func (remote *RemoteDebugger) SendRune(c rune) error {
	if _, err := remote.sendRequest("Input.dispatchKeyEvent", Params{
		"type":                  "rawKeyDown",
		"windowsVirtualKeyCode": int(c),
		"nativeVirtualKeyCode":  int(c),
		"unmodifiedText":        string(c),
		"text":                  string(c),
	}); err != nil {
		return err
	}
	if _, err := remote.sendRequest("Input.dispatchKeyEvent", Params{
		"type":                  "char",
		"windowsVirtualKeyCode": int(c),
		"nativeVirtualKeyCode":  int(c),
		"unmodifiedText":        string(c),
		"text":                  string(c),
	}); err != nil {
		return err
	}
	_, err := remote.sendRequest("Input.dispatchKeyEvent", Params{
		"type":                  "keyUp",
		"windowsVirtualKeyCode": int(c),
		"nativeVirtualKeyCode":  int(c),
		"unmodifiedText":        string(c),
		"text":                  string(c),
	})
	return err
}

//
// Evaluate javascript function in the context of the current page
//
func (remote *RemoteDebugger) Evaluate(expr string) (interface{}, error) {
	res, err := remote.sendRequest("Runtime.evaluate", Params{
		"expression":    expr,
		"returnByValue": true,
	})

	if err != nil {
		return nil, err
	}

	if res == nil {
		return nil, nil
	}

	res = res["result"].(map[string]interface{})
	if subtype, ok := res["subtype"]; ok && subtype.(string) == "error" {
		// this is actually an error
		return nil, EvaluateError(res)
	}

	return res["value"], nil
}

//
// To evaluate a list of expression, EvaluateWrap wraps them in `(function(){ ... })()`.
//
// Use a return statement to return a value.
//
func (remote *RemoteDebugger) EvaluateWrap(expr string) (interface{}, error) {
	expr = fmt.Sprintf("(function(){%v})()", expr)
	return remote.Evaluate(expr)
}

//
// Set a callback for the specified event
//
func (remote *RemoteDebugger) CallbackEvent(method string, cb EventCallback) {
	remote.Lock()
	remote.callbacks[method] = cb
	remote.Unlock()
}

//
// Enable listening to events in the specified domain
//
func (remote *RemoteDebugger) DomainEvents(domain string, enable bool) error {
	method := domain

	if enable {
		method += ".enable"
	} else {
		method += ".disable"
	}

	_, err := remote.sendRequest(method, nil)
	return err
}

//
// Enable listenting to DOM events
//
func (remote *RemoteDebugger) DOMEvents(enable bool) error {
	return remote.DomainEvents("DOM", enable)
}

//
// Enable listenting to Page events
//
func (remote *RemoteDebugger) PageEvents(enable bool) error {
	return remote.DomainEvents("Page", enable)
}

//
// Enable listenting to Network events
//
func (remote *RemoteDebugger) NetworkEvents(enable bool) error {
	return remote.DomainEvents("Network", enable)
}

//
// Enable listenting to Runtime events
//
func (remote *RemoteDebugger) RuntimeEvents(enable bool) error {
	return remote.DomainEvents("Runtime", enable)
}

//
// Enable listenting to Log events
//
func (remote *RemoteDebugger) LogEvents(enable bool) error {
	return remote.DomainEvents("Log", enable)
}
