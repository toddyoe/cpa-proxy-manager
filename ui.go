package main

// ui.go embeds the single-page management UI. The page is a standalone HTML
// document with no frontend framework, served through the plugin resource
// route; it talks to the plugin's management routes only.
import _ "embed"

//go:embed ui.html
var uiHTML []byte

func renderUIPage() []byte {
	return uiHTML
}
