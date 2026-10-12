package engine

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/assetnote/h3fuzz/pkg/ffuf"
)

func BenchmarkDisabledAutocalibrationResponsePath(b *testing.B) {
	job := &Job{
		Config: &ffuf.Config{},
		autocalibrationHost: func(ffuf.Request) string {
			b.Fatal("disabled response path parsed the request host")
			return ""
		},
	}
	req := ffuf.Request{Url: "https://example.test/bench/word?query=value"}
	input := map[string][]byte{"FUZZ": []byte("word")}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		job.calibrateResponse(req, input)
	}
}

func TestDisabledAutocalibrationResponsePathSkipsHostParseAndLock(t *testing.T) {
	var hostCalls atomic.Uint64
	job := &Job{
		Config: &ffuf.Config{},
		autocalibrationHost: func(ffuf.Request) string {
			hostCalls.Add(1)
			return "https://example.test"
		},
	}
	job.calibMutex.Lock()
	defer job.calibMutex.Unlock()

	done := make(chan struct{})
	go func() {
		job.calibrateResponse(ffuf.Request{Url: "https://example.test/bench/word"}, map[string][]byte{"FUZZ": []byte("word")})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("disabled response path waited for calibMutex")
	}
	if got := hostCalls.Load(); got != 0 {
		t.Fatalf("disabled response path parsed the host %d times", got)
	}
}

func TestCalibrateIfNeededDisabledGuardPrecedesLock(t *testing.T) {
	job := &Job{Config: &ffuf.Config{}}
	job.calibMutex.Lock()
	defer job.calibMutex.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- job.CalibrateIfNeeded("https://example.test", nil)
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("CalibrateIfNeeded: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("disabled CalibrateIfNeeded waited for calibMutex")
	}
}

func TestDisabledAutocalibrationGuardAllocatesNothing(t *testing.T) {
	job := &Job{Config: &ffuf.Config{}}
	req := ffuf.Request{Url: "https://example.test/bench/word"}
	input := map[string][]byte{"FUZZ": []byte("word")}
	if got := testing.AllocsPerRun(1000, func() { job.calibrateResponse(req, input) }); got != 0 {
		t.Fatalf("disabled autocalibration guard allocates %.2f times per call, want 0", got)
	}
}

func TestNewJobSnapshotsAutocalibrationEnabled(t *testing.T) {
	conf := &ffuf.Config{AutoCalibration: true}
	job := NewJob(conf)
	conf.AutoCalibration = false
	if !job.autocalibrationEnabled {
		t.Fatal("NewJob did not snapshot enabled autocalibration")
	}

	conf = &ffuf.Config{}
	job = NewJob(conf)
	conf.AutoCalibration = true
	if job.autocalibrationEnabled {
		t.Fatal("NewJob changed its disabled autocalibration snapshot")
	}
}
