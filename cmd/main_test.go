package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/stretchr/testify/require"
)

const (
	nvidiaRuntime      = "nvidia-container-runtime"
	nvidiaHook         = "nvidia-container-runtime-hook"
	bundlePath         = "../test/output/bundle/"
	specFile           = "config.json"
	unmodifiedSpecFile = "../test/input/test_spec.json"
)

func TestMain(m *testing.M) {
	// TEST SETUP
	// Update PATH to execute mock runc in current directory
	_, filename, _, _ := runtime.Caller(0)
	workingDir := path.Dir(filename)
	parentDir := path.Dir(workingDir)

	paths := strings.Split(os.Getenv("PATH"), ":")
	paths = append([]string{parentDir, workingDir}, paths...)
	os.Setenv("PATH", strings.Join(paths, ":"))

	// Confirm path setup correctly
	runcPath, err := exec.LookPath("runc")
	if err != nil || !strings.HasPrefix(runcPath, parentDir) {
		log.Fatal("error in test setup: mock runc path set incorrectly in TestMain()")
	}

	// RUN TESTS
	exitCode := m.Run()

	// TEST CLEANUP
	os.Remove(specFile)

	os.Exit(exitCode)
}

// case 1) nvidia-container-runtime run --bundle
// case 2) nvidia-container-runtime create --bundle
//		- Confirm the runtime handles bad input correctly
func TestBadInput(t *testing.T) {
	err := generateNewRuntimeSpec()
	if err != nil {
		t.Fatal(err)
	}

	cmdRun := exec.Command(nvidiaRuntime, "run", "--bundle")
	t.Logf("executing: %s\n", strings.Join(cmdRun.Args, " "))
	err = cmdRun.Run()
	require.Error(t, err, "runtime should return an error")

	cmdCreate := exec.Command(nvidiaRuntime, "create", "--bundle")
	t.Logf("executing: %s\n", strings.Join(cmdCreate.Args, " "))
	err = cmdCreate.Run()
	require.Error(t, err, "runtime should return an error")
}

// case 1) nvidia-container-runtime run --bundle <bundle-name> <ctr-name>
//		- Confirm the runtime runs with no errors
// case 2) nvidia-container-runtime create --bundle <bundle-name> <ctr-name>
//		- Confirm the runtime inserts the NVIDIA prestart hook correctly
func TestGoodInput(t *testing.T) {
	err := generateNewRuntimeSpec()
	if err != nil {
		t.Fatal(err)
	}

	cmdRun := exec.Command(nvidiaRuntime, "run", "--bundle", bundlePath, "testcontainer")
	t.Logf("executing: %s\n", strings.Join(cmdRun.Args, " "))
	err = cmdRun.Run()
	require.NoError(t, err, "runtime should not return an error")

	// Check config.json and confirm there are no hooks
	spec, err := getRuntimeSpec(filepath.Join(bundlePath, specFile))
	require.NoError(t, err, "should be no errors when reading and parsing spec from config.json")
	require.Empty(t, spec.Hooks, "there should be no hooks in config.json")

	cmdCreate := exec.Command(nvidiaRuntime, "create", "--bundle", bundlePath, "testcontainer")
	t.Logf("executing: %s\n", strings.Join(cmdCreate.Args, " "))
	err = cmdCreate.Run()
	require.NoError(t, err, "runtime should not return an error")

	// Check config.json for NVIDIA prestart hook
	spec, err = getRuntimeSpec(filepath.Join(bundlePath, specFile))
	require.NoError(t, err, "should be no errors when reading and parsing spec from config.json")
	require.NotEmpty(t, spec.Hooks, "there should be hooks in config.json")
	require.Equal(t, 1, nvidiaHookCount(spec.Hooks), "exactly one nvidia prestart hook should be inserted correctly into config.json")
}

