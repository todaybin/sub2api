//go:build unit

package repository

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComparePostgresVersions(t *testing.T) {
	require.Equal(t, 1, comparePostgresVersions("17", "16"))
	require.Equal(t, -1, comparePostgresVersions("15.2", "15.10"))
	require.Equal(t, 0, comparePostgresVersions("14", "14.0"))
}

func TestPostgresVersionFromToolPath(t *testing.T) {
	path := filepath.Join("C:\\Program Files", "PostgreSQL", "17", "bin", "pg_dump.exe")
	require.Equal(t, "17", postgresVersionFromToolPath(path))
}

func TestNewPostgresToolCommandUsesExplicitPath(t *testing.T) {
	toolPath := filepath.Join(t.TempDir(), "pg_dump")
	require.NoError(t, os.WriteFile(toolPath, []byte("test"), 0o700))
	t.Setenv("PATH", "")
	t.Setenv("PG_DUMP_PATH", toolPath)

	cmd, err := newPostgresToolCommand(context.Background(), "", "pg_dump", "--version")
	require.NoError(t, err)
	require.Equal(t, toolPath, cmd.Path)
}

func TestResolvePostgresToolUsesConfiguredClientBin(t *testing.T) {
	binDir := filepath.Join(t.TempDir(), "postgresql", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	toolPath := filepath.Join(binDir, postgresExecutableName("pg_dump", runtime.GOOS))
	require.NoError(t, os.WriteFile(toolPath, []byte("test"), 0o700))

	resolved, err := resolvePostgresTool("pg_dump", filepath.Dir(binDir))
	require.NoError(t, err)
	require.Equal(t, toolPath, resolved)
}

func TestBuildProjectPostgresBinCandidates(t *testing.T) {
	cwd := filepath.Join("workspace", "backend")
	executable := filepath.Join("workspace", "backend", "bin", "server")
	candidates := buildProjectPostgresBinCandidates(cwd, executable, "windows", "amd64")

	require.Contains(t, candidates, filepath.Join(cwd, "tools", "postgresql", "windows-amd64", "bin"))
	require.Contains(t, candidates, filepath.Join("workspace", "tools", "postgresql", "bin"))
	require.Contains(t, candidates, filepath.Join("workspace", "backend", "bin", "tools", "postgresql", "windows-amd64", "bin"))
}

func TestSetPostgresCommandEnv(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	cmd := exec.Command(executable)

	setPostgresCommandEnv(cmd, "secret", "require")
	require.Contains(t, cmd.Env, "PGPASSWORD=secret")
	require.Contains(t, cmd.Env, "PGSSLMODE=require")
}

func TestBuildWSLExecArgs(t *testing.T) {
	require.Equal(t,
		[]string{"--exec", "pg_dump", "--version"},
		buildWSLExecArgs("", "pg_dump", "--version"),
	)
	require.Equal(t,
		[]string{"--distribution", "Ubuntu-24.04", "--exec", "psql", "--version"},
		buildWSLExecArgs("Ubuntu-24.04", "psql", "--version"),
	)
}

func TestBuildDockerExecArgs(t *testing.T) {
	require.Equal(t,
		[]string{"exec", "-i", "-e", "PGPASSWORD", "-e", "PGSSLMODE", "sub2api-postgres", "pg_dump", "--version"},
		buildDockerExecArgs("sub2api-postgres", "pg_dump", "--version"),
	)
	require.Equal(t,
		[]string{"--distribution", "Ubuntu", "--exec", "docker", "exec", "sub2api-postgres", "sh"},
		appendCommandArgs(buildWSLExecArgs("Ubuntu", "docker"), "exec", "sub2api-postgres", "sh"),
	)
}
