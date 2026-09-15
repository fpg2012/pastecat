// Copyright (c) 2014-2015, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package main

import (
	"errors"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/mvdan/pastecat/storage"
)

const (
	// Name of the HTTP form field when uploading a paste
	fieldName = "paste"
	// Content-Type when serving pastes
	contentType = "text/plain; charset=utf-8"
	// Report usage stats how often
	reportInterval = 1 * time.Minute

	// HTTP response strings
	invalidID     = "invalid paste id"
	unknownAction = "unsupported action"
)

var (
	siteURL   = flag.String("u", "http://localhost:8080", "URL of the site")
	listen    = flag.String("l", ":8080", "Host and port to listen to")
	lifeTime  = flag.Duration("t", 24*time.Hour, "Lifetime of the pastes")
	timeout   = flag.Duration("T", 5*time.Second, "Timeout of HTTP requests")
	maxNumber = flag.Int("m", 0, "Maximum number of pastes to store at once")

	maxSize    = 1 * storage.MB
	maxStorage = 1 * storage.GB
)

func init() {
	flag.Var(&maxSize, "s", "Maximum size of pastes")
	flag.Var(&maxStorage, "M", "Maximum storage size to use at once")
}

func getContentFromForm(r *http.Request) ([]byte, error) {
	if value := r.FormValue(fieldName); len(value) > 0 {
		return []byte(value), nil
	}
	if f, _, err := r.FormFile(fieldName); err == nil {
		defer f.Close()
		content, err := ioutil.ReadAll(f)
		if err == nil && len(content) > 0 {
			return content, nil
		}
	}
	return nil, errors.New("no paste provided")
}

func setHeaders(header http.Header, id storage.ID, paste storage.Paste) {
	modTime := paste.ModTime()
	header.Set("Etag", fmt.Sprintf(`"%d-%s"`, modTime.Unix(), id))
	if *lifeTime > 0 {
		deathTime := modTime.Add(*lifeTime)
		lifeLeft := deathTime.Sub(time.Now())
		header.Set("Expires", deathTime.UTC().Format(http.TimeFormat))
		header.Set("Cache-Control", fmt.Sprintf(
			"max-age=%.f, must-revalidate", lifeLeft.Seconds()))
	}
	header.Set("Content-Type", contentType)
}

type httpHandler struct {
	store storage.Store
	stats *storage.Stats
}

func (h httpHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		h.handleGet(w, r)
	case "POST":
		h.handlePost(w, r)
	default:
		http.Error(w, unknownAction, http.StatusBadRequest)
	}
}

// pageData is passed to every HTML template. Templates ignore the fields they
// do not need.
type pageData struct {
	SiteURL   string
	MaxSize   storage.ByteSize
	LifeTime  time.Duration
	FieldName string
	// ID and Content are only used by the editor template
	ID      string
	Content string
}

func (h *httpHandler) executeTemplate(w http.ResponseWriter, name string, data pageData) {
	data.SiteURL = *siteURL
	data.MaxSize = maxSize
	data.LifeTime = *lifeTime
	data.FieldName = fieldName
	if err := tmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("Error executing template for %s: %v", name, err)
	}
}

