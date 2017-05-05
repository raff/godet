// Package godet implements a client to interact with an instance of Chrome via the Remote Debugging Protocol.
//
// See https://developer.chrome.com/devtools/docs/debugger-protocol
package godet

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/gobs/httpclient"
	"github.com/gorilla/websocket"
)

const (
	// EventClosed represents the "RemoteDebugger.closed" event.
	// It is emitted when RemoteDebugger.Close() is called.
	EventClosed = "RemoteDebugger.closed"

	// NavigationProceed allows the navigation
	NavigationProceed = NavigationResponse("Proceed")
	// NavigationCancel cancels the navigation
	NavigationCancel = NavigationResponse("Cancel")
	// NavigationCancelAndIgnore cancels the navigation and makes the requester of the navigation acts like the request was never made.
	NavigationCancelAndIgnore = NavigationResponse("CancelAndIgnore")
)

var (
	// ErrorNoActiveTab is returned if there are no active tabs (of type "page")
	ErrorNoActiveTab = errors.New("no active tab")
	// ErrorNoWsURL is returned if the active tab has no websocket URL
	ErrorNoWsURL = errors.New("no websocket URL")
)

// NavigationResponse define the type for ProcessNavigation `response`
type NavigationResponse string

func decode(resp *httpclient.HttpResponse, v interface{}) error {
	err := json.NewDecoder(resp.Body).Decode(v)
	resp.Close()

	return err
}

func unmarshal(payload []byte) (map[string]interface{}, error) {
	var response map[string]interface{}
	err := json.Unmarshal(payload, &response)
	if err != nil {
		log.Println("unmarshal", string(payload), len(payload), err)
	}
	return response, err
}

// Version holds the DevTools version information.
type Version struct {
	Browser         string `json:"Browser"`
	ProtocolVersion string `json:"Protocol-Version"`
	UserAgent       string `json:"User-Agent"`
	V8Version       string `json:"V8-Version"`
	WebKitVersion   string `json:"WebKit-Version"`
}

