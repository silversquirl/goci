package main

import (
	"log"
	"net/http"
	"strings"
)

type ci struct{}

func newCI() (ci ci) {
	return
}

func splitFirst(path string) (first, rest string) {
	path = strings.TrimPrefix(path, "/")
	i := strings.Index(path, "/")
	return path[:i], path[i+1:]
}

var actions = map[string]func(ci ci, project, ref string, w http.ResponseWriter, r *http.Request){
	"":      ci.status,
	"build": ci.build,
	"files": ci.files,
}

func (ci ci) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	project, path := splitFirst(r.URL.Path)
	ref, path := splitFirst(path)
	action, path := splitFirst(path)
	if hf, ok := actions[action]; ok {
		hf(ci, project, ref, w, r)
	} else {
		http.NotFound(w, r)
	}
}

func (ci ci) status(project, ref string, w http.ResponseWriter, r *http.Request) {
}

func (ci ci) build(project, ref string, w http.ResponseWriter, r *http.Request) {
}

func (ci ci) files(project, ref string, w http.ResponseWriter, r *http.Request) {
}

func main() {
	ci := newCI()
	log.Fatal(http.ListenAndServe(":8080", ci))
}
