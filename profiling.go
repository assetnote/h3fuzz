package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strings"
	"sync"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

type profileRuntime interface {
	StartCPUProfile(io.Writer) error
	StopCPUProfile()
	WriteHeapProfile(io.Writer) error
	WriteProfile(string, io.Writer) error
	SetMutexProfileFraction(int) int
	SetBlockProfileRate(int) int
}

type goProfileRuntime struct{}

func (goProfileRuntime) StartCPUProfile(w io.Writer) error { return pprof.StartCPUProfile(w) }
func (goProfileRuntime) StopCPUProfile()                   { pprof.StopCPUProfile() }
func (goProfileRuntime) WriteHeapProfile(w io.Writer) error {
	return pprof.WriteHeapProfile(w)
}
func (goProfileRuntime) WriteProfile(name string, w io.Writer) error {
	profile := pprof.Lookup(name)
	if profile == nil {
		return fmt.Errorf("runtime profile %q is unavailable", name)
	}
	return profile.WriteTo(w, 0)
}
func (goProfileRuntime) SetMutexProfileFraction(rate int) int {
	return runtime.SetMutexProfileFraction(rate)
}

var blockProfileRateState struct {
	sync.Mutex
	rate int
}

func (goProfileRuntime) SetBlockProfileRate(rate int) int {
	// Unlike SetMutexProfileFraction, Go's block profiler has no getter and its
	// setter does not return the previous rate. h3fuzz owns every block-rate
	// change it makes, so keep that process-local value beside the runtime call
	// and restore it through this same serialized path.
	blockProfileRateState.Lock()
	defer blockProfileRateState.Unlock()
	previous := blockProfileRateState.rate
	runtime.SetBlockProfileRate(rate)
	blockProfileRateState.rate = rate
	return previous
}

type profilingSession struct {
	runtime   profileRuntime
	cpu       *os.File
	heap      *os.File
	mutex     *os.File
	block     *os.File
	mutexRate int
	blockRate int
	once      sync.Once
	closeErr  error
}

func startProfiling(opts ffuf.OutputOptions) (*profilingSession, error) {
	return startProfilingWithRuntime(opts, goProfileRuntime{})
}

func profilingRequested(opts ffuf.OutputOptions) bool {
	return opts.ProfileCPU != "" || opts.ProfileHeap != "" ||
		opts.ProfileMutex != "" || opts.ProfileBlock != ""
}

func startProfilingWithRuntime(opts ffuf.OutputOptions, runtime profileRuntime) (*profilingSession, error) {
	session := &profilingSession{runtime: runtime}
	type requestedProfile struct {
		label string
		path  string
		dest  **os.File
	}
	requested := []requestedProfile{
		{"CPU", opts.ProfileCPU, &session.cpu},
		{"heap", opts.ProfileHeap, &session.heap},
		{"mutex", opts.ProfileMutex, &session.mutex},
		{"block", opts.ProfileBlock, &session.block},
	}
	seenPaths := make(map[string]string, len(requested))
	for _, profile := range requested {
		if profile.path == "" {
			continue
		}
		if strings.TrimSpace(profile.path) == "" {
			return nil, fmt.Errorf("%s profile path must not be blank", profile.label)
		}
		absolute, err := filepath.Abs(profile.path)
		if err != nil {
			return nil, fmt.Errorf("resolve %s profile path: %w", profile.label, err)
		}
		canonical := filepath.Clean(absolute)
		if previous := seenPaths[canonical]; previous != "" {
			return nil, fmt.Errorf("%s and %s profiles cannot use the same output path %q", previous, profile.label, profile.path)
		}
		seenPaths[canonical] = profile.label
	}
	created := make([]requestedProfile, 0, len(requested))
	rollback := func() {
		for _, profile := range created {
			_ = (*profile.dest).Close()
			_ = os.Remove(profile.path)
			*profile.dest = nil
		}
	}
	for _, profile := range requested {
		if profile.path == "" {
			continue
		}
		file, err := openProfileFile(profile.path)
		if err != nil {
			rollback()
			return nil, fmt.Errorf("create %s profile: %w", profile.label, err)
		}
		*profile.dest = file
		created = append(created, profile)
	}
	if session.cpu != nil {
		if err := runtime.StartCPUProfile(session.cpu); err != nil {
			rollback()
			return nil, fmt.Errorf("start CPU profile: %w", err)
		}
	}
	if session.mutex != nil {
		session.mutexRate = runtime.SetMutexProfileFraction(1)
	}
	if session.block != nil {
		session.blockRate = runtime.SetBlockProfileRate(1)
	}
	return session, nil
}

func openProfileFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(path)
		return nil, err
	}
	return file, nil
}

func (s *profilingSession) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		var errs []error
		if s.cpu != nil {
			s.runtime.StopCPUProfile()
			if err := s.cpu.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close CPU profile: %w", err))
			}
		}
		if s.heap != nil {
			if err := s.runtime.WriteHeapProfile(s.heap); err != nil {
				errs = append(errs, fmt.Errorf("write heap profile: %w", err))
			}
			if err := s.heap.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close heap profile: %w", err))
			}
		}
		if s.mutex != nil {
			if err := s.runtime.WriteProfile("mutex", s.mutex); err != nil {
				errs = append(errs, fmt.Errorf("write mutex profile: %w", err))
			}
			s.runtime.SetMutexProfileFraction(s.mutexRate)
			if err := s.mutex.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close mutex profile: %w", err))
			}
		}
		if s.block != nil {
			if err := s.runtime.WriteProfile("block", s.block); err != nil {
				errs = append(errs, fmt.Errorf("write block profile: %w", err))
			}
			s.runtime.SetBlockProfileRate(s.blockRate)
			if err := s.block.Close(); err != nil {
				errs = append(errs, fmt.Errorf("close block profile: %w", err))
			}
		}
		s.closeErr = errors.Join(errs...)
	})
	return s.closeErr
}