// Domain holds a domain name and version.
type Domain struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Tab represents an opened tab/page.
type Tab struct {
	ID          string `json:"id"`
	Type        string `json:"type"`
	Description string `json:"description"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	WsURL       string `json:"webSocketDebuggerUrl"`
	DevURL      string `json:"devtoolsFrontendUrl"`
}

// Profile represents a profile data structure.
type Profile struct {
	Nodes      []ProfileNode `json:"nodes"`
	StartTime  int64         `json:"startTime"`
	EndTime    int64         `json:"endTime"`
	Samples    []int64       `json:"samples"`
	TimeDeltas []int64       `json:"timeDeltas"`
}

// ProfileNode represents a profile node data structure.
// The experimental fields are kept as json.RawMessage, so you may decode them with your own code, see: https://chromedevtools.github.io/debugger-protocol-viewer/tot/Profiler/
type ProfileNode struct {
	ID            int64           `json:"id"`
	CallFrame     json.RawMessage `json:"callFrame"`
	HitCount      int64           `json:"hitCount"`
	Children      []int64         `json:"children"`
	DeoptReason   string          `json:"deoptReason"`
	PositionTicks json.RawMessage `json:"positionTicks"`
}

// EvaluateError is returned by Evaluate in case of expression errors.
type EvaluateError struct {
	ErrorDetails     map[string]interface{}
	ExceptionDetails map[string]interface{}
}

func (err EvaluateError) Error() string {
	desc := err.ErrorDetails["description"].(string)
	if excp := err.ExceptionDetails; excp != nil {
		if excp["exception"] != nil {
			desc += fmt.Sprintf(" at line %v col %v",
				excp["lineNumber"].(float64), excp["columnNumber"].(float64))
		}
	}

	return desc
}

// RemoteDebugger implements an interface for Chrome DevTools.
type RemoteDebugger struct {
	http    *httpclient.HttpClient
	ws      *websocket.Conn
	current string
	reqID   int
	verbose bool

	sync.Mutex
	closed chan bool

	requests  chan Params
	responses map[int]chan json.RawMessage
	callbacks map[string]EventCallback
	events    chan wsMessage
}

// Params is a type alias for the event params structure.
type Params map[string]interface{}

// EventCallback represents a callback event, associated with a method.
type EventCallback func(params Params)

// Connect to the remote debugger and return `RemoteDebugger` object.
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

	// remote.http.Verbose = verbose
	if verbose {
		httpclient.StartLogging(false, true)
	}

	if err := remote.connectWs(nil); err != nil {
		return nil, err
	}

	go remote.sendMessages()
	go remote.processEvents()
	return remote, nil
}

func (remote *RemoteDebugger) connectWs(tab *Tab) error {
	if tab == nil || len(tab.WsURL) == 0 {
		tabs, err := remote.TabList("page")
		if err != nil {
			return err
		}

		if len(tabs) == 0 {
			return ErrorNoActiveTab
		}

		if tab == nil {
			tab = tabs[0]
		} else {
			for _, t := range tabs {
				if tab.ID == t.ID {
					tab.WsURL = t.WsURL
					break
				}
			}
		}
	}

	if remote.ws != nil {
		if tab.ID == remote.current {
			// nothing to do
			return nil
		}

		if remote.verbose {
			log.Println("disconnecting from current tab, id", remote.current)
		}

		remote.Lock()
		ws := remote.ws
		remote.ws, remote.current = nil, ""
		remote.Unlock()

		_ = ws.Close()
	}

	if len(tab.WsURL) == 0 {
		return ErrorNoWsURL
	}

	// check websocket connection
	if remote.verbose {
		log.Println("connecting to tab", tab.WsURL)
	}

	ws, _, err := websocket.DefaultDialer.Dial(tab.WsURL, nil)
	if err != nil {
		if remote.verbose {
			log.Println("dial error:", err)
		}
		return err
	}

	remote.Lock()
	remote.ws = ws
	remote.current = tab.ID
	remote.Unlock()

	go remote.readMessages()
	return nil
}

func (remote *RemoteDebugger) socket() (ws *websocket.Conn) {
	remote.Lock()
	ws = remote.ws
	remote.Unlock()
	return
}

// Close the RemoteDebugger connection.
func (remote *RemoteDebugger) Close() error {
	close(remote.closed)
	err := remote.ws.Close()
	if remote.verbose {
		httpclient.StopLogging()
	}
	return err
}

type wsMessage struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`

	Method string          `json:"Method"`
	Params json.RawMessage `json:"Params"`
}

// SendRequest sends a request and returns the reply as a a map.
func (remote *RemoteDebugger) SendRequest(method string, params Params) (map[string]interface{}, error) {
	rawReply, err := remote.sendRawReplyRequest(method, params)
	if err != nil || rawReply == nil {
		return nil, err
	}
	return unmarshal(rawReply)
}

// sendRequest sends a request and returns the reply bytes.
func (remote *RemoteDebugger) sendRawReplyRequest(method string, params Params) ([]byte, error) {
	remote.Lock()
	reqID := remote.reqID
	remote.responses[reqID] = make(chan json.RawMessage, 1)
	remote.reqID++
	remote.Unlock()

	command := Params{
		"id":     reqID,
		"method": method,
		"params": params,
	}

	remote.requests <- command
	reply := <-remote.responses[reqID]

	remote.Lock()
	delete(remote.responses, reqID)
	remote.Unlock()

	return reply, nil
}

func (remote *RemoteDebugger) sendMessages() {
	for message := range remote.requests {
		bytes, err := json.Marshal(message)
		if err != nil {
			log.Println("marshal", message, err)
			continue
		}

		if remote.verbose {
			log.Println("SEND", string(bytes))
		}

		ws := remote.socket()
		err = ws.WriteMessage(websocket.TextMessage, bytes)
		if err != nil {
			log.Println("write message:", err)
		}
	}
}

