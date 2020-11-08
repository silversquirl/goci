package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

type CI struct {
	Path string
	proj map[string]*Project
}

func NewCI(root string) *CI {
	return &CI{root, make(map[string]*Project)}
}

func splitFirst(route string) (first, rest string) {
	route = strings.TrimPrefix(route, "/")
	i := strings.Index(route, "/")
	if i < 0 {
		return route, ""
	}
	return route[:i], route[i+1:]
}

var actions = map[string]func(ci *CI, build *Build, route string, w http.ResponseWriter, r *http.Request){
	"":      (*CI).Status,
	"files": (*CI).Files,
}

func (ci *CI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	project, rest := splitFirst(r.URL.Path)
	ref, rest := splitFirst(rest)
	action, rest := splitFirst(rest)

	actionFunc, ok := actions[action]
	if !ok {
		http.NotFound(w, r)
		return
	}

	if project == "" || project[0] == '.' {
		http.NotFound(w, r)
		return
	}

	if ref == "" {
		ref = "master"
	} else if ref[0] == '-' {
		http.NotFound(w, r)
		return
	}

	proj, err := ci.Project(project)
	if err != nil {
		log.Print(err)
		http.NotFound(w, r)
		return
	}

	actualRef, err := proj.Ref(ref)
	if err != nil {
		log.Print(err)
		http.NotFound(w, r)
		return
	}
	if actualRef != ref {
		u := r.URL
		u.Path = path.Join("/", project, actualRef, action, rest)
		http.Redirect(w, r, u.String(), http.StatusTemporaryRedirect)
		return
	}

	build, err := proj.GetBuild(ref)
	if err != nil {
		log.Print(err)
		http.NotFound(w, r)
		return
	}

	build.StartBuild()
	actionFunc(ci, build, rest, w, r)
}

func (ci *CI) Status(build *Build, route string, w http.ResponseWriter, r *http.Request) {
	if route != "" {
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(build.Summary())
}

func (ci *CI) Files(build *Build, route string, w http.ResponseWriter, r *http.Request) {
	if build.Status() != BuildFinished {
		http.NotFound(w, r)
		return
	}

	if route == "" {
		dir, err := os.Open(build.FilesPath)
		if err != nil {
			log.Print(err)
			http.NotFound(w, r)
			return
		}
		names, err := dir.Readdirnames(-1)
		if err != nil {
			log.Print(err)
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(names)
	} else {
		route = path.Clean(route)
		http.ServeFile(w, r, filepath.Join(build.FilesPath, route))
	}
}

func (ci *CI) Project(name string) (proj *Project, err error) {
	proj, ok := ci.proj[name]
	if !ok {
		proj, err = OpenProject(name, filepath.Join(ci.Path, name+".git"))
		if err != nil {
			return nil, err
		}
		ci.proj[name] = proj
	}
	return proj, nil
}

func main() {
	ci := NewCI("goci")
	log.Fatal(http.ListenAndServe(":8080", ci))
}
