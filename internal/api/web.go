package api

import (
	"embed"
	"net/http"
)

//go:embed web/index.html web/board.js web/board.css
var webAssets embed.FS

func asset(name, contentType string) http.HandlerFunc {
	data, err := webAssets.ReadFile("web/" + name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if name == "index.html" {
			w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		}
		_, _ = w.Write(data)
	}
}