// NVIDIA prestart hook already present in config file
func TestDuplicateHook(t *testing.T) {
	err := generateNewRuntimeSpec()
	if err != nil {
		t.Fatal(err)
	}

	var spec specs.Spec
	spec, err = getRuntimeSpec(filepath.Join(bundlePath, specFile))
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("inserting nvidia prestart hook to config.json")
	if err = addNVIDIAHook(&spec); err != nil {
		t.Fatal(err)
	}

	jsonOutput, err := json.MarshalIndent(spec, "", "\t")
	if err != nil {
		t.Fatal(err)
	}

	jsonFile, err := os.OpenFile(bundlePath+specFile, os.O_RDWR, 0644)
	if err != nil {
		t.Fatal(err)
	}
	_, err = jsonFile.WriteAt(jsonOutput, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Test how runtime handles already existing prestart hook in config.json
	cmdCreate := exec.Command(nvidiaRuntime, "create", "--bundle", bundlePath, "testcontainer")
	t.Logf("executing: %s\n", strings.Join(cmdCreate.Args, " "))
	err = cmdCreate.Run()
	require.NoError(t, err, "runtime should not return an error")

	// Check config.json for NVIDIA prestart hook
	spec, err = getRuntimeSpec(filepath.Join(bundlePath, specFile))
	require.NoError(t, err, "should be no errors when reading and parsing spec from config.json")
	require.NotEmpty(t, spec.Hooks, "there should be hooks in config.json")
	require.Equal(t, 1, nvidiaHookCount(spec.Hooks), "exactly one nvidia prestart hook should be inserted correctly into config.json")
}

func getRuntimeSpec(filePath string) (specs.Spec, error) {
	var spec specs.Spec
	jsonFile, err := os.OpenFile(filePath, os.O_RDWR, 0644)
	if err != nil {
		return spec, err
	}
	defer jsonFile.Close()

	jsonContent, err := ioutil.ReadAll(jsonFile)
	if err != nil {
		return spec, err
	} else if json.Valid(jsonContent) {
		err = json.Unmarshal(jsonContent, &spec)
		if err != nil {
			return spec, err
		}
	} else {
		err = json.NewDecoder(bytes.NewReader(jsonContent)).Decode(&spec)
		if err != nil {
			return spec, err
		}
	}

	return spec, err
}

func generateNewRuntimeSpec() error {
	var err error

	err = os.MkdirAll(bundlePath, 0755)
	if err != nil {
		return err
	}

	cmd := exec.Command("cp", unmodifiedSpecFile, filepath.Join(bundlePath, specFile))
	err = cmd.Run()
	if err != nil {
		return err
	}
	return nil
}

// Return number of valid NVIDIA prestart hooks in runtime spec
func nvidiaHookCount(hooks *specs.Hooks) int {
	prestartHooks := hooks.Prestart
	count := 0

	for _, hook := range prestartHooks {
		if strings.Contains(hook.Path, nvidiaHook) {
			count++
		}
	}
	return count
}

func TestGetConfigWithCustomConfig(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	// By default debug is disabled
	contents := []byte("[nvidia-container-runtime]\ndebug = \"/nvidia-container-toolkit.log\"")
	testDir := path.Join(wd, "test")
	filename := path.Join(testDir, configFilePath)

	os.Setenv(configOverride, testDir)

	require.NoError(t, os.MkdirAll(filepath.Dir(filename), 0766))
	require.NoError(t, ioutil.WriteFile(filename, contents, 0766))

	defer func() { require.NoError(t, os.RemoveAll(testDir)) }()

	cfg, err := getConfig()
	require.NoError(t, err)
	require.Equal(t, cfg.debugFilePath, "/nvidia-container-toolkit.log")
}

func TestArgsGetConfigFilePath(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)

	testCases := []struct {
		args       args
		configPath string
	}{
		{
			args:       args{},
			configPath: fmt.Sprintf("%v/config.json", wd),
		},
		{
			args:       args{bundleDirPath: "/foo/bar"},
			configPath: "/foo/bar/config.json",
		},
		{
			args:       args{bundleDirPath: "/foo/bar/"},
			configPath: "/foo/bar/config.json",
		},
	}

	for i, tc := range testCases {
		cp, err := tc.args.getConfigFilePath()

		require.NoErrorf(t, err, "%d: %v", i, tc)
		require.Equalf(t, tc.configPath, cp, "%d: %v", i, tc)
	}
}

func TestGetArgs(t *testing.T) {
	testCases := []struct {
		argv     []string
		expected *args
		isError  bool
	}{
		{
			argv:     []string{},
			expected: &args{},
		},
		{
			argv: []string{"create"},
			expected: &args{
				cmd: "create",
			},
		},
		{
			argv:     []string{"--bundle"},
			expected: nil,
			isError:  true,
		},
		{
			argv:     []string{"-b"},
			expected: nil,
			isError:  true,
		},
		{
			argv:     []string{"--bundle", "/foo/bar"},
			expected: &args{bundleDirPath: "/foo/bar"},
		},
		{
			argv:     []string{"-bundle", "/foo/bar"},
			expected: &args{bundleDirPath: "/foo/bar"},
		},
		{
			argv:     []string{"--bundle=/foo/bar"},
			expected: &args{bundleDirPath: "/foo/bar"},
		},
		{
			argv:     []string{"-b=/foo/bar"},
			expected: &args{bundleDirPath: "/foo/bar"},
		},
		{
			argv:     []string{"-b=/foo/=bar"},
			expected: &args{bundleDirPath: "/foo/=bar"},
		},
		{
			argv:     []string{"-b", "/foo/bar"},
			expected: &args{bundleDirPath: "/foo/bar"},
		},
		{
			argv: []string{"create", "-b", "/foo/bar"},
			expected: &args{
				cmd:           "create",
				bundleDirPath: "/foo/bar",
			},
		},
		{
			argv: []string{"-b", "create", "create"},
			expected: &args{
				cmd:           "create",
				bundleDirPath: "create",
			},
		},
		{
			argv: []string{"-b=create", "create"},
			expected: &args{
				cmd:           "create",
				bundleDirPath: "create",
			},
		},
		{
			argv: []string{"-b", "create"},
			expected: &args{
				bundleDirPath: "create",
			},
		},
	}

	for i, tc := range testCases {
		args, err := getArgs(tc.argv)

		if tc.isError {
			require.Errorf(t, err, "%d: %v", i, tc)
		} else {
			require.NoErrorf(t, err, "%d: %v", i, tc)
		}
		require.EqualValuesf(t, tc.expected, args, "%d: %v", i, tc)
	}
}