func permanentError(err error) bool {
	if websocket.IsUnexpectedCloseError(err) {
		return true
	}

	if neterr, ok := err.(net.Error); ok && !neterr.Temporary() {
		return true
	}

	return false
}

func (remote *RemoteDebugger) readMessages() {
	remoteClosed := false

loop:
	for {
		select {
		case <-remote.closed:
			remoteClosed = true
			break loop

		default:
			_, bytes, err := remote.socket().ReadMessage()
			if err != nil {
				log.Println("read message:", err)
				if permanentError(err) {
					break loop
				}
			} else {
				var message wsMessage

				//
				// unmarshall message
				//
				if err := json.Unmarshal(bytes, &message); err != nil {
					log.Println("unmarshal", string(bytes), len(bytes), err)
				} else if message.Method != "" {
					if remote.verbose {
						log.Println("EVENT", message.Method, string(message.Params), len(remote.events))
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
						remoteClosed = true
						break loop
					}
				} else {
					//
					// should be a method reply
					//
					if remote.verbose {
						log.Println("REPLY", message.ID, string(message.Result))
					}

					remote.Lock()
					ch := remote.responses[message.ID]
					remote.Unlock()

					if ch != nil {
						ch <- message.Result
					}
				}
			}
		}
	}

	// log.Println("exit readMessages", remoteClosed)

	if remoteClosed {
		remote.events <- wsMessage{Method: EventClosed, Params: []byte("{}")}
		close(remote.events)
	}
}

func (remote *RemoteDebugger) processEvents() {
	for ev := range remote.events {
		remote.Lock()
		cb := remote.callbacks[ev.Method]
		remote.Unlock()

		if cb != nil {
			var params Params
			if err := json.Unmarshal(ev.Params, &params); err != nil {
				log.Println("unmarshal", string(ev.Params), len(ev.Params), err)
			} else {
				cb(params)
			}
		}
	}
}

// Version returns version information (protocol, browser, etc.).
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

// TabList returns a list of opened tabs/pages.
// If filter is not empty, only tabs of the specified type are returned (i.e. "page").
//
// Note that tabs are ordered by activitiy time (most recently used first) so the
// current tab is the first one of type "page".
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

// ActivateTab activates the specified tab.
func (remote *RemoteDebugger) ActivateTab(tab *Tab) error {
	resp, err := remote.http.Get("/json/activate/"+tab.ID, nil, nil)
	resp.Close()

	if err == nil {
		err = remote.connectWs(tab)
	}

	return err
}

// CloseTab closes the specified tab.
func (remote *RemoteDebugger) CloseTab(tab *Tab) error {
	resp, err := remote.http.Get("/json/close/"+tab.ID, nil, nil)
	resp.Close()
	return err
}

// NewTab creates a new tab.
func (remote *RemoteDebugger) NewTab(url string) (*Tab, error) {
	path := "/json/new"
	if url != "" {
		path += "?" + url
	}

	resp, err := remote.http.Do(remote.http.Request("GET", path, nil, nil))
	if err != nil {
		return nil, err
	}

	var tab Tab
	if err = decode(resp, &tab); err != nil {
		return nil, err
	}

	if err = remote.connectWs(&tab); err != nil {
		return nil, err
	}

	return &tab, nil
}

// GetDomains lists the available DevTools domains.
func (remote *RemoteDebugger) GetDomains() ([]Domain, error) {
	res, err := remote.sendRawReplyRequest("Schema.getDomains", nil)
	if err != nil {
		return nil, err
	}

	var domains struct {
		Domains []Domain
	}

	err = json.Unmarshal(res, &domains)
	if err != nil {
		return nil, err
	}

	return domains.Domains, nil
}

