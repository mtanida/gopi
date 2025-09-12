package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

func main() {
	var dirPrefix string
	flag.StringVar(&dirPrefix, "prefix", ".", "Directory prefix for all operations")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		// Try to read the directory to verify we have access
		_, err := os.ReadDir(dirPrefix)
		if err != nil {
			log.Printf("Liveness check failed: %v\n", err)
			http.Error(w, "Cannot read directory", http.StatusInternalServerError)
			return
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dirPrefix, r.URL.Path)

		f, err := os.Open(path)
		if err != nil {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		defer f.Close()

		fileInfo, err := f.Stat()
		if err != nil {
			http.Error(w, "Error getting file info", http.StatusInternalServerError)
			return
		}

		if fileInfo.IsDir() {
			files, err := f.ReadDir(-1)
			if err != nil {
				http.Error(w, "Error reading directory", http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<!DOCTYPE html>\n")
			fmt.Fprintf(w, "<html lang=\"en\">\n")
			fmt.Fprintf(w, "<head>\n")
			fmt.Fprintf(w, "  <meta charset=\"utf-8\">\n")
			fmt.Fprintf(w, "  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
			fmt.Fprintf(w, "  <title>Directory listing for %s</title>\n", path)
			fmt.Fprintf(w, "</head>\n")
			fmt.Fprintf(w, "<body>\n")
			fmt.Fprintf(w, "  <header>\n")
			fmt.Fprintf(w, "    <h1>Links for %s</h1>\n", path)
			fmt.Fprintf(w, "  </header>\n")
			fmt.Fprintf(w, "  <main>\n")
			fmt.Fprintf(w, "    <ul>\n")
			for _, file := range files {
				name := file.Name()
				if file.IsDir() {
					name += "/"
				}
				fmt.Fprintf(w, "      <li><a href=\"%s\">%s</a></li>\n", filepath.Join(name), name)
			}
			fmt.Fprintf(w, "    </ul>\n")
			fmt.Fprintf(w, "  </main>\n")
			fmt.Fprintf(w, "</body>\n")
			fmt.Fprintf(w, "</html>\n")
		} else {
			http.ServeFile(w, r, path)
		}
	})

	mux.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseMultipartForm(10 << 20) // 10 MB max memory
		if err != nil {
			http.Error(w, "Unable to parse form", http.StatusBadRequest)
			return
		}

		// Check for "name" key and create directory if it exists
		var dirName string
		if names, ok := r.MultipartForm.Value["name"]; ok && len(names) > 0 {
			dirName = names[0]
			err := os.Mkdir(filepath.Join(dirPrefix, dirName), 0755)
			if err != nil && !os.IsExist(err) {
				log.Printf("Error creating directory: %v\n", err)
				http.Error(w, "Unable to create directory", http.StatusInternalServerError)
				return
			}
			log.Printf("Created directory: %s\n", dirName)
		} else {
			http.Error(w, "Directory name not provided", http.StatusBadRequest)
			return
		}

		// Save uploaded files to the created directory
		for key, files := range r.MultipartForm.File {
			for _, file := range files {
				log.Printf("File: %s, Name: %s, Size: %d bytes\n", key, file.Filename, file.Size)

				// Open the uploaded file
				src, err := file.Open()
				if err != nil {
					log.Printf("Error opening uploaded file: %v\n", err)
					continue
				}

				// Check if the file already exists
				filePath := filepath.Join(dirPrefix, dirName, file.Filename)
				log.Printf("Checking if file already exists: %s\n", filePath)
				if _, err := os.Stat(filePath); err == nil {
					log.Printf("File already exists: %s\n", filePath)
					http.Error(w, "File already exists", http.StatusConflict)
					src.Close()
					return
				}

				// Create the destination file
				dst, err := os.OpenFile(
					filePath,
					os.O_WRONLY|os.O_CREATE|os.O_EXCL,
					0444,
				)
				if err != nil {
					log.Printf("Error creating destination file: %v\n", err)
					http.Error(w, "Unable to create file", http.StatusInternalServerError)
					src.Close()
					return
				}

				// Copy the uploaded file to the destination file
				writtenSize, err := io.Copy(dst, src)
				src.Close()
				dst.Close()
				if err != nil || writtenSize != file.Size {
					log.Printf("Error copying file: %v\n", err)
					// Delete the partially written file
					removeErr := os.Remove(filePath)
					if removeErr != nil {
						log.Printf("Error removing partial file: %v\n", removeErr)
					}
					http.Error(w, "Error copying file", http.StatusInternalServerError)
					return
				}
				log.Printf("File saved: %s\n", filePath)
			}
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Form data received and printed"))
	})

	mux.HandleFunc("DELETE /", func(w http.ResponseWriter, r *http.Request) {
		relPath := r.URL.Path
		// Safety checks: block root, empty, or suspicious paths
		if relPath == "/" || relPath == "" || relPath == "*" || relPath == "/*" {
			http.Error(w, "Refusing to delete root or wildcard path", http.StatusForbidden)
			return
		}
		// Prevent attempts to delete outside the prefix
		path := filepath.Join(dirPrefix, relPath)
		absPrefix, _ := filepath.Abs(dirPrefix)
		absPath, _ := filepath.Abs(path)
		if absPrefix == absPath {
			http.Error(w, "Refusing to delete root directory", http.StatusForbidden)
			return
		}
		if len(relPath) == 0 || relPath == "/" || relPath == "*" || relPath == "/*" {
			http.Error(w, "Invalid delete path", http.StatusForbidden)
			return
		}
		info, err := os.Stat(path)
		if err != nil {
			http.Error(w, "File or directory not found", http.StatusNotFound)
			return
		}
		// Remove file or directory
		var removeErr error
		if info.IsDir() {
			removeErr = os.RemoveAll(path)
		} else {
			removeErr = os.Remove(path)
		}
		if removeErr != nil {
			log.Printf("Error deleting: %v\n", removeErr)
			http.Error(w, "Unable to delete", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Deleted"))
	})

	srv := http.Server{
		Addr:    ":8080",
		Handler: mux,
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-quit
		log.Println("Shutting down...")
		if err := srv.Shutdown(context.Background()); err != nil {
			log.Fatal(err)
		}
	}()

	log.Println("Starting server on :8080...")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
