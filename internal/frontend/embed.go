package frontend

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/valyala/fasthttp"
	"github.com/valyala/fasthttp/fasthttpadaptor"
)

// mimeTypes maps file extensions to MIME types
var mimeTypes = map[string]string{
	".js":    "application/javascript",
	".mjs":   "application/javascript",
	".css":   "text/css",
	".html":  "text/html",
	".json":  "application/json",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".svg":   "image/svg+xml",
	".ico":   "image/x-icon",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".eot":   "application/vnd.ms-fontobject",
}

//go:embed all:dist
var distFS embed.FS

// HTTPHandler returns a net/http handler that serves the embedded frontend (SPA).
// Prefer this during/after the chi migration; Handler() remains for fasthttp callers.
func HTTPHandler(basePath string) http.Handler {
	basePath = strings.TrimSuffix(basePath, "/")

	distSubFS, err := fs.Sub(distFS, "dist")
	if err != nil {
		return notEmbeddedHTTPHandler("Frontend not embedded: " + err.Error())
	}

	indexContent, err := fs.ReadFile(distSubFS, "index.html")
	if err != nil {
		return notEmbeddedHTTPHandler("Frontend not embedded: index.html not found. Run 'make build-prod' to embed frontend.")
	}

	baseHref := basePath + "/"
	if basePath == "" {
		baseHref = "/"
	}
	baseTag := fmt.Sprintf(`<head><base href="%s">`, baseHref)
	modifiedHTML := strings.Replace(string(indexContent), "<head>", baseTag, 1)
	basePathScript := fmt.Sprintf(`<script>window.__BASE_PATH__ = "%s";</script></head>`, basePath)
	indexHTML := []byte(strings.Replace(modifiedHTML, "</head>", basePathScript, 1))

	fileServer := http.FileServer(http.FS(distSubFS))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if path != "/" && !strings.HasPrefix(path, "/api") {
			filePath := strings.TrimPrefix(path, "/")
			file, err := distSubFS.Open(filePath)
			if err == nil {
				defer func() { _ = file.Close() }()
				stat, err := file.Stat()
				if err != nil {
					http.Error(w, "Internal Server Error", http.StatusInternalServerError)
					return
				}
				if stat.IsDir() {
					fileServer.ServeHTTP(w, r)
					return
				}
				ext := strings.ToLower(filepath.Ext(filePath))
				if mimeType, ok := mimeTypes[ext]; ok {
					w.Header().Set("Content-Type", mimeType)
				} else {
					w.Header().Set("Content-Type", "application/octet-stream")
				}
				acceptEncoding := r.Header.Get("Accept-Encoding")
				var content []byte
				var contentEncoding string
				if strings.Contains(acceptEncoding, "br") {
					if brContent, err := fs.ReadFile(distSubFS, filePath+".br"); err == nil {
						content = brContent
						contentEncoding = "br"
					}
				}
				if content == nil && strings.Contains(acceptEncoding, "gzip") {
					if gzContent, err := fs.ReadFile(distSubFS, filePath+".gz"); err == nil {
						content = gzContent
						contentEncoding = "gzip"
					}
				}
				if content == nil {
					content, err = fs.ReadFile(distSubFS, filePath)
					if err != nil {
						http.Error(w, "Internal Server Error", http.StatusInternalServerError)
						return
					}
				}
				if contentEncoding != "" {
					w.Header().Set("Content-Encoding", contentEncoding)
				}
				w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))
				_, _ = w.Write(content)
				return
			}
		}

		if path == "/" || (!strings.HasPrefix(path, "/api") && !strings.Contains(path, ".")) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(indexHTML)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func notEmbeddedHTTPHandler(message string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(message))
	})
}

// Handler returns a fasthttp handler that serves the embedded frontend files.
// Deprecated for new code paths: prefer HTTPHandler and mount on net/http/chi.
func Handler(basePath string) fasthttp.RequestHandler {
	return fasthttpadaptor.NewFastHTTPHandler(HTTPHandler(basePath))
}

// IsEmbedded returns true if the frontend dist folder is embedded
func IsEmbedded() bool {
	entries, err := distFS.ReadDir("dist")
	if err != nil {
		return false
	}
	return len(entries) > 0
}

// notEmbeddedHandler returns a handler that displays a message when frontend is not embedded
func notEmbeddedHandler(message string) fasthttp.RequestHandler {
	return func(ctx *fasthttp.RequestCtx) {
		ctx.SetContentType("text/plain; charset=utf-8")
		ctx.SetStatusCode(fasthttp.StatusNotFound)
		_, _ = ctx.WriteString(message)
	}
}
