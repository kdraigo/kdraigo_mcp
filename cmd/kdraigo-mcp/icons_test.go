package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

// The logo reaches a client in the initialize answer's serverInfo.
func TestInitializeNamesTheLogo(t *testing.T) {
	s := server.NewMCPServer("kdraigo", "test", serverIcons())
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	raw, err := json.Marshal(s.HandleMessage(context.Background(), json.RawMessage(req)))
	if err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result struct {
			ServerInfo struct {
				Icons []struct {
					Src      string   `json:"src"`
					MIMEType string   `json:"mimeType"`
					Sizes    []string `json:"sizes"`
				} `json:"icons"`
			} `json:"serverInfo"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	icons := resp.Result.ServerInfo.Icons
	if len(icons) != 2 || icons[0].Src != "https://kdraigo.com/favicon.svg" || icons[1].Src != "https://kdraigo.com/icon-512.png" {
		t.Fatalf("serverInfo.icons = %+v (answer %s)", icons, raw)
	}
}
