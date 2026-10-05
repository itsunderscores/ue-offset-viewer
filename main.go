// SDK Viewer: a fast, single-binary web UI for browsing UE SDK offset dumps.
//
//	go run .                       # serves sdk.txt next to the binary on http://127.0.0.1:8080
//	go run . -file other.txt -addr :9000
//	go run . -export sdk.json      # convert the dump to JSON and exit
package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

//go:embed web
var webFS embed.FS

// defaultSDKPath picks the dump to load when -file is not given: the Desktop
// copy on this Windows machine, otherwise sdk.txt next to the executable, then
// sdk.txt in the working directory.
func defaultSDKPath() string {
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "sdk.txt"))
	}
	candidates = append(candidates, "sdk.txt")
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return candidates[len(candidates)-1]
}

func main() {
	file := flag.String("file", "", "path to the SDK dump (.txt) or a JSON export (.json); default: sdk.txt next to the binary")
	addr := flag.String("addr", "127.0.0.1:8080", "address to listen on")
	export := flag.String("export", "", "write the parsed dataset as JSON to this path and exit")
	pretty := flag.Bool("pretty", false, "indent the JSON written by -export")
	offsetsPath := flag.String("offsets", "", "read-only JSON file with your Important Offsets; default: offsets.json next to the binary")
	open := flag.Bool("open", false, "open the UI in the default browser after starting")
	watch := flag.Bool("watch", true, "reload automatically when the source file changes")
	flag.Parse()

	log.SetFlags(log.Ltime)
	if *file == "" {
		*file = defaultSDKPath()
	}

	db, err := Load(*file)
	if err != nil {
		log.Fatalf("failed to load %s: %v", *file, err)
	}
	log.Printf("loaded %s: %d classes, %d fields, %d lines in %s (%d empty namespaces hidden)",
		*file, len(db.Classes), db.FieldCount, len(db.Lines), db.ParseTime.Round(time.Millisecond), db.SkippedEmpty)

	if *export != "" {
		if err := writeExport(db, *export, *pretty); err != nil {
			log.Fatalf("export failed: %v", err)
		}
		st, _ := os.Stat(*export)
		log.Printf("wrote %s (%.1f MB)", *export, float64(st.Size())/1e6)
		return
	}

	static, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatal(err)
	}
	if *offsetsPath == "" {
		*offsetsPath = "offsets.json"
		if exe, err := os.Executable(); err == nil {
			*offsetsPath = filepath.Join(filepath.Dir(exe), "offsets.json")
		}
	}
	offsets := OpenOffsets(*offsetsPath)
	uiBuild := uiBuildID(static)
	indexHTML, err := prepareIndex(static, uiBuild)
	if err != nil {
		log.Fatal(err)
	}
	srv, err := NewServer(*file, db, static, offsets, indexHTML, uiBuild)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("UI build %s (verify after deploy: curl /api/stats → ui_build)", uiBuild)

	if *watch {
		go offsets.Watch(2 * time.Second)
		go srv.Watch(2*time.Second, func() (int64, time.Time, bool) {
			st, err := os.Stat(*file)
			if err != nil {
				return 0, time.Time{}, false
			}
			return st.Size(), st.ModTime(), true
		})
	}

	host, port, _ := net.SplitHostPort(*addr)
	url := "http://" + *addr
	if host == "" || host == "0.0.0.0" || host == "::" {
		url = "http://localhost:" + port
		log.Printf("SDK viewer listening on all interfaces, port %s (http://<server-ip>:%s)", port, port)
	} else {
		log.Printf("SDK viewer listening on %s", url)
	}
	if *open {
		go openBrowser(url)
	}

	h := &http.Server{
		Addr:              *addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := h.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func writeExport(db *DB, path string, pretty bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	if pretty {
		enc.SetIndent("", "  ")
	}
	return enc.Encode(db.ExportDoc())
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(os.Stderr, "could not open browser: %v\n", err)
	}
}
