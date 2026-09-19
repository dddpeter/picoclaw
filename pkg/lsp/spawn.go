package lsp

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// spawnedServer bundles everything the Client needs from a launched server
// process.
type spawnedServer struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.Reader
	stderr    io.Reader
	terminate func() // kills the whole process tree
}

// spawnServer launches the language-server process with stdio pipes.
// terminate kills the whole process tree (cmd.exe shims and npx wrappers
// spawn children; a single Kill would orphan them).
func spawnServer(cfg ServerConfig, root string) (*spawnedServer, error) {
	args := append([]string{}, cfg.Args...)
	command := cfg.Command

	// Windows .bat/.cmd shims (npm global installs) must run through
	// cmd.exe; Go cannot exec them directly.
	if wrapNeeded(command) {
		args = append([]string{"/d", "/s", "/c", command}, args...)
		command = comSpec()
	}

	cmd := exec.Command(command, args...)
	cmd.Dir = root
	cmd.Env = mergedEnv(cfg.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: stdin pipe: %w", cfg.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: stdout pipe: %w", cfg.Name, err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("%s: stderr pipe: %w", cfg.Name, err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: failed to start %s: %w", cfg.Name, cfg.Command, err)
	}

	terminate := trackForTermination(cmd)

	return &spawnedServer{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    stdout,
		stderr:    stderr,
		terminate: terminate,
	}, nil
}

func mergedEnv(overrides map[string]string) []string {
	if len(overrides) == 0 {
		return nil
	}
	over := make(map[string]string, len(overrides))
	for k, v := range overrides {
		over[k] = v
	}
	env := os.Environ()
	out := make([]string, 0, len(env)+len(over))
	for _, kv := range env {
		name := kv
		if i := indexByte(kv, '='); i >= 0 {
			name = kv[:i]
		}
		if v, ok := over[name]; ok {
			out = append(out, name+"="+v)
			delete(over, name)
		} else {
			out = append(out, kv)
		}
	}
	for k, v := range over {
		out = append(out, k+"="+v)
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
