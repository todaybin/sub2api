package repository

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// PgDumper implements service.DBDumper using pg_dump/psql
type PgDumper struct {
	cfg *config.DatabaseConfig
}

// NewPgDumper creates a new PgDumper
func NewPgDumper(cfg *config.Config) service.DBDumper {
	return &PgDumper{cfg: &cfg.Database}
}

// Dump executes pg_dump and returns a streaming reader of the output
func (d *PgDumper) Dump(ctx context.Context) (io.ReadCloser, error) {
	args := []string{
		"-h", d.cfg.Host,
		"-p", fmt.Sprintf("%d", d.cfg.Port),
		"-U", d.cfg.User,
		"-d", d.cfg.DBName,
		"--no-owner",
		"--no-acl",
		"--clean",
		"--if-exists",
	}

	cmd, err := newPostgresToolCommand(ctx, d.cfg.ClientBin, "pg_dump", args...)
	if err != nil {
		return nil, err
	}
	setPostgresCommandEnv(cmd, d.cfg.Password, d.cfg.SSLMode)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start pg_dump: %w", err)
	}

	// 返回一个 ReadCloser：读 stdout，关闭时等待进程退出
	return &cmdReadCloser{ReadCloser: stdout, cmd: cmd}, nil
}

// Restore executes psql to restore from a streaming reader
func (d *PgDumper) Restore(ctx context.Context, data io.Reader) error {
	args := []string{
		"-h", d.cfg.Host,
		"-p", fmt.Sprintf("%d", d.cfg.Port),
		"-U", d.cfg.User,
		"-d", d.cfg.DBName,
		"--single-transaction",
	}

	cmd, err := newPostgresToolCommand(ctx, d.cfg.ClientBin, "psql", args...)
	if err != nil {
		return err
	}
	setPostgresCommandEnv(cmd, d.cfg.Password, d.cfg.SSLMode)

	cmd.Stdin = data

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, string(output))
	}
	return nil
}

func newPostgresToolCommand(ctx context.Context, clientBin, name string, args ...string) (*exec.Cmd, error) {
	toolPath, nativeErr := resolvePostgresTool(name, clientBin)
	if nativeErr == nil {
		cmd := exec.CommandContext(ctx, toolPath, args...)
		setPostgresClientRuntimeEnv(cmd, toolPath)
		return cmd, nil
	}

	if runtime.GOOS != "windows" {
		if dockerPath, err := exec.LookPath("docker"); err == nil {
			if cmd := newDockerPostgresToolCommand(ctx, dockerPath, nil, name, args...); cmd != nil {
				return cmd, nil
			}
		}
		return nil, fmt.Errorf("%w; the tool was also not found in the configured PostgreSQL Docker container", nativeErr)
	}

	wslPath, err := exec.LookPath("wsl.exe")
	distro := strings.TrimSpace(os.Getenv("PG_WSL_DISTRO"))
	if err == nil {
		if wslHasPostgresTool(ctx, wslPath, distro, name) {
			wslArgs := buildWSLExecArgs(distro, name, args...)
			return exec.CommandContext(ctx, wslPath, wslArgs...), nil
		}
		wslDockerPrefix := buildWSLExecArgs(distro, "docker")
		if cmd := newDockerPostgresToolCommand(ctx, wslPath, wslDockerPrefix, name, args...); cmd != nil {
			return cmd, nil
		}
	}
	if dockerPath, dockerErr := exec.LookPath("docker.exe"); dockerErr == nil {
		if cmd := newDockerPostgresToolCommand(ctx, dockerPath, nil, name, args...); cmd != nil {
			return cmd, nil
		}
	}
	return nil, fmt.Errorf("%w; the tool was also not found in WSL or the configured PostgreSQL Docker container", nativeErr)
}

func wslHasPostgresTool(ctx context.Context, wslPath, distro, name string) bool {
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(checkCtx, wslPath, buildWSLExecArgs(distro, "sh", "-lc", "command -v "+name)...)
	return cmd.Run() == nil
}

func buildWSLExecArgs(distro, name string, args ...string) []string {
	wslArgs := make([]string, 0, len(args)+5)
	if distro != "" {
		wslArgs = append(wslArgs, "--distribution", distro)
	}
	wslArgs = append(wslArgs, "--exec", name)
	return append(wslArgs, args...)
}

