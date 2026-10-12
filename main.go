package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/assetnote/h3fuzz/pkg/assembly"
	"github.com/assetnote/h3fuzz/pkg/engine"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/assetnote/h3fuzz/pkg/filter"
	"github.com/assetnote/h3fuzz/pkg/input"
	"github.com/assetnote/h3fuzz/pkg/interactive"
	"github.com/assetnote/h3fuzz/pkg/runner"
	"github.com/assetnote/h3fuzz/pkg/webserver"
	webassets "github.com/assetnote/h3fuzz/web"
)

// flagRegistry describes the registered flags for the segmented help output
// (see Usage in help.go). It is (re)populated on every ParseFlags call.
var flagRegistry *ffuf.FlagRegistry

// ParseFlags registers every CLI flag from ffuf.RegisterFlags — the single source
// of truth — onto the global flag set and parses the command line into opts.
// Flag defaults are the current field values, so a value loaded from a config
// file is used unless the same flag is given on the command line (file < CLI).
func ParseFlags(opts *ffuf.ConfigOptions) *ffuf.ConfigOptions {
	flagRegistry = ffuf.RegisterFlags(flag.CommandLine, opts)
	flag.Usage = Usage
	flag.Parse()
	return opts
}

func main() {
	os.Exit(run())
}

func run() (exitCode int) {
	var err, optserr error
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// prepare the default config options from default config file
	var opts *ffuf.ConfigOptions
	opts, optserr = ffuf.ReadDefaultConfig()

	opts = ParseFlags(opts)

	// Handle searchhash functionality and exit
	if opts.General.Searchhash != "" {
		coptions, pos, err := engine.SearchHash(opts.General.Searchhash)
		if err != nil {
			fmt.Printf("[ERR] %s\n", err)
			return 1
		}
		if len(coptions) > 0 {
			fmt.Printf("Request candidate(s) for hash %s\n", opts.General.Searchhash)
		}
		for _, copt := range coptions {
			conf, err := ffuf.ConfigFromOptions(&copt.ConfigOptions, ctx, cancel)
			if err != nil {
				continue
			}
			ok, reason := engine.HistoryReplayable(conf)
			if ok {
				printSearchResults(conf, pos, copt.Time, opts.General.Searchhash)
			} else {
				fmt.Printf("[ERR] Hash cannot be mapped back because %s\n", reason)
			}

		}
		if err != nil {
			fmt.Printf("[ERR] %s\n", err)
		}
		return 0
	}

	if opts.General.ShowVersion {
		fmt.Printf("h3fuzz version: %s\n", ffuf.Version())
		return 0
	}
	if len(opts.Output.DebugLog) != 0 {
		f, err := os.OpenFile(opts.Output.DebugLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Disabling logging, encountered error(s): %s\n", err)
			log.SetOutput(io.Discard)
		} else {
			log.SetOutput(f)
			defer f.Close()
		}
	} else {
		log.SetOutput(io.Discard)
	}
	if optserr != nil {
		log.Printf("Error while opening default config file: %s", optserr)
	}

	if opts.General.ConfigFile != "" {
		opts, err = ffuf.ReadConfig(opts.General.ConfigFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Encoutered error(s): %s\n", err)
			Usage()
			fmt.Fprintf(os.Stderr, "Encoutered error(s): %s\n", err)
			return 1
		}
		// Reset the flag package state
		flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
		// Re-parse the cli options
		opts = ParseFlags(opts)
	}
	// Capture presence separately from value so explicit default-valued bounds
	// and CLI-only policy flags survive ConfigFromOptions and mode checks.
	flag.Visit(func(f *flag.Flag) {
		ffuf.MarkExplicitOptions(opts, f.Name)
	})
	if profilingRequested(opts.Output) {
		profiles, profileErr := startProfiling(opts.Output)
		if profileErr != nil {
			fmt.Fprintf(os.Stderr, "Could not start profiling: %s\n", profileErr)
			return 1
		}
		defer func() {
			if profileErr := profiles.Close(); profileErr != nil {
				fmt.Fprintf(os.Stderr, "Could not finish profiling: %s\n", profileErr)
				exitCode = 1
			}
		}()
	}
	if opts.General.Web {
		if ffuf.HighRatePolicyRequested(opts) {
			fmt.Fprintln(os.Stderr, "HTTP/3 high-rate policy flags are command-line scan options and cannot be used with -web")
			return 1
		}
		if err := runWebConsole(ctx, opts); err != nil {
			fmt.Fprintf(os.Stderr, "Web console failed: %s\n", err)
			return 1
		}
		return 0
	}

	// Set up Config struct
	conf, err := ffuf.ConfigFromOptions(opts, ctx, cancel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		Usage()
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		return 1
	}
	if conf.Http3Fingerprint {
		report, fingerprintErr := runner.FingerprintHTTP3(conf, true)
		if fingerprintErr != nil {
			report.Target = conf.Url
			report.Reason = fingerprintErr.Error()
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetEscapeHTML(false)
		if encodeErr := encoder.Encode(report); encodeErr != nil {
			fmt.Fprintf(os.Stderr, "could not encode HTTP/3 fingerprint: %s\n", encodeErr)
			return 1
		}
		if fingerprintErr != nil {
			fmt.Fprintf(os.Stderr, "HTTP/3 fingerprint failed: %s\n", fingerprintErr)
			return 1
		}
		if report.HighRateReady {
			return 0
		}
		return 2
	}

	// Matchers must exist before the runner probes response minimization: body
	// matchers and filters make HEAD/Range semantically unsafe.
	if err := SetupFilters(opts, conf); err != nil {
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		Usage()
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		return 1
	}

	job, err := assembly.BuildJob(conf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		Usage()
		fmt.Fprintf(os.Stderr, "Encountered error(s): %s\n", err)
		return 1
	}
	if conf.HighRatePolicy != nil && conf.HighRatePolicy.Enabled {
		policyJSON, marshalErr := json.MarshalIndent(conf.HighRatePolicy, "", "  ")
		if marshalErr != nil {
			fmt.Fprintf(os.Stderr, "Could not encode HTTP/3 high-rate policy: %s\n", marshalErr)
			return 1
		}
		fmt.Fprintf(os.Stderr, "HTTP/3 high-rate effective policy:\n%s\n", policyJSON)
		if !conf.HighRatePolicy.InputDurationHeadroom {
			fmt.Fprintf(os.Stderr, "WARNING: expanded input count %d is below the recommended %d inputs (target_rps * 36)\n",
				conf.HighRatePolicy.ExpandedInputs, conf.HighRatePolicy.RecommendedMinimumInputs)
		}
	}
	if job.AuditLogger != nil {
		defer job.AuditLogger.Close()
	}

	if !conf.Noninteractive {
		go func() {
			err := interactive.Handle(job)
			if err != nil {
				log.Printf("Error while trying to initialize interactive session: %s", err)
			}
		}()
	}

	// Job handles waiting for goroutines to complete itself
	job.Start()
	return 0
}

func runWebConsole(parent context.Context, opts *ffuf.ConfigOptions) error {
	server, err := webserver.New(webserver.Config{
		ListenAddress:   opts.General.WebListen,
		DatabasePath:    opts.General.WebDatabase,
		Username:        "h3fuzz",
		CertificatePath: opts.General.WebTLSCertificate,
		PrivateKeyPath:  opts.General.WebTLSPrivateKey,
		PlainHTTP:       opts.General.WebPlainHTTP,
		AllowRemote:     opts.General.WebAllowRemote,
		AllowInputCmd:   opts.General.WebAllowInputCmd,
		Assets:          webassets.FS(),
	})
	if err != nil {
		return err
	}
	listener, err := server.Listen()
	if err != nil {
		return err
	}
	password := server.BasicAuthPassword()
	fmt.Fprintf(os.Stderr, "h3fuzz web console listening on %s\nUsername: h3fuzz\nPassword: %s\n",
		server.ConsoleURL(listener), password)
	if opts.General.WebPlainHTTP {
		fmt.Fprintln(os.Stderr, "WARNING: Plain HTTP is enabled. Basic Auth credentials are not encrypted; only use this behind a trusted local TLS reverse proxy.")
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
		close(shutdownDone)
	}()
	if err := server.ServeListener(listener); err != nil {
		return err
	}
	if ctx.Err() != nil {
		<-shutdownDone
	}
	return nil
}

// SetupFilters installs the matchers and filters onto conf.MatcherManager. The
// matcher/filter construction itself lives in filter.FromConfig (pure, testable);
// this shim owns only the one decision that genuinely needs the global CLI flag
// set - whether to install the default status matcher - plus the -ignore-body
// warning.
func SetupFilters(parseOpts *ffuf.ConfigOptions, conf *ffuf.Config) error {
	// The default status matcher is installed unless another matcher was explicitly
	// passed on the CLI. Only the global flag set knows which flags were passed vs
	// defaulted, so this decision cannot move into the library.
	statusSet := false
	matcherSet := false
	// responseMatcherSet tracks the response-body matchers (ms/ml/mw) explicitly
	// passed on the CLI. The -ignore-body warning keys off CLI-passed flags (as the
	// original did via flag.Visit), NOT off the config value, so a config-file /
	// .ffufrc value does not spuriously trigger it.
	responseMatcherSet := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "mc":
			statusSet = true
		case "ms", "ml", "mw":
			matcherSet = true
			responseMatcherSet = true
		case "mr", "mt":
			matcherSet = true
		}
	})

	mm, err := filter.FromConfig(parseOpts, statusSet || !matcherSet)
	conf.MatcherManager = mm

	warningIgnoreBody := responseMatcherSet ||
		parseOpts.Filter.Size != "" || parseOpts.Filter.Lines != "" || parseOpts.Filter.Words != ""
	if conf.IgnoreBody && warningIgnoreBody {
		fmt.Printf("*** Warning: possible undesired combination of -ignore-body and the response options: fl,fs,fw,ml,ms and mw.\n")
	}
	return err
}

func printSearchResults(conf *ffuf.Config, pos int, exectime time.Time, hash string) {
	inp, err := input.NewInputProvider(conf)
	if err.ErrorOrNil() != nil {
		fmt.Printf("-------------------------------------------\n")
		fmt.Println("Encountered error that prevents reproduction of the request:")
		fmt.Println(err.ErrorOrNil())
		return
	}
	inp.SetPosition(pos)
	inputdata := inp.Value()
	inputdata["FFUFHASH"] = []byte(hash)
	basereq := ffuf.BaseRequest(conf)
	dummyrunner := runner.NewSimpleRunner(conf, false)
	ffufreq, _ := dummyrunner.Prepare(inputdata, &basereq)
	rawreq, _ := dummyrunner.Dump(&ffufreq)
	fmt.Printf("-------------------------------------------\n")
	fmt.Printf("h3fuzz job started at: %s\n\n", exectime.Format(time.RFC3339))
	fmt.Printf("%s\n", string(rawreq))
}
