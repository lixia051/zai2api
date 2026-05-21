package internal

import (
	_ "embed"
	"net/http"
)

//go:embed admin_ui.html
var adminUIHTML []byte

// HandleAdminUI GET /admin/ 返回单页应用 HTML
func HandleAdminUI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(adminUIHTML)
}