func newDockerPostgresToolCommand(ctx context.Context, runtimePath string, runtimePrefix []string, name string, args ...string) *exec.Cmd {
	for _, container := range postgresDockerContainerCandidates() {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		checkArgs := appendCommandArgs(runtimePrefix, "exec", container, "sh", "-lc", "command -v "+name)
		checkErr := exec.CommandContext(checkCtx, runtimePath, checkArgs...).Run()
		cancel()
		if checkErr != nil {
			continue
		}
		dockerArgs := buildDockerExecArgs(container, name, args...)
		return exec.CommandContext(ctx, runtimePath, appendCommandArgs(runtimePrefix, dockerArgs...)...)
	}
	return nil
}

func postgresDockerContainerCandidates() []string {
	if container := strings.TrimSpace(os.Getenv("PG_DOCKER_CONTAINER")); container != "" {
		return []string{container}
	}
	return []string{"sub2api-postgres", "sub2api-postgres-dev"}
}

func buildDockerExecArgs(container, name string, args ...string) []string {
	dockerArgs := []string{"exec", "-i", "-e", "PGPASSWORD", "-e", "PGSSLMODE", container, name}
	return append(dockerArgs, args...)
}

func appendCommandArgs(prefix []string, args ...string) []string {
	result := make([]string, 0, len(prefix)+len(args))
	result = append(result, prefix...)
	return append(result, args...)
}

func setPostgresCommandEnv(cmd *exec.Cmd, password, sslMode string) {
	env := cmd.Environ()
	wslEnvNames := make([]string, 0, 2)
	if password != "" {
		env = append(env, "PGPASSWORD="+password)
		wslEnvNames = append(wslEnvNames, "PGPASSWORD/u")
	}
	if sslMode != "" {
		env = append(env, "PGSSLMODE="+sslMode)
		wslEnvNames = append(wslEnvNames, "PGSSLMODE/u")
	}
	if runtime.GOOS == "windows" && strings.EqualFold(filepath.Base(cmd.Path), "wsl.exe") && len(wslEnvNames) > 0 {
		existing := strings.TrimSpace(os.Getenv("WSLENV"))
		if existing != "" {
			wslEnvNames = append([]string{existing}, wslEnvNames...)
		}
		env = append(env, "WSLENV="+strings.Join(wslEnvNames, ":"))
	}
	cmd.Env = env
}

