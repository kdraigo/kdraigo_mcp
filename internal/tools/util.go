package tools

import "fmt"

func httpErr(name string, status int, body []byte) error {
	return fmt.Errorf("%s: HTTP %d: %s", name, status, string(body))
}
