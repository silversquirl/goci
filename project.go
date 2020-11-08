//go:generate stringer -linecomment -type BuildStatus
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
)

type Project struct {
	Name  string
	URL   string
	path  string
	build map[string]*Build
}

func OpenProject(name, path string) (proj *Project, err error) {
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	proj = &Project{
		Name: name, path: path,
		build: make(map[string]*Build),
	}

	if _, err = os.Stat(proj.path); err != nil {
		return nil, err
	}

	proj.URL, err = proj.exec("git", "remote", "get-url", "origin")
	if err != nil {
		return nil, err
	}

	return proj, nil
}

type GitError []byte

func (e GitError) Error() string {
	return "git error: " + string(e)
}

func (proj *Project) exec(name string, arg ...string) (string, error) {
	cmd := exec.Command(name, arg...)
	cmd.Dir = proj.path
	out, err := cmd.Output()
	switch err := err.(type) {
	case nil:
	case *exec.ExitError:
		return "", GitError(err.Stderr)
	default:
		return "", err
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (proj *Project) Fetch() error {
	_, err := proj.exec("git", "fetch")
	return err
}

func (proj *Project) Ref(ref string) (string, error) {
	// HACK: here we check the ref against a shortened sha1 hash of the same length to determine whether it's something that can change on the remote. There's almost definitely a better way of doing this
	actualRef, err := proj.exec("git", "rev-parse", fmt.Sprintf("--short=%d", len(ref)), ref)
	if err != nil || actualRef != ref {
		proj.Fetch()
	}

	actualRef, err = proj.exec("git", "rev-parse", "--short", ref)
	if err != nil {
		return "", err
	}
	return actualRef, nil
}

type Build struct {
	Proj *Project
	Ref  string
	Desc string
	path string

	CodePath, FilesPath string

	status   int32
	buildCmd *exec.Cmd
	buildLog string
}

func (proj *Project) GetBuild(ref string) (*Build, error) {
	build, ok := proj.build[ref]
	if !ok {
		path := filepath.Join(proj.path, "goci", ref)
		if err := os.MkdirAll(path, 0777); err != nil {
			return nil, err
		}

		desc, err := proj.exec("git", "show-branch", "--no-name", "--", ref)
		if err != nil {
			return nil, err
		}

		build = &Build{
			Proj: proj, Ref: ref, Desc: desc, path: path,
			CodePath:  filepath.Join(path, "code"),
			FilesPath: filepath.Join(path, "files"),
		}
		proj.build[ref] = build
	}
	return build, nil
}

func (build *Build) StartBuild() {
	if !atomic.CompareAndSwapInt32(&build.status, int32(BuildNotStarted), int32(BuildInProgress)) {
		return
	}

	go func() {
		var err error
		status := BuildFailed

		defer func() {
			if err != nil {
				build.buildLog = err.Error()
				status = BuildFailed
			}
			if status == BuildFailed {
				os.RemoveAll(build.FilesPath)
			}
			atomic.StoreInt32(&build.status, int32(status))
		}()

		codeErr := os.Mkdir(build.CodePath, 0777)
		filesErr := os.Mkdir(build.FilesPath, 0777)
		if os.IsExist(codeErr) {
			if os.IsExist(filesErr) {
				status = BuildFinished
			} else {
				status = BuildFailed
			}
			return
		} else if codeErr != nil {
			err = codeErr
			return
		} else if filesErr != nil {
			err = filesErr
			return
		}

		_, err = build.Proj.exec("git", "--work-tree", build.CodePath, "checkout", "--detach", build.Ref)
		if err != nil {
			return
		}
		_, err = build.Proj.exec("git", "--work-tree", build.CodePath, "reset", "--hard")
		if err != nil {
			return
		}

		build.buildCmd = exec.Command("go", "build", "-o", filepath.Join(build.FilesPath, build.Proj.Name))
		build.buildCmd.Dir = build.CodePath

		out, err := build.buildCmd.CombinedOutput()
		build.buildLog = string(out)
		switch e := err.(type) {
		case nil:
			status = BuildFinished
		case *exec.ExitError:
			build.buildLog += "\n" + e.Error()
			status = BuildFailed
			err = nil
		default:
			status = BuildFailed
		}
	}()
}

func (build *Build) Status() BuildStatus {
	return BuildStatus(atomic.LoadInt32(&build.status))
}

func (build *Build) Log() string {
	switch build.Status() {
	case BuildFinished, BuildFailed:
		return build.buildLog
	default:
		return ""
	}
}

func (build *Build) Summary() BuildSummary {
	return BuildSummary{
		build.Proj.Name,
		build.Proj.URL,
		build.Ref,
		build.Desc,
		build.Status(),
		build.Log(),
	}
}

type BuildStatus int32

const (
	BuildNotStarted BuildStatus = iota // Not started
	BuildInProgress                    // In progress
	BuildFinished                      // Finished
	BuildFailed                        // Failed
)

func (status BuildStatus) MarshalText() ([]byte, error) {
	return []byte(status.String()), nil
}

type BuildSummary struct {
	ProjName string      `json:"projectName"`
	ProjURL  string      `json:"projectURL"`
	CommitID string      `json:"commit"`
	Summary  string      `json:"commitSummary"`
	Status   BuildStatus `json:"status"`
	Log      string      `json:"buildLog,omitempty"`
}