// resolvePostgresTool supports explicit, project-local, and system client tools.
func resolvePostgresTool(name, configuredBin string) (string, error) {
	pathEnv := strings.ToUpper(name) + "_PATH"
	if candidate := strings.TrimSpace(os.Getenv(pathEnv)); isRegularFile(candidate) {
		return candidate, nil
	}

	executableName := postgresExecutableName(name, runtime.GOOS)
	if candidate := findPostgresToolInLocation(configuredBin, executableName); candidate != "" {
		return candidate, nil
	}
	for _, envName := range []string{"PG_BIN", "POSTGRES_BIN"} {
		if candidate := findPostgresToolInLocation(os.Getenv(envName), executableName); candidate != "" {
			return candidate, nil
		}
	}
	if home := strings.TrimSpace(os.Getenv("POSTGRES_HOME")); home != "" {
		if candidate := filepath.Join(home, "bin", executableName); isRegularFile(candidate) {
			return candidate, nil
		}
	}
	for _, bin := range projectPostgresBinCandidates() {
		if candidate := filepath.Join(bin, executableName); isRegularFile(candidate) {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}

	var candidates []string
	if runtime.GOOS == "windows" {
		for _, root := range []string{
			os.Getenv("ProgramW6432"),
			os.Getenv("ProgramFiles"),
			os.Getenv("ProgramFiles(x86)"),
		} {
			if strings.TrimSpace(root) == "" {
				continue
			}
			matches, _ := filepath.Glob(filepath.Join(root, "PostgreSQL", "*", "bin", executableName))
			candidates = append(candidates, matches...)
		}
		// PostgreSQL zip distributions are commonly unpacked directly here.
		if root := strings.TrimSpace(os.Getenv("SystemDrive")); root != "" {
			matches, _ := filepath.Glob(filepath.Join(root+string(filepath.Separator), "PostgreSQL", "*", "bin", executableName))
			candidates = append(candidates, matches...)
		}
	} else {
		// Debian/Ubuntu, Homebrew, and source installs may keep the client
		// outside PATH while still using these conventional locations.
		for _, pattern := range []string{
			filepath.Join(string(filepath.Separator), "usr", "lib", "postgresql", "*", "bin", executableName),
			filepath.Join(string(filepath.Separator), "usr", "local", "pgsql", "bin", executableName),
			filepath.Join(string(filepath.Separator), "opt", "homebrew", "opt", "libpq", "bin", executableName),
			filepath.Join(string(filepath.Separator), "usr", "local", "opt", "libpq", "bin", executableName),
		} {
			matches, _ := filepath.Glob(pattern)
			candidates = append(candidates, matches...)
		}
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		return comparePostgresVersions(postgresVersionFromToolPath(candidates[i]), postgresVersionFromToolPath(candidates[j])) > 0
	})
	for _, candidate := range candidates {
		if isRegularFile(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%s executable not found; set database.client_bin, place a complete PostgreSQL client under tools/postgresql, or set %s/PG_BIN", name, pathEnv)
}

func postgresExecutableName(name, goos string) string {
	if goos == "windows" {
		return name + ".exe"
	}
	return name
}

func findPostgresToolInLocation(location, executableName string) string {
	location = strings.TrimSpace(location)
	if location == "" {
		return ""
	}
	for _, candidate := range []string{
		filepath.Join(location, executableName),
		filepath.Join(location, "bin", executableName),
		filepath.Join(location, runtime.GOOS+"-"+runtime.GOARCH, "bin", executableName),
	} {
		if isRegularFile(candidate) {
			return candidate
		}
	}
	return ""
}

func projectPostgresBinCandidates() []string {
	cwd, _ := os.Getwd()
	executable, _ := os.Executable()
	return buildProjectPostgresBinCandidates(cwd, executable, runtime.GOOS, runtime.GOARCH)
}

func buildProjectPostgresBinCandidates(cwd, executable, goos, goarch string) []string {
	roots := make([]string, 0, 4)
	if cwd != "" {
		roots = append(roots, cwd, filepath.Dir(cwd))
	}
	if executable != "" {
		executableDir := filepath.Dir(executable)
		roots = append(roots, executableDir, filepath.Dir(executableDir))
	}

	seen := make(map[string]struct{})
	result := make([]string, 0, len(roots)*2)
	for _, root := range roots {
		postgresRoot := filepath.Clean(filepath.Join(root, "tools", "postgresql"))
		for _, bin := range []string{
			filepath.Join(postgresRoot, goos+"-"+goarch, "bin"),
			filepath.Join(postgresRoot, "bin"),
		} {
			key := strings.ToLower(filepath.Clean(bin))
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, bin)
		}
	}
	return result
}

func setPostgresClientRuntimeEnv(cmd *exec.Cmd, toolPath string) {
	binDir := filepath.Dir(toolPath)
	prependCommandPathEnv(cmd, "PATH", binDir)

	libDir := filepath.Join(filepath.Dir(binDir), "lib")
	if !isDirectory(libDir) {
		return
	}
	switch runtime.GOOS {
	case "linux":
		prependCommandPathEnv(cmd, "LD_LIBRARY_PATH", libDir)
	case "darwin":
		prependCommandPathEnv(cmd, "DYLD_LIBRARY_PATH", libDir)
	}
}

func prependCommandPathEnv(cmd *exec.Cmd, name, value string) {
	existing := os.Getenv(name)
	if existing != "" {
		value += string(os.PathListSeparator) + existing
	}
	cmd.Env = append(cmd.Environ(), name+"="+value)
}

func isRegularFile(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func isDirectory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func postgresVersionFromToolPath(path string) string {
	return filepath.Base(filepath.Dir(filepath.Dir(path)))
}

func comparePostgresVersions(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	length := len(aParts)
	if len(bParts) > length {
		length = len(bParts)
	}
	for i := 0; i < length; i++ {
		var aPart, bPart int
		if i < len(aParts) {
			aPart, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bPart, _ = strconv.Atoi(bParts[i])
		}
		if aPart > bPart {
			return 1
		}
		if aPart < bPart {
			return -1
		}
	}
	return 0
}

// cmdReadCloser wraps a command stdout pipe and waits for the process on Close
type cmdReadCloser struct {
	io.ReadCloser
	cmd *exec.Cmd
}

func (c *cmdReadCloser) Close() error {
	// Close the pipe first
	_ = c.ReadCloser.Close()
	// Wait for the process to exit
	if err := c.cmd.Wait(); err != nil {
		return fmt.Errorf("pg_dump exited with error: %w", err)
	}
	return nil
}
