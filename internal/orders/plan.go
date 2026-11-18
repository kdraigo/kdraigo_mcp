package orders

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kdraigo/dev_sdk/orderplan"

	"github.com/kdraigo/kdraigo_mcp/internal/client"
)

func asStatus(err error, target **client.StatusError) bool { return errors.As(err, target) }

// drawnPosition is a long-position or short-position drawing's prices, read
// as research_service reads them (service.viewDrawing): the first point is
// the entry; style.stopLevel and style.profitLevel are distances from it.
type drawnPosition struct {
	Side   orderplan.Side
	Entry  float64
	Stop   float64
	Target float64
}

func readPosition(d Drawing) (drawnPosition, error) {
	var b struct {
		Kind   string `json:"kind"`
		Points []struct {
			Price float64 `json:"price"`
		} `json:"points"`
		Style struct {
			StopLevel   *float64 `json:"stopLevel"`
			ProfitLevel *float64 `json:"profitLevel"`
		} `json:"style"`
	}
	if err := json.Unmarshal(d.Body, &b); err != nil {
		return drawnPosition{}, fmt.Errorf("drawing %q could not be read: %w", d.ID, err)
	}
	var p drawnPosition
	switch b.Kind {
	case "long-position":
		p.Side = orderplan.Long
	case "short-position":
		p.Side = orderplan.Short
	default:
		return p, fmt.Errorf("drawing %q is a %s: an order comes from a long-position or short-position drawing", d.ID, b.Kind)
	}
	if len(b.Points) == 0 || b.Style.StopLevel == nil || b.Style.ProfitLevel == nil {
		return p, fmt.Errorf("drawing %q has no stop and target", d.ID)
	}
	p.Entry = b.Points[0].Price
	sign := 1.0
	if p.Side == orderplan.Short {
		sign = -1
	}
	p.Stop = p.Entry - sign**b.Style.StopLevel
	p.Target = p.Entry + sign**b.Style.ProfitLevel
	return p, nil
}

// findDrawing picks a drawing by id.
func findDrawing(list []Drawing, id string) (Drawing, bool) {
	for _, d := range list {
		if d.ID == id {
			return d, true
		}
	}
	return Drawing{}, false
}

// failedChecks is the failed checks, one per line.
func failedChecks(cs []orderplan.Check) string {
	var out []string
	for _, c := range cs {
		if !c.OK {
			out = append(out, c.Name+": "+c.Detail)
		}
	}
	return strings.Join(out, "\n")
}