// wantsHTML reports whether the client is a web browser that would prefer the
// editable web interface over the raw paste contents.
func wantsHTML(r *http.Request) bool {
	if _, raw := r.URL.Query()["raw"]; raw {
		return false
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// reservedNames are URL paths that are handled specially and therefore cannot
// be used as paste names.
var reservedNames = map[string]bool{
	"form":        true,
	"redirect":    true,
	"favicon.ico": true,
}

// nameToID maps a URL path to a paste id. Any non-empty name of up to the
// maximum length is accepted.
func nameToID(name string) (storage.ID, bool) {
	if name == "" || reservedNames[name] {
		return "", false
	}
	id, err := storage.NewID(name)
	if err != nil {
		return "", false
	}
	return id, true
}

func (h *httpHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	if wantsHTML(r) {
		h.handleWeb(w, r)
		return
	}
	if _, e := templates[r.URL.Path]; e {
		h.executeTemplate(w, r.URL.Path, pageData{})
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/")
	id, ok := nameToID(name)
	if !ok {
		http.Error(w, invalidID, http.StatusBadRequest)
		return
	}
	h.servePaste(w, r, id)
}

func (h *httpHandler) servePaste(w http.ResponseWriter, r *http.Request, id storage.ID) {
	paste, err := h.store.Get(id)
	if err == storage.ErrPasteNotFound {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	} else if err != nil {
		log.Printf("Unknown error on GET: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer paste.Close()
	setHeaders(w.Header(), id, paste)
	http.ServeContent(w, r, "", paste.ModTime(), paste)
}

// handleWeb serves the editable web interface. Unknown or invalid paste names
// result in a new random paste.
func (h *httpHandler) handleWeb(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	id, ok := nameToID(name)
	if !ok {
		h.redirectToNew(w, r)
		return
	}
	content, err := h.readPaste(id)
	if err == storage.ErrPasteNotFound {
		if err := h.putPaste(id, nil); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	} else if err != nil {
		log.Printf("Unknown error on GET: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.executeTemplate(w, "/edit", pageData{ID: name, Content: string(content)})
}

func (h *httpHandler) redirectToNew(w http.ResponseWriter, r *http.Request) {
	id, err := h.createPaste(nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, "/"+id.String(), http.StatusFound)
}

func (h *httpHandler) readPaste(id storage.ID) ([]byte, error) {
	paste, err := h.store.Get(id)
	if err != nil {
		return nil, err
	}
	defer paste.Close()
	return ioutil.ReadAll(paste)
}

func (h *httpHandler) pasteSize(id storage.ID) (int64, bool, error) {
	paste, err := h.store.Get(id)
	if err == storage.ErrPasteNotFound {
		return 0, false, nil
	} else if err != nil {
		return 0, false, err
	}
	defer paste.Close()
	return paste.Size(), true, nil
}

// createPaste stores a new paste under a random id.
func (h *httpHandler) createPaste(content []byte) (storage.ID, error) {
	size := int64(len(content))
	if err := h.stats.MakeSpaceFor(size); err != nil {
		return "", err
	}
	id, err := h.store.Put(content)
	if err != nil {
		h.stats.FreeSpace(size)
		return id, err
	}
	storage.SetupPasteDeletion(h.store, h.stats, id, size, *lifeTime)
	return id, nil
}

// putPaste creates or overwrites the paste stored under id.
func (h *httpHandler) putPaste(id storage.ID, content []byte) error {
	size := int64(len(content))
	oldSize, existed, err := h.pasteSize(id)
	if err != nil {
		return err
	}
	if existed {
		if err := h.stats.Resize(oldSize, size); err != nil {
			return err
		}
	} else if err := h.stats.MakeSpaceFor(size); err != nil {
		return err
	}
	if err := h.store.PutWithID(id, content); err != nil {
		if existed {
			h.stats.Resize(size, oldSize)
		} else {
			h.stats.FreeSpace(size)
		}
		return err
	}
	storage.SetupPasteDeletion(h.store, h.stats, id, size, *lifeTime)
	return nil
}

func (h *httpHandler) handlePost(w http.ResponseWriter, r *http.Request) {
	// A POST to a valid paste name saves that paste (used by the web editor).
	if name := strings.TrimPrefix(r.URL.Path, "/"); name != "" {
		if id, ok := nameToID(name); ok {
			h.handleSave(w, r, id)
			return
		}
	}
	h.limitBody(w, r)
	content, err := getContentFromForm(r)
	size := int64(len(content))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.stats.MakeSpaceFor(size); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	}
	id, err := h.store.Put(content)
	if err != nil {
		log.Printf("Unknown error on POST: %v", err)
		h.stats.FreeSpace(size)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	storage.SetupPasteDeletion(h.store, h.stats, id, size, *lifeTime)
	url := fmt.Sprintf("%s/%s", *siteURL, id)
	switch r.URL.Path {
	case "/redirect":
		http.Redirect(w, r, url, 302)
	default:
		fmt.Fprintln(w, url)
	}
}

// handleSave stores the request body as the paste with the given id.
func (h *httpHandler) handleSave(w http.ResponseWriter, r *http.Request, id storage.ID) {
	h.limitBody(w, r)
	content, err := ioutil.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	if err := h.putPaste(id, content); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// limitBody caps the request body at maxSize, unless maxSize is zero (meaning
// no limit).
func (h *httpHandler) limitBody(w http.ResponseWriter, r *http.Request) {
	if maxSize > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, int64(maxSize))
	}
}

func (h *httpHandler) setupStore(lifeTime time.Duration, storageType string, args []string) error {
	params, e := map[string]map[string]string{
		"fs": {
			"dir": "pastes",
		},
		"fs-mmap": {
			"dir": "pastes",
		},
		"mem": {},
	}[storageType]
	if !e {
		return fmt.Errorf("unknown storage type '%s'", storageType)
	}
	if len(args) > len(params) {
		return fmt.Errorf("too many arguments given for %s", storageType)
	}
	for k := range params {
		if len(args) == 0 {
			break
		}
		params[k] = args[0]
		args = args[1:]
	}
	var err error
	switch storageType {
	case "fs":
		log.Printf("Starting up file store in the directory '%s'", params["dir"])
		h.store, err = storage.NewFileStore(h.stats, lifeTime, params["dir"])
	case "fs-mmap":
		log.Printf("Starting up mmapped file store in the directory '%s'", params["dir"])
		h.store, err = storage.NewMmapStore(h.stats, lifeTime, params["dir"])
	case "mem":
		log.Printf("Starting up in-memory store")
		h.store, err = storage.NewMemStore()
	}
	return err
}

func logStats(stats *storage.Stats) {
	num, stg := stats.Report()
	var numStats, stgStats string
	if stats.MaxNumber > 0 {
		numStats = fmt.Sprintf("%d (%.2f%% out of %d)", num,
			float64(num*100)/float64(stats.MaxNumber), stats.MaxNumber)
	} else {
		numStats = fmt.Sprintf("%d", num)
	}
	if stats.MaxStorage > 0 {
		stgStats = fmt.Sprintf("%s (%.2f%% out of %s)", storage.ByteSize(stg),
			float64(stg*100)/float64(stats.MaxStorage), storage.ByteSize(stats.MaxStorage))
	} else {
		stgStats = fmt.Sprintf("%s", storage.ByteSize(stg))
	}
	log.Printf("Have a total of %s pastes using %s", numStats, stgStats)
}

func main() {
	flag.Parse()
	if maxStorage > 1*storage.EB {
		log.Fatalf("Specified a maximum storage size that would overflow int64!")
	}
	if maxSize > 1*storage.EB {
		log.Fatalf("Specified a maximum paste size that would overflow int64!")
	}
	loadTemplates()
	var handler httpHandler
	handler.stats = &storage.Stats{
		MaxNumber:  *maxNumber,
		MaxStorage: int64(maxStorage),
	}
	log.Printf("siteURL    = %s", *siteURL)
	log.Printf("listen     = %s", *listen)
	log.Printf("lifeTime   = %s", *lifeTime)
	log.Printf("maxSize    = %s", maxSize)
	log.Printf("maxNumber  = %d", *maxNumber)
	log.Printf("maxStorage = %s", maxStorage)

	args := flag.Args()
	if len(args) == 0 {
		args = []string{"fs"}
	}
	if err := handler.setupStore(*lifeTime, args[0], args[1:]); err != nil {
		log.Fatalf("Could not setup paste store: %v", err)
	}

	ticker := time.NewTicker(reportInterval)
	go func() {
		logStats(handler.stats)
		for range ticker.C {
			logStats(handler.stats)
		}
	}()
	var finalHandler http.Handler = handler
	if *timeout > 0 {
		finalHandler = http.TimeoutHandler(finalHandler, *timeout, "")
	}
	http.Handle("/", finalHandler)
	log.Println("Up and running!")
	log.Fatal(http.ListenAndServe(*listen, nil))
}
