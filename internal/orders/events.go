package orders

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
)

// event is one server-sent event of the executor stream.
type event struct {
	Name string
	Data string
}

// decision is a "decided" event, and each entry of "ready".
type decision struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Hash   string `json:"hash"`
}

// readEvents reads server-sent events until the stream ends: "event:" and
// "data:" lines, a blank line closing each; ":" lines are keep-alives.
func readEvents(r io.Reader, on func(event)) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	var ev event
	var data []string
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if ev.Name != "" || len(data) > 0 {
				ev.Data = strings.Join(data, "\n")
				on(ev)
			}
			ev, data = event{}, nil
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			ev.Name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return sc.Err()
}

// decisions is what an event asks the executor to act on: the confirmed
// proposals in "ready", or a "decided" one.
func decisions(ev event) []decision {
	switch ev.Name {
	case "ready":
		var r struct {
			Confirmed []decision `json:"confirmed"`
		}
		if json.Unmarshal([]byte(ev.Data), &r) == nil {
			return r.Confirmed
		}
	case "decided":
		var d decision
		if json.Unmarshal([]byte(ev.Data), &d) == nil && d.ID != "" {
			return []decision{d}
		}
	}
	return nil
}
