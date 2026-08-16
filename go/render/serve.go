package render

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

var hashedToken = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

var assetExtensions = map[string]bool{
	".js": true, ".mjs": true, ".cjs": true, ".css": true, ".map": true,
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true,
	".webp": true, ".avif": true, ".ico": true, ".bmp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp3": true, ".mp4": true, ".webm": true, ".ogg": true, ".wav": true,
	".json": true, ".txt": true, ".xml": true, ".wasm": true, ".pdf": true,
}

var subresourceDestinations = map[string]bool{
	"script": true, "style": true, "image": true, "font": true,
	"audio": true, "video": true, "track": true, "manifest": true,
	"worker": true, "sharedworker": true, "serviceworker": true,
	"embed": true, "object": true, "paintworklet": true, "xslt": true,
	"report": true,
}

func (renderer *Renderer) serve(writer http.ResponseWriter, request *http.Request) {
	requestPath := path.Clean(request.URL.Path)
	if !strings.HasPrefix(requestPath, "/") {
		requestPath = "/" + requestPath
	}

	if renderer.isReserved(requestPath) {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		http.NotFound(writer, request)
		return
	}
	if hasDotSegment(requestPath) {
		http.NotFound(writer, request)
		return
	}
	if renderer.serveFile(writer, request, requestPath) {
		return
	}
	if _, pattern := renderer.routes.Handler(request); pattern != "" {
		renderer.routes.ServeHTTP(writer, request)
		return
	}
	if isAssetRequest(request, requestPath) {
		http.NotFound(writer, request)
		return
	}
	if !acceptsHTML(request.Header.Get("Accept")) {
		http.NotFound(writer, request)
		return
	}
	renderer.serveShell(writer, request, nil)
}

func hasDotSegment(requestPath string) bool {
	for _, segment := range strings.Split(requestPath, "/") {
		if strings.HasPrefix(segment, ".") && segment != "" {
			return true
		}
	}
	return false
}

func (renderer *Renderer) serveFile(writer http.ResponseWriter, request *http.Request, requestPath string) bool {
	name := strings.TrimPrefix(requestPath, "/")
	if name == "" || name == renderer.index {
		return false
	}
	if !fs.ValidPath(name) {
		return false
	}
	info, err := fs.Stat(renderer.files, name)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	file, err := renderer.files.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()

	seeker, ok := file.(io.ReadSeeker)
	if !ok {
		content, readErr := io.ReadAll(file)
		if readErr != nil {
			return false
		}
		seeker = bytes.NewReader(content)
	}
	writer.Header().Set("Cache-Control", cacheControl(path.Base(name)))
	http.ServeContent(writer, request, path.Base(name), info.ModTime(), seeker)
	return true
}

func cacheControl(name string) string {
	if isContentHashed(name) {
		return "public, max-age=31536000, immutable"
	}
	return "no-cache"
}

func isContentHashed(name string) bool {
	stem := strings.TrimSuffix(name, path.Ext(name))
	separator := strings.LastIndexAny(stem, "-.")
	if separator < 0 {
		return false
	}
	token := stem[separator+1:]
	if len(token) < 8 || !hashedToken.MatchString(token) {
		return false
	}
	hex := true
	mixed := false
	for _, character := range token {
		switch {
		case character >= '0' && character <= '9':
			mixed = true
		case character >= 'a' && character <= 'f':
		case character >= 'A' && character <= 'Z':
			hex = false
			mixed = true
		case character == '_':
			hex = false
			mixed = true
		default:
			hex = false
		}
	}
	return hex || mixed
}

func (renderer *Renderer) resolve(ctx context.Context, resolver Resolver, link Link) (meta *Meta) {
	defer func() {
		if recovered := recover(); recovered != nil {
			meta = nil
			renderer.reportError(ctx, fmt.Errorf("render resolver panicked: %v", recovered))
		}
	}()
	resolved, err := resolver(ctx, link)
	if err != nil {
		renderer.reportError(ctx, err)
		return nil
	}
	return resolved
}

func (renderer *Renderer) reportError(ctx context.Context, err error) {
	if renderer.report == nil {
		return
	}
	renderer.report(ctx, err)
}

func (renderer *Renderer) serveShell(writer http.ResponseWriter, request *http.Request, meta *Meta) {
	body := renderer.render(meta, request.URL.RequestURI())
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(writer, request, "", time.Time{}, bytes.NewReader(body))
}

func isAssetRequest(request *http.Request, requestPath string) bool {
	destination := strings.ToLower(request.Header.Get("Sec-Fetch-Dest"))
	if destination == "document" {
		return false
	}
	if subresourceDestinations[destination] {
		return true
	}
	if requestPath == "/assets" || strings.HasPrefix(requestPath, "/assets/") {
		return true
	}
	return assetExtensions[strings.ToLower(path.Ext(requestPath))]
}

func acceptsHTML(accept string) bool {
	if strings.TrimSpace(accept) == "" {
		return true
	}
	for _, entry := range strings.Split(accept, ",") {
		media := strings.ToLower(strings.TrimSpace(strings.Split(entry, ";")[0]))
		if media == "text/html" || media == "application/xhtml+xml" || media == "*/*" {
			return true
		}
	}
	return false
}
