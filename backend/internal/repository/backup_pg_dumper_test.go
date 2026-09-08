//go:build unit

package repository

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/config"
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

func newTestPgDumper(t *testing.T, commandContext func(context.Context, string, ...string) *exec.Cmd) (*PgDumper, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return &PgDumper{
		cfg: &config.DatabaseConfig{
			Host:     "db.example.test",
			Port:     5432,
			User:     "sub2api",
			Password: "secret",
			DBName:   "sub2api",
			SSLMode:  "require",
		},
		db:             db,
		commandContext: commandContext,
	}, mock
}

func expectBackupMigrationLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnRows(sqlmock.NewRows([]string{"pg_try_advisory_lock"}).AddRow(true))
}

func expectBackupMigrationUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnResult(sqlmock.NewResult(0, 1))
}

// skipWithoutPOSIXShell keeps the injected-command tests portable: they drive
// pg_dump through a POSIX shell that is not guaranteed on Windows hosts.
func skipWithoutPOSIXShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("injected pg_dump command tests require a POSIX shell")
	}
}

func TestPgDumperHoldsMigrationLockThroughReaderClose(t *testing.T) {
	skipWithoutPOSIXShell(t)
	var mock sqlmock.Sqlmock
	commandCreated := false
	dumper, createdMock := newTestPgDumper(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
		commandCreated = true
		require.Equal(t, "pg_dump", name)
		require.Contains(t, args, "--clean")
		require.NoError(t, mock.ExpectationsWereMet(), "migration lock must be acquired before pg_dump is created")
		return exec.CommandContext(ctx, "sh", "-c", "printf backup-data")
	})
	mock = createdMock
	expectBackupMigrationLock(mock)

	reader, err := dumper.Dump(context.Background())
	require.NoError(t, err)
	require.True(t, commandCreated)
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "backup-data", string(data))

	expectBackupMigrationUnlock(mock)
	require.Error(t, mock.ExpectationsWereMet(), "migration lock was released before the reader closed")
	require.NoError(t, reader.Close())
	require.NoError(t, mock.ExpectationsWereMet())
	require.NoError(t, reader.Close(), "reader close must be idempotent")
}

func TestPgDumperReleasesMigrationLockWhenStdoutPipeSetupFails(t *testing.T) {
	skipWithoutPOSIXShell(t)
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "sh", "-c", "true")
		cmd.Stdout = io.Discard
		return cmd
	})
	expectBackupMigrationLock(mock)
	expectBackupMigrationUnlock(mock)

	reader, err := dumper.Dump(context.Background())
	require.Nil(t, reader)
	require.ErrorContains(t, err, "create stdout pipe")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperReleasesMigrationLockWhenProcessStartFails(t *testing.T) {
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/path/that/does/not/exist/pg_dump")
	})
	expectBackupMigrationLock(mock)
	expectBackupMigrationUnlock(mock)

	reader, err := dumper.Dump(context.Background())
	require.Nil(t, reader)
	require.ErrorContains(t, err, "start pg_dump")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperReleasesMigrationLockWhenProcessFails(t *testing.T) {
	skipWithoutPOSIXShell(t)
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf partial-backup; exit 7")
	})
	expectBackupMigrationLock(mock)

	reader, err := dumper.Dump(context.Background())
	require.NoError(t, err)
	data, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "partial-backup", string(data))
	expectBackupMigrationUnlock(mock)
	require.ErrorContains(t, reader.Close(), "pg_dump exited with error")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperReportsUnlockFailureAndDiscardsConnection(t *testing.T) {
	skipWithoutPOSIXShell(t)
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "printf backup-data")
	})
	expectBackupMigrationLock(mock)

	reader, err := dumper.Dump(context.Background())
	require.NoError(t, err)
	_, err = io.ReadAll(reader)
	require.NoError(t, err)
	mock.ExpectExec(regexp.QuoteMeta("SELECT pg_advisory_unlock($1)")).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnError(errors.New("unlock unavailable"))
	require.ErrorContains(t, reader.Close(), "release backup migration lock")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperDoesNotStartProcessWhenMigrationLockFails(t *testing.T) {
	commandCreated := false
	dumper, mock := newTestPgDumper(t, func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		commandCreated = true
		return exec.CommandContext(ctx, "sh", "-c", "true")
	})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT pg_try_advisory_lock($1)")).
		WithArgs(migrationsAdvisoryLockID).
		WillReturnError(errors.New("database unavailable"))

	reader, err := dumper.Dump(context.Background())
	require.Nil(t, reader)
	require.ErrorContains(t, err, "acquire backup migration lock")
	require.False(t, commandCreated)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPgDumperRejectsNilDatabase(t *testing.T) {
	dumper := &PgDumper{cfg: &config.DatabaseConfig{}}
	reader, err := dumper.Dump(context.Background())
	require.Nil(t, reader)
	require.ErrorContains(t, err, "nil sql db")
}
