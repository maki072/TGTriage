// Package netutil provides shared outbound HTTP transport helpers, notably optional SOCKS5
// proxying for hosts where some or all destinations (Telegram, LLM providers) are blocked
// directly but reachable through a local bypass (e.g. an Xray/V2Ray SOCKS5 inbound).
package netutil

import (
	"context"
	"net"
	"net/http"
	"time"

	"golang.org/x/net/proxy"
)

// NewTransport builds an http.Transport with sane pooling defaults. When socks5Addr is
// non-empty (e.g. "127.0.0.1:1080"), every connection is dialed through that unauthenticated
// SOCKS5 proxy instead of directly; otherwise it behaves like a normal direct transport
// (honoring HTTP_PROXY/HTTPS_PROXY env vars, same as before this existed).
func NewTransport(socks5Addr string) *http.Transport {
	t := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if socks5Addr == "" {
		return t
	}
	dialer, err := proxy.SOCKS5("tcp", socks5Addr, nil, proxy.Direct)
	if err != nil {
		// proxy.SOCKS5 only errors on invalid auth config, which is never passed here.
		panic("netutil: invalid SOCKS5 proxy config: " + err.Error())
	}
	t.Proxy = nil // the SOCKS5 dialer replaces the HTTP(S) CONNECT proxy path entirely
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if d, ok := dialer.(proxy.ContextDialer); ok {
			return d.DialContext(ctx, network, addr)
		}
		return dialer.Dial(network, addr) // fallback: the bundled SOCKS5 dialer always implements ContextDialer
	}
	return t
}
