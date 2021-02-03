//go:generate stringer -linecomment -type BuildStatus
package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
)

type Project struct {
	Name   string
	URL    string
	path   string
	build  map[string]*Build
	buildM sync.Mutex
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

func (proj *Project) Ref(ref string) (actualRef string, hash bool, err error) {
	actualRef, err = proj.exec("git", "rev-parse", fmt.Sprintf("--short=%d", len(ref)), ref)
	if err != nil || actualRef != ref {
		proj.Fetch()
	} else {
		hash = true
	}

	actualRef, err = proj.exec("git", "rev-parse", "--short", ref)
	if err != nil {
		return "", false, err
	}
	return actualRef, hash, nil
}

type Build struct {
	Proj *Project
	Ref  string
	Desc string
	path string

	CodePath, FilesPath string

	status   int32
	buildLog string
}

func (proj *Project) GetBuild(ref string) (*Build, error) {
	proj.buildM.Lock()
	defer proj.buildM.Unlock()
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

		targetStr, err := build.Proj.exec("git", "--work-tree", build.CodePath, "config", "goci.targets")
		if err != nil {
			return
		}

		var targets []Target
		if targetStr == "" {
			targets = []Target{{}}
		} else {
			for _, targetStr := range strings.Split(targetStr, " ") {
				m := targetRe.FindStringSubmatch(targetStr)
				if m == nil {
					err = fmt.Errorf("Invalid target %q", targetStr)
					return
				}

				target := Target{
					OS:   m[1],
					Arch: m[2],
				}

				for _, tag := range strings.Split(m[3], ",") {
					if tag == "cgo" {
						target.UseCgo = true
					} else {
						target.Tags = append(target.Tags, tag)
					}
				}

				targets = append(targets, target)
			}
		}

		buildLog := bytes.Buffer{}
	build:
		for _, target := range targets {
			outfn := build.Proj.Name
			if target.OS != "" {
				outfn += "-" + target.OS
			}
			if target.Arch != "" {
				outfn += "-" + target.Arch
			}
			if len(target.Tags) > 0 {
				outfn += "-" + strings.Join(target.Tags, "-")
			}
			if target.OS == "windows" {
				outfn += ".exe"
			}

			cmd := exec.Command("go", "build", "-o", filepath.Join(build.FilesPath, outfn), "-tags", strings.Join(target.Tags, ","))
			cmd.Dir = build.CodePath
			cmd.Env = os.Environ()
			cmd.Stdout = &buildLog
			cmd.Stderr = &buildLog

			if target.OS != "" {
				cmd.Env = append(cmd.Env, "GOOS="+target.OS)
			}
			if target.Arch != "" {
				cmd.Env = append(cmd.Env, "GOARCH="+target.Arch)
			}
			if target.UseCgo {
				cmd.Env = append(cmd.Env, "CGO_ENABLED=1")

				arch := ""
				switch target.Arch {
				case "":
				case "amd64":
					arch = "x86_64"
				case "386":
					arch = "x86"
				// TODO: more
				default:
					err = fmt.Errorf("Unknown architecture %q", target.Arch)
				}

				os := ""
				switch target.OS {
				case "":
				case "linux":
					// FIXME: do better than just guessing here - libc could be non-gnu, kernel could be branded
					os = "unknown-linux-gnu"
				case "windows":
					os = "w64-mingw32"
				// TODO: more
				default:
					err = fmt.Errorf("Unknown OS %q", target.OS)
				}

				if arch != "" && os != "" {
					cmd.Env = append(cmd.Env, fmt.Sprintf("CC=%s-%s-gcc", arch, os))
				}
			} else {
				cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
			}

			buildLog.WriteString(cmd.String())
			buildLog.WriteByte('\n')

			err := cmd.Run()
			switch e := err.(type) {
			case nil:
				status = BuildFinished
			case *exec.ExitError:
				buildLog.WriteByte('\n')
				buildLog.WriteString(e.Error())
				status = BuildFailed
				err = nil
				break build
			default:
				status = BuildFailed
				break build
			}
		}

		build.buildLog = buildLog.String()
	}()
}

var targetRe = regexp.MustCompile(`(\w+):(\w+)(?:\((\w+(?:,\w+)*)\))?`)

type Target struct {
	OS, Arch string
	UseCgo   bool
	Tags     []string
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
