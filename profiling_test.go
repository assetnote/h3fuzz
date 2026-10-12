package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type recordingProfileRuntime struct {
	events        []string
	mutexRate     int
	blockRate     int
	startCPUError error
	writeErrors   map[string]error
}

func (r *recordingProfileRuntime) StartCPUProfile(w io.Writer) error {
	r.events = append(r.events, "start-cpu")
	if r.startCPUError != nil {
		return r.startCPUError
	}
	_, _ = io.WriteString(w, "cpu-profile")
	return nil
}

func (r *recordingProfileRuntime) StopCPUProfile() {
	r.events = append(r.events, "stop-cpu")
}

func (r *recordingProfileRuntime) WriteHeapProfile(w io.Writer) error {
	r.events = append(r.events, "write-heap")
	if err := r.writeErrors["heap"]; err != nil {
		return err
	}
	_, _ = io.WriteString(w, "heap-profile")
	return nil
}

func (r *recordingProfileRuntime) WriteProfile(name string, w io.Writer) error {
	r.events = append(r.events, "write-"+name)
	if err := r.writeErrors[name]; err != nil {
		return err
	}
	_, _ = io.WriteString(w, name+"-profile")
	return nil
}

func (r *recordingProfileRuntime) SetMutexProfileFraction(rate int) int {
	previous := r.mutexRate
	r.mutexRate = rate
	r.events = append(r.events, "mutex-rate")
	return previous
}

func (r *recordingProfileRuntime) SetBlockProfileRate(rate int) int {
	previous := r.blockRate
	r.blockRate = rate
	r.events = append(r.events, "block-rate")
	return previous
}

func TestProfilingLifecycleCreatesAndClosesCPUProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpu.pprof")
	runtime := &recordingProfileRuntime{}
	session, err := startProfilingWithRuntime(ffuf.OutputOptions{ProfileCPU: path}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("profile permissions = %04o, want 0600", got)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runtime.events, []string{"start-cpu", "stop-cpu"}) {
		t.Fatalf("profile lifecycle events = %v", runtime.events)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "cpu-profile" {
		t.Fatalf("CPU profile contents = %q", data)
	}
}

