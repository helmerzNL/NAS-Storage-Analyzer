package api

import (
	"fmt"
	"net"
)

// CheckListenAddr refuses to bind the unauthenticated API to anything but a
// loopback address unless allowRemote is set. Authentication arrives with
// phase 3; until then the API must not be reachable from other machines by
// accident (an empty host such as ":8080" means every interface).
func CheckListenAddr(addr string, allowRemote bool) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", addr, err)
	}
	if allowRemote || isLoopbackHost(host) {
		return nil
	}
	return fmt.Errorf("refusing to listen on %q: the API has no authentication yet. "+
		"Bind to 127.0.0.1 or pass -allow-unauthenticated-remote if a trusted reverse proxy protects it", addr)
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