// Navigate navigates to the specified URL.
func (remote *RemoteDebugger) Navigate(url string) (string, error) {
	res, err := remote.SendRequest("Page.navigate", Params{
		"url": url,
	})
	if err != nil {
		return "", err
	}

	frameID, ok := res["frameId"]
	if !ok {
		return "", nil
	}
	return frameID.(string), nil
}

// Reload reloads the current page.
func (remote *RemoteDebugger) Reload() error {
	_, err := remote.SendRequest("Page.reload", Params{
		"ignoreCache": true,
	})

	return err
}

// SetControlNavigation toggles navigation throttling which allows programatic control over navigation and redirect response.
func (remote *RemoteDebugger) SetControlNavigation(enabled bool) error {
	_, err := remote.SendRequest("Page.setControlNavigation", Params{
		"enabled": enabled,
	})

	return err
}

// ProcessNavigation should be sent in response to a navigationRequested or a redirectRequested event, telling the browser how to handle the navigation.
func (remote *RemoteDebugger) ProcessNavigation(navigationID int, navigation NavigationResponse) error {
	_, err := remote.SendRequest("Page.processNavigation", Params{
		"response":     navigation,
		"navigationId": navigationID,
	})

	return err
}

// CaptureScreenshot takes a screenshot, uses "png" as default format.
func (remote *RemoteDebugger) CaptureScreenshot(format string, quality int, fromSurface bool) ([]byte, error) {
	if format == "" {
		format = "png"
	}
	res, err := remote.SendRequest("Page.captureScreenshot", Params{
		"format":      format,
		"quality":     quality,
		"fromSurface": fromSurface,
	})
	if err != nil {
		return nil, err
	}

	return base64.StdEncoding.DecodeString(res["data"].(string))
}

// SaveScreenshot takes a screenshot and saves it to a file.
func (remote *RemoteDebugger) SaveScreenshot(filename string, perm os.FileMode, quality int, fromSurface bool) error {
	var format string
	ext := filepath.Ext(filename)
	switch ext {
	case ".jpg":
		format = "jpeg"
	case ".png":
		format = "png"
	default:
		return errors.New("Image format not supported")
	}
	rawScreenshot, err := remote.CaptureScreenshot(format, quality, fromSurface)
	if err != nil {
		return err
	}
	return ioutil.WriteFile(filename, rawScreenshot, perm)
}

// HandleJavaScriptDialog accepts or dismisses a Javascript initiated dialog.
func (remote *RemoteDebugger) HandleJavaScriptDialog(accept bool, promptText string) error {
	_, err := remote.SendRequest("Page.handleJavaScriptDialog", Params{
		"accept":     accept,
		"promptText": promptText,
	})

	return err
}

// GetResponseBody returns the response body of a given requestId (from the Network.responseReceived payload).
func (remote *RemoteDebugger) GetResponseBody(req string) ([]byte, error) {
	res, err := remote.SendRequest("Network.getResponseBody", Params{
		"requestId": req,
	})

	if err != nil {
		return nil, err
	} else if b, ok := res["base64Encoded"]; ok && b.(bool) {
		return base64.StdEncoding.DecodeString(res["body"].(string))
	} else {
		return []byte(res["body"].(string)), nil
	}
}

// GetDocument gets the "Document" object as a DevTool node.
func (remote *RemoteDebugger) GetDocument() (map[string]interface{}, error) {
	return remote.SendRequest("DOM.getDocument", nil)
}

// QuerySelector gets the nodeId for a specified selector.
func (remote *RemoteDebugger) QuerySelector(nodeID int, selector string) (map[string]interface{}, error) {
	return remote.SendRequest("DOM.querySelector", Params{
		"nodeId":   nodeID,
		"selector": selector,
	})
}

// QuerySelectorAll gets a list of nodeId for the specified selectors.
func (remote *RemoteDebugger) QuerySelectorAll(nodeID int, selector string) (map[string]interface{}, error) {
	return remote.SendRequest("DOM.querySelectorAll", Params{
		"nodeId":   nodeID,
		"selector": selector,
	})
}

