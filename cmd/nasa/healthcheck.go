package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"
)

// runHealthcheck is the container HEALTHCHECK: it needs no wget or curl in the
// image. It exits 0 when the URL answers 2xx and 1 otherwise.
func runHealthcheck(args []string) error {
	fs := flag.NewFlagSet("healthcheck", flag.ExitOnError)
	url := fs.String("url", env("NASA_HEALTHCHECK_URL", "http://127.0.0.1:8080/readyz"), "address to request (env NASA_HEALTHCHECK_URL)")
	timeout := fs.Duration("timeout", 4*time.Second, "give up after this long")
	fs.Parse(args)
	c := &http.Client{Timeout: *timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(*url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s answered %d: %s", *url, resp.StatusCode, body)
	}
	return nil
}