func TestProfilingLifecycleWritesSnapshotsAndRestoresRuntimeRates(t *testing.T) {
	dir := t.TempDir()
	paths := map[string]string{
		"heap": filepath.Join(dir, "heap.pprof"), "mutex": filepath.Join(dir, "mutex.pprof"),
		"block": filepath.Join(dir, "block.pprof"),
	}
	runtime := &recordingProfileRuntime{mutexRate: 17, blockRate: 23}
	session, err := startProfilingWithRuntime(ffuf.OutputOptions{
		ProfileHeap: paths["heap"], ProfileMutex: paths["mutex"], ProfileBlock: paths["block"],
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.mutexRate != 1 || runtime.blockRate != 1 {
		t.Fatalf("active profile rates = mutex %d, block %d; want 1, 1", runtime.mutexRate, runtime.blockRate)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if runtime.mutexRate != 17 || runtime.blockRate != 23 {
		t.Fatalf("restored profile rates = mutex %d, block %d; want 17, 23", runtime.mutexRate, runtime.blockRate)
	}
	for name, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(data), name+"-profile"; got != want {
			t.Errorf("%s profile contents = %q, want %q", name, got, want)
		}
	}
}

func TestProfilingLifecycleRejectsCollidingOutputNamesBeforeCreatingFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared.pprof")
	runtime := &recordingProfileRuntime{}
	_, err := startProfilingWithRuntime(ffuf.OutputOptions{
		ProfileCPU: path, ProfileHeap: filepath.Join(filepath.Dir(path), ".", filepath.Base(path)),
	}, runtime)
	if err == nil || !strings.Contains(err.Error(), "same output") {
		t.Fatalf("start error = %v, want colliding-output rejection", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("colliding profile path was created: %v", statErr)
	}
	if len(runtime.events) != 0 {
		t.Fatalf("runtime profiling changed before validation: %v", runtime.events)
	}
}

func TestProfilingLifecycleRejectsBlankOutputPath(t *testing.T) {
	runtime := &recordingProfileRuntime{}
	_, err := startProfilingWithRuntime(ffuf.OutputOptions{ProfileBlock: " \t "}, runtime)
	if err == nil || !strings.Contains(err.Error(), "must not be blank") {
		t.Fatalf("start error = %v, want blank-path rejection", err)
	}
	if len(runtime.events) != 0 {
		t.Fatalf("blank path changed runtime profiling: %v", runtime.events)
	}
}

func TestProfilingDisabledDoesNotTouchRuntime(t *testing.T) {
	runtime := &recordingProfileRuntime{mutexRate: 7, blockRate: 11}
	session, err := startProfilingWithRuntime(ffuf.OutputOptions{}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if len(runtime.events) != 0 || runtime.mutexRate != 7 || runtime.blockRate != 11 {
		t.Fatalf("disabled profiling changed runtime state: events=%v mutex=%d block=%d",
			runtime.events, runtime.mutexRate, runtime.blockRate)
	}
	if profilingRequested(ffuf.OutputOptions{}) {
		t.Fatal("zero-value profiling options unexpectedly enable profiling")
	}
}

func TestProfilingStartupFailureRollsBackOnlyNewArtifacts(t *testing.T) {
	dir := t.TempDir()
	cpuPath := filepath.Join(dir, "cpu.pprof")
	heapPath := filepath.Join(dir, "heap.pprof")
	if err := os.WriteFile(heapPath, []byte("keep-me"), 0o640); err != nil {
		t.Fatal(err)
	}
	runtime := &recordingProfileRuntime{}
	_, err := startProfilingWithRuntime(ffuf.OutputOptions{ProfileCPU: cpuPath, ProfileHeap: heapPath}, runtime)
	if err == nil {
		t.Fatal("start succeeded despite an existing output artifact")
	}
	if _, statErr := os.Stat(cpuPath); !os.IsNotExist(statErr) {
		t.Fatalf("new CPU artifact survived failed startup: %v", statErr)
	}
	data, readErr := os.ReadFile(heapPath)
	if readErr != nil || string(data) != "keep-me" {
		t.Fatalf("existing artifact was changed: data=%q err=%v", data, readErr)
	}
	if len(runtime.events) != 0 {
		t.Fatalf("runtime profiling started before every artifact was secured: %v", runtime.events)
	}
}

func TestProfilingCPUStartFailureRemovesAllNewArtifacts(t *testing.T) {
	dir := t.TempDir()
	paths := []string{filepath.Join(dir, "cpu.pprof"), filepath.Join(dir, "heap.pprof")}
	runtime := &recordingProfileRuntime{startCPUError: errors.New("CPU profiler busy")}
	_, err := startProfilingWithRuntime(ffuf.OutputOptions{ProfileCPU: paths[0], ProfileHeap: paths[1]}, runtime)
	if err == nil || !strings.Contains(err.Error(), "CPU profiler busy") {
		t.Fatalf("start error = %v", err)
	}
	for _, path := range paths {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Errorf("artifact %s survived failed CPU startup: %v", path, statErr)
		}
	}
}

func TestProfilingShutdownRestoresRatesAfterWriteFailures(t *testing.T) {
	dir := t.TempDir()
	runtime := &recordingProfileRuntime{
		mutexRate: 5, blockRate: 9,
		writeErrors: map[string]error{"mutex": errors.New("mutex write failed"), "block": errors.New("block write failed")},
	}
	session, err := startProfilingWithRuntime(ffuf.OutputOptions{
		ProfileMutex: filepath.Join(dir, "mutex.pprof"), ProfileBlock: filepath.Join(dir, "block.pprof"),
	}, runtime)
	if err != nil {
		t.Fatal(err)
	}
	closeErr := session.Close()
	if closeErr == nil || !strings.Contains(closeErr.Error(), "mutex write failed") || !strings.Contains(closeErr.Error(), "block write failed") {
		t.Fatalf("Close error = %v, want both write failures", closeErr)
	}
	if runtime.mutexRate != 5 || runtime.blockRate != 9 {
		t.Fatalf("rates after failed writes = mutex %d block %d, want 5 and 9", runtime.mutexRate, runtime.blockRate)
	}
	eventCount := len(runtime.events)
	if secondErr := session.Close(); secondErr == nil || secondErr.Error() != closeErr.Error() {
		t.Fatalf("second Close error = %v, want idempotent %v", secondErr, closeErr)
	}
	if len(runtime.events) != eventCount {
		t.Fatalf("second Close repeated lifecycle operations: before=%d after=%d", eventCount, len(runtime.events))
	}
}

func TestProfilingLifecycleWritesRealRuntimeArtifacts(t *testing.T) {
	dir := t.TempDir()
	paths := []string{
		filepath.Join(dir, "cpu.pprof"), filepath.Join(dir, "heap.pprof"),
		filepath.Join(dir, "mutex.pprof"), filepath.Join(dir, "block.pprof"),
	}
	session, err := startProfiling(ffuf.OutputOptions{
		ProfileCPU: paths[0], ProfileHeap: paths[1], ProfileMutex: paths[2], ProfileBlock: paths[3],
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(25 * time.Millisecond)
	var work uint64
	for time.Now().Before(deadline) {
		work = work*1664525 + 1013904223
	}
	if work == 0 {
		t.Log("CPU workload wrapped to zero")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 || info.Size() == 0 {
			t.Errorf("artifact %s: mode=%04o size=%d, want mode 0600 and non-empty", path, info.Mode().Perm(), info.Size())
		}
	}
}
