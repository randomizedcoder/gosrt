package srt

import (
	"fmt"
	"net/url"
)

// UnmarshalURL takes a SRT URL and parses out the configuration. A SRT URL is
// srt://[host]:[port]?[key1]=[value1]&[key2]=[value2]... It returns the host:port
// of the URL.
func (c *Config) UnmarshalURL(srturl string) (string, error) {
	u, err := url.Parse(srturl)
	if err != nil {
		return "", err
	}

	if u.Scheme != "srt" {
		return "", fmt.Errorf("the URL doesn't seem to be an srt:// URL")
	}

	return u.Host, c.UnmarshalQuery(u.RawQuery)
}

// UnmarshalQuery parses a query string and interprets it as a configuration
// for a SRT connection. The key in each key/value pair corresponds to the
// respective field in the Config type, but with only lower case letters. Bool
// values can be represented as "true"/"false", "on"/"off", "yes"/"no", or "0"/"1".
//
// This function uses a table-driven approach, reducing cyclomatic complexity
// from 74 to ~10.
func (c *Config) UnmarshalQuery(query string) error {
	return c.unmarshalQueryTable(query)
}

// MarshalURL returns the SRT URL for this config and the given address (host:port).
func (c *Config) MarshalURL(address string) string {
	return "srt://" + address + "?" + c.MarshalQuery()
}

// MarshalQuery returns the corresponding query string for a configuration.
//
// This function uses a table-driven approach, reducing cyclomatic complexity
// from 37 to ~10.
func (c *Config) MarshalQuery() string {
	return c.marshalQueryTable()
}
