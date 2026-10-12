// Package assembly wires a ffuf.Job together from a Config: the input provider,
// runner(s), output provider, audit logger, and scraper. It exists as its own
// package because it must import the provider packages (input/runner/output/
// scraper), which themselves import pkg/ffuf - so this composition cannot live in
// pkg/ffuf without an import cycle, and it cannot stay unexported in package main
// without being untestable. Both main and the integration tests call BuildJob, so
// the production and test wiring can never drift.
package assembly

import (
	"github.com/assetnote/h3fuzz/pkg/engine"
	"github.com/assetnote/h3fuzz/pkg/ffuf"
	"github.com/assetnote/h3fuzz/pkg/input"
	"github.com/assetnote/h3fuzz/pkg/output"
	"github.com/assetnote/h3fuzz/pkg/runner"
	"github.com/assetnote/h3fuzz/pkg/scraper"
)

// BuildJob constructs and wires a Job from conf. It is the single source of truth
// for how a Job is assembled. Matchers/filters are NOT installed here (they need
// the CLI flag state); see filter.FromConfig and main.SetupFilters.
func BuildJob(conf *ffuf.Config) (*engine.Job, error) {
	var err error
	var errs ffuf.Multierror
	inputProvider, inputErrs := input.NewInputProvider(conf)
	inputErr := inputErrs.ErrorOrNil()
	if inputErr != nil {
		errs.Add(inputErr)
	}

	// Load every active scraper rule before initializing the runner. Automatic
	// HEAD/Range selection must know whether installed defaults or a custom file
	// consumes resp.Data; discovering that after the probes would silently starve
	// body scrapers at execution time.
	newscraper, scraperErrs := scraper.FromDir(ffuf.SCRAPERDIR, conf.Scrapers)
	scraperReady := true
	if scraperErr := scraperErrs.ErrorOrNil(); scraperErr != nil {
		errs.Add(scraperErr)
		scraperReady = false
	}
	if conf.ScraperFile != "" {
		if appendErr := newscraper.AppendFromFile(conf.ScraperFile); appendErr != nil {
			errs.Add(appendErr)
			scraperReady = false
		}
	}
	conf.ScraperNeedsResponseBody = newscraper.NeedsResponseBody()

	// Protocol selection and response optimization can increase the worker count,
	// so initialize the runner before NewJob constructs its rate/progress state.
	mainRunner := runner.NewSimpleRunner(conf, false)
	runnerReady := true
	if initializer, ok := mainRunner.(interface{ Initialize(*ffuf.Request) error }); ok && inputErr == nil && scraperReady {
		baseRequest := ffuf.BaseRequest(conf)
		if initErr := initializer.Initialize(&baseRequest); initErr != nil {
			errs.Add(initErr)
			runnerReady = false
		}
	}
	if inputErr == nil && inputProvider.Total() > 0 && conf.Threads > inputProvider.Total() {
		conf.Threads = inputProvider.Total()
	}
	if inputErr == nil && scraperReady && runnerReady {
		if policyErr := ffuf.FinalizeHighRatePolicy(conf, inputProvider.Total()); policyErr != nil {
			errs.Add(policyErr)
		}
	}

	job := engine.NewJob(conf)
	job.Input = inputProvider
	job.Runner = mainRunner
	job.Scraper = newscraper
	if len(conf.ReplayProxyURL) > 0 {
		job.ReplayRunner = runner.NewSimpleRunner(conf, true)
	}

	// Only the stdout output provider exists today.
	job.Output = output.NewStdoutput(conf)

	if len(conf.AuditLog) > 0 {
		job.AuditLogger, err = output.NewAuditLogger(conf.AuditLog)
		if err != nil {
			errs.Add(err)
		} else {
			if err = job.AuditLogger.Write(conf); err != nil {
				errs.Add(err)
			}
		}
	}

	buildErr := errs.ErrorOrNil()
	if buildErr != nil {
		if closer, ok := job.Runner.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		if closer, ok := job.ReplayRunner.(interface{ Close() error }); ok {
			_ = closer.Close()
		}
	}
	return job, buildErr
}
