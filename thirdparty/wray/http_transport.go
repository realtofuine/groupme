package wray

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	// "fmt"
	"io/ioutil"
	"net/http"
	"net/url"
)

// httpClient is used for all Bayeux long-polling requests made by
// HTTPTransport, instead of http.DefaultClient.
//
// This is a local patch (see thirdparty/wray/README.md in the
// mautrix-groupme repo): GroupMe's push server (push.groupme.com/faye)
// was observed to hang / time out (504 Gateway Timeout, or no response at
// all) when this POST-based long-polling transport was negotiated over
// HTTP/2, both from this client and from a plain `curl --http2` against the
// same endpoint from the same host. Plain HTTP/1.1 requests did not
// reproduce the problem in that testing.
//
// Go's http.DefaultClient (used by the original upstream http.Post call
// here) auto-negotiates HTTP/2 over TLS via ALPN whenever the server
// advertises "h2", so a dedicated client with HTTP/2 disabled is used here
// instead of relying on the process-wide default transport.
var httpClient = &http.Client{
	Transport: &http.Transport{
		// A non-nil (even empty) TLSNextProto map tells net/http not to
		// auto-upgrade TLS connections to HTTP/2, forcing HTTP/1.1.
		TLSNextProto: make(map[string]func(authority string, c *tls.Conn) http.RoundTripper),
	},
}

// HTTPTransport models a faye protocol transport over HTTP long polling
type HTTPTransport struct {
	url string
}

func (t HTTPTransport) isUsable(clientURL string) bool {
	parsedURL, err := url.Parse(clientURL)
	if err != nil {
		return false
	}
	if parsedURL.Scheme == "http" || parsedURL.Scheme == "https" {
		return true
	}
	return false
}

func (t HTTPTransport) connectionType() string {
	return "long-polling"
}

func (t HTTPTransport) send(msg json.Marshaler) (decoder, error) {
	b, err := json.Marshal(msg)

	if err != nil {
		return nil, err
	}

	buffer := bytes.NewBuffer(b)
	responseData, err := httpClient.Post(t.url, "application/json", buffer)
	if err != nil {
		return nil, err
	}
	if responseData.StatusCode != 200 {
		return nil, errors.New(responseData.Status)
	}
	defer responseData.Body.Close()
	jsonData, err := ioutil.ReadAll(responseData.Body)
	if err != nil {
		return nil, err
	}
	// fmt.Println("BUFFFERERERERER!!! ", string(jsonData))
	return json.NewDecoder(bytes.NewBuffer(jsonData)), nil
}

func (t *HTTPTransport) setURL(url string) {
	t.url = url
}
