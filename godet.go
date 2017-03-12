package main

import (
	"encoding/json"
	"flag"
	"fmt"

	"github.com/gobs/httpclient"
	"golang.org/x/net/websocket"
)

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
	http *httpclient.HttpClient
	ws   *websocket.Conn
}

//
// Connect to the remote debugger and return `RemoteDebugger` object
//
func Connect(port string) (*RemoteDebugger, error) {
	remote := &RemoteDebugger{}

	remote.http = httpclient.NewHttpClient("http://" + port)

	if tablist, err := remote.TabList(""); err != nil {
		return nil, err
	} else if remote.ws, err = websocket.Dial(tablist[0].WsUrl, "", "http://localhost"); err != nil {
		return nil, err
	}

	return remote, nil
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

	dec := json.NewDecoder(resp.Body)
	err = dec.Decode(&version)

	resp.Close()
	return &version, err
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

	var tablist []*Tab

	dec := json.NewDecoder(resp.Body)
	err = dec.Decode(&tablist)

	resp.Close()

	if err != nil {
		return nil, err
	}

	if filter == "" {
		return tablist, nil
	}

	var filtered []*Tab

	for _, t := range tablist {
		if t.Type == filter {
			filtered = append(filtered, t)
		}
	}

	return filtered, nil
}

//
// Close specified tab
//
func (remote *RemoteDebugger) Close(tab *Tab) error {
	resp, err := remote.http.Get("/json/close/"+tab.Id, nil, nil)
	resp.Close()
	return err
}

func main() {
	port := flag.String("port", "localhost:9222", "Chrome remote debugger port")
	flag.Parse()

	remote, _ := Connect(*port)

	fmt.Println()
	fmt.Println("Version:")
	fmt.Println(remote.Version())

	fmt.Println()
	fmt.Println(remote.TabList("page"))
}
