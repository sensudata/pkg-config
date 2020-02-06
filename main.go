package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/influxdata/pkg-config/libs/flux"
	"github.com/spf13/pflag"
	"go.uber.org/zap"
)

const pkgConfigExecName = "pkg-config"

// Library is the interface for building and installing a library
// for use by package config.
type Library interface {
	// Install will be used to build and install the library into
	// the directory.
	Install(ctx context.Context, l *zap.Logger) error

	// WritePackageConfig will write out the package configuration
	// for this library to the given writer.
	WritePackageConfig(w io.Writer) error
}

func getArg0Path() string {
	arg0 := os.Args[0]
	if strings.Contains(arg0, "/") {
		return arg0
	}
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, arg0)
}

func lookPath(arg0path string) (string, error) {
	path := os.Getenv("PATH")
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			// Unix shell semantics: path element "" means "."
			dir = "."
		}
		dir, _ = filepath.Abs(dir)
		path := filepath.Join(dir, pkgConfigExecName)
		if arg0path == path {
			// Exclude this executable from the search path.
			continue
		} else if _, err := exec.LookPath(path); err == nil {
			return path, nil
		}
	}
	return "", errors.New("pkg-config executable not found on PATH")
}

var logger *zap.Logger

func configureLogger(logger **zap.Logger) error {
	logPath := os.Getenv("PKG_CONFIG_LOG")
	if logPath == "" {
		*logger = zap.NewNop()
		return nil
	}
	config := zap.NewProductionConfig()
	config.OutputPaths = []string{logPath}
	l, err := config.Build()
	if err != nil {
		return err
	}
	*logger = l
	return nil
}

type Flags struct {
	Cflags bool
	Libs   bool
}

func parseFlags(name string, args []string) ([]string, Flags, error) {
	var flags Flags
	flagSet := pflag.NewFlagSet(name, pflag.ContinueOnError)
	flagSet.BoolVar(&flags.Cflags, "cflags", false, "output all pre-processor and compiler flags")
	flagSet.BoolVar(&flags.Libs, "libs", false, "output all linker flags")
	if err := flagSet.Parse(args); err != nil {
		return nil, flags, err
	}
	return flagSet.Args(), flags, nil
}

func runPkgConfig(execCmd, pkgConfigPath string, libs []string, flags Flags) error {
	args := make([]string, 0, len(libs)+3)
	if flags.Cflags {
		args = append(args, "--cflags")
	}
	if flags.Libs {
		args = append(args, "--libs")
	}
	args = append(args, "--")
	args = append(args, libs...)

	pathEnv := os.Getenv("PKG_CONFIG_PATH")
	if pathEnv != "" {
		pathEnv = fmt.Sprintf("%s%c%s", pkgConfigPath, os.PathListSeparator, pathEnv)
	} else {
		pathEnv = pkgConfigPath
	}

	cmd := exec.Command(execCmd, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), fmt.Sprintf("PKG_CONFIG_PATH=%s", pathEnv))
	return cmd.Run()
}

func getLibraryFor(ctx context.Context, name string) (Library, bool, error) {
	switch name {
	case "flux":
		l, err := flux.Configure(ctx, logger)
		if err != nil {
			return nil, true, err
		}
		return l, true, nil
	}
	return nil, false, nil
}

func realMain() int {
	if err := configureLogger(&logger); err != nil {
		panic(err)
	}

	ctx := context.TODO()
	arg0path := getArg0Path()
	logger.Info("Started pkg-config", zap.String("arg0", arg0path), zap.Strings("args", os.Args[1:]))
	pkgConfigExec, err := lookPath(getArg0Path())
	if err != nil {
		logger.Error("Could not find pkg-config executable", zap.Error(err))
		return 1
	}
	logger.Info("Found pkg-config execute", zap.String("path", pkgConfigExec))

	libs, flags, err := parseFlags(os.Args[0], os.Args[1:])
	if err != nil {
		logger.Error("Failed to parse command-line flags", zap.Error(err))
		return 1
	}

	// Construct a temporary path where we will place all of the generated
	// pkgconfig files.
	pkgConfigPath, err := ioutil.TempDir("", "pkgconfig")
	if err != nil {
		logger.Error("Unable to create temporary directory for pkgconfig files", zap.Error(err))
		return 1
	}
	defer func() { _ = os.RemoveAll(pkgConfigPath) }()

	// Construct the packages and write pkgconfig files to point to those packages.
	for _, lib := range libs {
		if l, ok, err := getLibraryFor(ctx, lib); err != nil {
			logger.Error("Error configuring library", zap.String("name", lib), zap.Error(err))
			return 1
		} else if ok {
			if err := l.Install(ctx, logger); err != nil {
				logger.Error("Error installing library", zap.String("name", lib), zap.Error(err))
				return 1
			}

			pkgfile := filepath.Join(pkgConfigPath, lib+".pc")
			f, err := os.Create(pkgfile)
			if err != nil {
				logger.Error("Could not create pkg-config configuration file", zap.String("path", pkgfile), zap.Error(err))
				return 1
			}

			if err := l.WritePackageConfig(f); err != nil {
				logger.Error("Error writing pkg-config configuration file", zap.String("path", pkgfile), zap.Error(err))
				return 1
			}
		}
	}

	// Run pkgconfig for the given libraries and flags.
	if err := runPkgConfig(pkgConfigExec, pkgConfigPath, libs, flags); err != nil {
		logger.Error("Running pkg-config failed", zap.Error(err))
		return 1
	}
	return 0
}

func main() {
	os.Exit(realMain())
}