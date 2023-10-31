package gin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Builder interface {
	Build() error
	Binary() string
	Errors() string
}

type BuildEvent struct {
	ID              string    `json:"id"`
	Timestamp       time.Time `json:"timestamp"`
	Args            []string  `json:"args"`
	Platform        string    `json:"platform"`
	User            string    `json:"user"`
	ExitStatus      int       `json:"exit_status"`
	DurationSeconds float64   `json:"duration_seconds"`
}

type builder struct {
	dir                string
	binary             string
	logFile            *os.File
	buildEventEndpoint string
	platform           string
	errors             string
	useGodep           bool
	wd                 string
	buildArgs          []string
}

func NewBuilder(dir string, bin string, useGodep bool, wd string, buildArgs []string, buildEventEndpoint string) Builder {
	if len(bin) == 0 {
		bin = "bin"
	}

	// does not work on Windows without the ".exe" extension
	if runtime.GOOS == "windows" {
		if !strings.HasSuffix(bin, ".exe") { // check if it already has the .exe extension
			bin += ".exe"
		}
	}

	logFilePath := os.Getenv("GIN_BUILD_LOG")
	if logFilePath == "" {
		logFilePath = path.Join(os.Getenv("HOME"), ".gin-build.log")
	}

	logFile, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(fmt.Errorf("failed to open log file: %w", err))
	}

	platformBytes, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
	if err != nil {
		platformBytes = []byte("unknown")
	}

	return &builder{
		dir:                dir,
		binary:             bin,
		logFile:            logFile,
		buildEventEndpoint: buildEventEndpoint,
		platform:           string(platformBytes),
		useGodep:           useGodep,
		wd:                 wd,
		buildArgs:          buildArgs,
	}
}

func (b *builder) Binary() string {
	return b.binary
}

func (b *builder) Errors() string {
	return b.errors
}

func (b *builder) Build() error {
	args := append([]string{"go", "build", "-o", filepath.Join(b.wd, b.binary)}, b.buildArgs...)

	var command *exec.Cmd
	if b.useGodep {
		args = append([]string{"godep"}, args...)
	}
	command = exec.Command(args[0], args[1:]...)

	command.Dir = b.dir

	startAt := time.Now()
	output, err := command.CombinedOutput()
	var (
		duration   = time.Since(startAt)
		exitStatus = 0
	)
	if command.ProcessState.Success() {
		b.errors = ""
	} else {
		b.errors = string(output)
		exitStatus = command.ProcessState.ExitCode()
	}

	ev := BuildEvent{
		ID:              uuid.NewString(),
		Timestamp:       time.Now(),
		Args:            args,
		Platform:        b.platform,
		User:            os.Getenv("USER"),
		ExitStatus:      exitStatus,
		DurationSeconds: duration.Seconds(),
	}

	{
		err = json.NewEncoder(b.logFile).Encode(ev)
		if err != nil {
			fmt.Println(fmt.Errorf("failed to log event: %w", err))
		} else {
			b.logFile.Sync()

			go func() {
				data, err := json.Marshal(map[string]any{"payload": ev})
				if err != nil {
					fmt.Println(fmt.Errorf("failed to marshal event: %w", err))
				}

				req, err := http.NewRequest("POST", b.buildEventEndpoint, bytes.NewReader(data))
				if err != nil {
					fmt.Println(fmt.Errorf("failed to log event: %w", err))
					return
				}

				req.Header.Set("Content-Type", "application/json")

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					fmt.Println(fmt.Errorf("failed to send build event: %w", err))
				} else {
					fmt.Printf("build event sent: %v\n", resp.Status)
				}
			}()
		}
	}

	if len(b.errors) > 0 {
		return fmt.Errorf(b.errors)
	}

	return err
}