// ResolveNode returns some information about the node.
func (remote *RemoteDebugger) ResolveNode(nodeID int) (map[string]interface{}, error) {
	return remote.SendRequest("DOM.resolveNode", Params{
		"nodeId": nodeID,
	})
}

// RequestNode requests a node, the response is generated as a DOM.setChildNodes event.
func (remote *RemoteDebugger) RequestNode(nodeID int) error {
	_, err := remote.SendRequest("DOM.requestChildNodes", Params{
		"nodeId": nodeID,
	})

	return err
}

// Focus sets focus on a specified node.
func (remote *RemoteDebugger) Focus(nodeID int) error {
	_, err := remote.SendRequest("DOM.focus", Params{
		"nodeId": nodeID,
	})

	return err
}

// SetInputFiles attaches input files to a specified node (an input[type=file] element?).
func (remote *RemoteDebugger) SetInputFiles(nodeID int, files []string) error {
	_, err := remote.SendRequest("DOM.setInputFiles", Params{
		"nodeId": nodeID,
		"files":  files,
	})

	return err
}

// SetAttributeValue sets the value for a specified attribute.
func (remote *RemoteDebugger) SetAttributeValue(nodeID int, name, value string) error {
	_, err := remote.SendRequest("DOM.setAttributeValue", Params{
		"nodeId": nodeID,
		"name":   name,
		"value":  value,
	})

	return err
}

// GetOuterHTML returns node's HTML markup.
func (remote *RemoteDebugger) GetOuterHTML(nodeID int) (string, error) {
	res, err := remote.SendRequest("DOM.getOuterHTML", Params{
		"nodeId": nodeID,
	})

	if err != nil {
		return "", err
	}

	return res["outerHTML"].(string), nil
}

// SetOuterHTML sets node HTML markup.
func (remote *RemoteDebugger) SetOuterHTML(nodeID int, outerHTML string) error {
	_, err := remote.SendRequest("DOM.setOuterHTML", Params{
		"nodeId":    nodeID,
		"outerHTML": outerHTML,
	})

	return err
}

// SendRune sends a character as keyboard input.
func (remote *RemoteDebugger) SendRune(c rune) error {
	if _, err := remote.SendRequest("Input.dispatchKeyEvent", Params{
		"type":                  "rawKeyDown",
		"windowsVirtualKeyCode": int(c),
		"nativeVirtualKeyCode":  int(c),
		"unmodifiedText":        string(c),
		"text":                  string(c),
	}); err != nil {
		return err
	}
	if _, err := remote.SendRequest("Input.dispatchKeyEvent", Params{
		"type":                  "char",
		"windowsVirtualKeyCode": int(c),
		"nativeVirtualKeyCode":  int(c),
		"unmodifiedText":        string(c),
		"text":                  string(c),
	}); err != nil {
		return err
	}
	_, err := remote.SendRequest("Input.dispatchKeyEvent", Params{
		"type":                  "keyUp",
		"windowsVirtualKeyCode": int(c),
		"nativeVirtualKeyCode":  int(c),
		"unmodifiedText":        string(c),
		"text":                  string(c),
	})
	return err
}

// Evaluate evalutes a Javascript function in the context of the current page.
func (remote *RemoteDebugger) Evaluate(expr string) (interface{}, error) {
	res, err := remote.SendRequest("Runtime.evaluate", Params{
		"expression":    expr,
		"returnByValue": true,
	})

	if err != nil {
		return nil, err
	}

	if res == nil {
		return nil, nil
	}

	result := res["result"].(map[string]interface{})
	if subtype, ok := result["subtype"]; ok && subtype.(string) == "error" {
		// this is actually an error
		exception := res["exceptionDetails"].(map[string]interface{})
		return nil, EvaluateError{ErrorDetails: result, ExceptionDetails: exception}
	}

	return result["value"], nil
}

