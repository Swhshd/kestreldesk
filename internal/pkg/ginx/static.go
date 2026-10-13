package ginx

// This file has been modified for the KestrelDesk project.

import (
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

type noDirFS struct {
	fs http.FileSystem
}

type DirOptions struct {
	ShowList  bool
	SPA       bool
	IndexName string
}

type SPAOptions struct {
	Root             string
	EmbeddedFS       fs.FS
	EmbeddedRoot     string
	DirOptions       DirOptions
	NotFoundPrefixes []string
	NotFoundHandler  gin.HandlerFunc
}

func StaticFiles(root string) http.FileSystem {
	return noDirFS{fs: http.Dir(root)}
}

func (n noDirFS) Open(name string) (http.File, error) {
	file, err := n.fs.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.IsDir() {
		_ = file.Close()
		return nil, os.ErrNotExist
	}
	return file, nil
}

func HandleSPA(engine *gin.Engine, options SPAOptions) gin.HandlerFunc {
	handler := NewSPAHandler(options.Root, options.EmbeddedFS, options.EmbeddedRoot, options.DirOptions)
	engine.GET("/", handler)
	engine.HEAD("/", handler)
	engine.NoRoute(func(ctx *gin.Context) {
		for _, prefix := range options.NotFoundPrefixes {
			if strings.HasPrefix(ctx.Request.URL.Path, prefix) {
				if options.NotFoundHandler != nil {
					options.NotFoundHandler(ctx)
					return
				}
				ctx.AbortWithStatus(http.StatusNotFound)
				return
			}
		}
		handler(ctx)
	})
	return handler
}

func NewSPAHandler(root string, embeddedFS fs.FS, embeddedRoot string, options DirOptions) gin.HandlerFunc {
	if _, err := os.Stat(path.Join(root, options.IndexName)); err == nil {
		return DirHandler(http.Dir(root), options)
	}

	spaFS, err := fs.Sub(embeddedFS, embeddedRoot)
	if err != nil {
		slog.Error("failed to load embedded SPA files", slog.Any("err", err))
		return func(ctx *gin.Context) {
			ctx.AbortWithStatus(http.StatusInternalServerError)
		}
	}
	return DirHandler(http.FS(spaFS), options)
}

func tryOpenFile(fileSystem http.FileSystem, filePath string) (http.File, os.FileInfo, bool) {
	file, err := fileSystem.Open(filePath)
	if err != nil {
		return nil, nil, false
	}
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		_ = file.Close()
		return nil, nil, false
	}
	return file, info, true
}

func DirHandler(fileSystem http.FileSystem, options DirOptions) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		name := path.Clean(ctx.Request.URL.Path)
		if name == "." || name == "/" {
			name = "/" + options.IndexName
		}
		if rscName, ok := nextRSCExportPath(name); ok {
			if file, info, exists := tryOpenFile(fileSystem, rscName); exists {
				defer file.Close()
				serveFile(ctx, rscName, file, info)
				return
			}
		}

		file, err := fileSystem.Open(name)
		if err == nil {
			defer file.Close()
			info, err := file.Stat()
			if err != nil {
				ctx.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			if info.IsDir() {
				if options.IndexName != "" {
					indexName := path.Join(name, options.IndexName)
					if indexFile, indexInfo, ok := tryOpenFile(fileSystem, indexName); ok {
						defer indexFile.Close()
						serveFile(ctx, indexName, indexFile, indexInfo)
						return
					}
				}
				// Support Next.js static export where directory name matches page html (e.g. /dashboard -> /dashboard.html)
				htmlName := strings.TrimRight(name, "/") + ".html"
				if htmlFile, htmlInfo, ok := tryOpenFile(fileSystem, htmlName); ok {
					defer htmlFile.Close()
					serveFile(ctx, htmlName, htmlFile, htmlInfo)
					return
				}
				if !options.ShowList {
					if options.SPA && options.IndexName != "" {
						indexName := "/" + options.IndexName
						if indexFile, indexInfo, ok := tryOpenFile(fileSystem, indexName); ok {
							defer indexFile.Close()
							serveFile(ctx, indexName, indexFile, indexInfo)
							return
						}
					}
					ctx.AbortWithStatus(http.StatusNotFound)
					return
				}
			} else {
				serveFile(ctx, name, file, info)
				return
			}
		} else if !os.IsNotExist(err) {
			ctx.AbortWithStatus(http.StatusInternalServerError)
			return
		}

		// Try name.html (for sub-routes like /dashboard/login -> /dashboard/login.html)
		htmlName := strings.TrimRight(name, "/") + ".html"
		if htmlFile, htmlInfo, ok := tryOpenFile(fileSystem, htmlName); ok {
			defer htmlFile.Close()
			serveFile(ctx, htmlName, htmlFile, htmlInfo)
			return
		}

		if options.SPA && options.IndexName != "" {
			indexName := "/" + options.IndexName
			if indexFile, indexInfo, ok := tryOpenFile(fileSystem, indexName); ok {
				defer indexFile.Close()
				serveFile(ctx, indexName, indexFile, indexInfo)
				return
			}
		}

		ctx.AbortWithStatus(http.StatusNotFound)
	}
}

// nextRSCExportPath maps Next's dot-delimited RSC URL to the nested path used
// by `next build` static exports. For example,
// `/support/__next.!<group>.support.txt` is emitted as
// `/support/__next.!<group>/support.txt`.
func nextRSCExportPath(name string) (string, bool) {
	directory, filename := path.Split(name)
	if !strings.HasPrefix(filename, "__next.!") || path.Ext(filename) != ".txt" {
		return "", false
	}

	stem := strings.TrimSuffix(filename, ".txt")
	segments := strings.Split(stem, ".")
	if len(segments) < 3 || segments[0] != "__next" || !validNextRSCGroup(segments[1]) {
		return "", false
	}
	for _, segment := range segments[2:] {
		if segment == "" || segment == "." || segment == ".." || strings.ContainsAny(segment, `/\\`) {
			return "", false
		}
	}

	parts := make([]string, 0, len(segments)+1)
	parts = append(parts, segments[0]+"."+segments[1])
	parts = append(parts, segments[2:]...)
	parts[len(parts)-1] += ".txt"
	return path.Join(directory, path.Join(parts...)), true
}

func validNextRSCGroup(group string) bool {
	if len(group) < 2 || group[0] != '!' {
		return false
	}
	for _, character := range group[1:] {
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' {
			return false
		}
	}
	return true
}

func serveFile(ctx *gin.Context, name string, file http.File, info os.FileInfo) {
	http.ServeContent(ctx.Writer, ctx.Request, name, info.ModTime(), file)
}
