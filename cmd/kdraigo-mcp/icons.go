package main

import (
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// serverIcons puts kdraigo's logo in the server's initialize answer, so an
// MCP client can show it beside the server's name. The files are the
// website's; server.json lists the same PNG for the registry.
func serverIcons() server.ServerOption {
	return server.WithIcons(
		mcp.Icon{Src: "https://kdraigo.com/favicon.svg", MIMEType: "image/svg+xml", Sizes: []string{"any"}},
		mcp.Icon{Src: "https://kdraigo.com/icon-512.png", MIMEType: "image/png", Sizes: []string{"512x512"}},
	)
}