// EvaluateWrap evaluates a list of expressions, EvaluateWrap wraps them in `(function(){ ... })()`.
// Use a return statement to return a value.
func (remote *RemoteDebugger) EvaluateWrap(expr string) (interface{}, error) {
	expr = fmt.Sprintf("(function(){%v})()", expr)
	return remote.Evaluate(expr)
}

// SetBlockedURLs blocks URLs from loading (wildcards '*' are allowed)
func (remote *RemoteDebugger) SetBlockedURLs(urls ...string) error {
	_, err := remote.SendRequest("Network.setBlockedURLs", Params{
		"urls": urls,
	})
	return err
}

// SetUserAgent overrides the default user agent.
func (remote *RemoteDebugger) SetUserAgent(userAgent string) error {
	_, err := remote.SendRequest("Network.setUserAgentOverride", Params{
		"userAgent": userAgent,
	})
	return err
}

// CallbackEvent sets a callback for the specified event.
func (remote *RemoteDebugger) CallbackEvent(method string, cb EventCallback) {
	remote.Lock()
	remote.callbacks[method] = cb
	remote.Unlock()
}

// StartProfiler starts the profiler.
func (remote *RemoteDebugger) StartProfiler() error {
	_, err := remote.SendRequest("Profiler.start", nil)
	return err
}

// StopProfiler stops the profiler.
// Returns a Profile data structure, as specified here: https://chromedevtools.github.io/debugger-protocol-viewer/tot/Profiler/#type-Profile
func (remote *RemoteDebugger) StopProfiler() (p Profile, err error) {
	res, err := remote.sendRawReplyRequest("Profiler.stop", nil)
	if err != nil {
		return p, err
	}
	var response map[string]json.RawMessage
	err = json.Unmarshal(res, &response)
	if err != nil {
		return p, err
	}
	err = json.Unmarshal(response["profile"], &p)
	return p, err
}

// SetProfilerSamplingInterval sets the profiler sampling interval in microseconds, must be called before StartProfiler.
func (remote *RemoteDebugger) SetProfilerSamplingInterval(n int64) error {
	_, err := remote.SendRequest("Profiler.setSamplingInterval", Params{
		"interval": n,
	})
	return err
}

// DomainEvents enables event listening in the specified domain.
func (remote *RemoteDebugger) DomainEvents(domain string, enable bool) error {
	method := domain

	if enable {
		method += ".enable"
	} else {
		method += ".disable"
	}

	_, err := remote.SendRequest(method, nil)
	return err
}

// AllEvents enables event listening for all domains.
func (remote *RemoteDebugger) AllEvents(enable bool) error {
	domains, err := remote.GetDomains()
	if err != nil {
		return err
	}

	for _, domain := range domains {
		if err := remote.DomainEvents(domain.Name, enable); err != nil {
			return err
		}
	}

	return nil
}

// DOMEvents enables DOM events listening.
func (remote *RemoteDebugger) DOMEvents(enable bool) error {
	return remote.DomainEvents("DOM", enable)
}

// PageEvents enables Page events listening.
func (remote *RemoteDebugger) PageEvents(enable bool) error {
	return remote.DomainEvents("Page", enable)
}

// NetworkEvents enables Network events listening.
func (remote *RemoteDebugger) NetworkEvents(enable bool) error {
	return remote.DomainEvents("Network", enable)
}

// RuntimeEvents enables Runtime events listening.
func (remote *RemoteDebugger) RuntimeEvents(enable bool) error {
	return remote.DomainEvents("Runtime", enable)
}

// LogEvents enables Log events listening.
func (remote *RemoteDebugger) LogEvents(enable bool) error {
	return remote.DomainEvents("Log", enable)
}

// ProfilerEvents enables Profiler events listening.
func (remote *RemoteDebugger) ProfilerEvents(enable bool) error {
	return remote.DomainEvents("Profiler", enable)
}
