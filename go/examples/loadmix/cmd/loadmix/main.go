package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
)

const (
	defaultPool     = 80
	allKind         = "all"
	saturationUsers = 16
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	code, err := run(ctx, os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loadmix:", err)
	}
	os.Exit(code)
}

func run(ctx context.Context, args []string, out io.Writer) (int, error) {
	set := flag.NewFlagSet("loadmix", flag.ContinueOnError)
	dsn := set.String("dsn", os.Getenv("GOLEM_TEST_POSTGRES_DSN"), "PostgreSQL admin DSN (default GOLEM_TEST_POSTGRES_DSN)")
	root := set.String("root", ".", "examples/loadmix directory holding the reviewed migrations")
	cli := set.String("golem", "", "golem CLI binary (default ../../bin/golem relative to -root)")
	pool := set.Int("pool", defaultPool, "database pool maximum open connections")
	kind := set.String("kind", allKind, "steady, burst, saturation or all")
	users := set.Int("users", 100, "simulated users for steady and burst")
	writers := set.Int("writers", saturationUsers, "closed-loop writers for saturation")
	warmup := set.Duration("warmup", steadyWarmup, "steady warmup")
	measure := set.Duration("measure", steadyMeasure, "steady measured window")
	asJSON := set.Bool("json", false, "print the full result as JSON")
	if err := set.Parse(args); err != nil {
		return 2, err
	}
	environment, err := newEnvironment(*dsn, *root, *cli, *pool)
	if err != nil {
		return 2, err
	}
	var specs []RunSpec
	switch Kind(*kind) {
	case KindSteady:
		specs = []RunSpec{{Kind: KindSteady, Users: *users, Warmup: *warmup, Measure: *measure}}
	case KindBurst:
		specs = []RunSpec{{Kind: KindBurst, Users: *users}}
	case KindSaturation:
		specs = []RunSpec{{Kind: KindSaturation, Users: *writers}}
	case allKind:
		specs = []RunSpec{{Kind: KindSteady, Users: *users, Warmup: *warmup, Measure: *measure}, {Kind: KindBurst, Users: burstUsers}, {Kind: KindSaturation, Users: *writers}}
	default:
		return 2, fmt.Errorf("kind must be steady, burst, saturation or all, got %q", *kind)
	}
	code := 0
	for _, spec := range specs {
		spec.Seed = 1000
		detail, err := Execute(ctx, environment, spec)
		if err != nil {
			return 1, err
		}
		if *asJSON {
			encoder := json.NewEncoder(out)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(detail); err != nil {
				return 1, err
			}
		} else {
			render(out, detail)
		}
		if !detail.Passed() {
			code = 1
		}
	}
	return code, nil
}

func newEnvironment(dsn, root, cli string, pool int) (Environment, error) {
	if dsn == "" {
		return Environment{}, errors.New("a PostgreSQL DSN is required: pass -dsn or set GOLEM_TEST_POSTGRES_DSN")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Environment{}, err
	}
	if cli == "" {
		cli = filepath.Join(absolute, "..", "..", "bin", "golem")
	}
	if _, err := os.Stat(cli); err != nil {
		return Environment{}, fmt.Errorf("golem CLI not found at %s (run make go-build): %w", cli, err)
	}
	return Environment{AdminDSN: dsn, Root: absolute, CLI: cli, Pool: pool}, nil
}

func render(out io.Writer, detail RunDetail) {
	verdict := "PASS"
	if !detail.Passed() {
		verdict = "FAIL"
	}
	fmt.Fprintf(out, "%s users=%d %s (ran %.0fs, measured %.0fs, drain %.1fs)\n", detail.Kind, detail.Users, verdict, detail.RanSecs, detail.MeasuredSecs, detail.DrainSecs)
	for _, class := range allClasses {
		stats := detail.Classes[class]
		fmt.Fprintf(out, "  %-15s offered %6d achieved %6d %8.1f/s  commit ms p50 %7.1f p99 %8.1f max %8.1f\n", class, stats.Offered, stats.Achieved, stats.PerSecond, stats.LatencyMilli.P50, stats.LatencyMilli.P99, stats.LatencyMilli.Max)
	}
	delivery := detail.Delivery
	fmt.Fprintf(out, "  delivery        expected %d delivered %d foreign %d duplicates %d lag p99 %.1f ms\n", delivery.Expected, delivery.Delivered, delivery.Foreign, delivery.Duplicates, detail.DeliveryLagP99)
	codes := make([]string, 0, len(delivery.Terminations))
	for code := range delivery.Terminations {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	for _, code := range codes {
		fmt.Fprintf(out, "  terminated      %s x%d\n", code, delivery.Terminations[code])
	}
	counters := detail.Counters
	fmt.Fprintf(out, "  publisher       claims %d notices %d claim size p50 %.0f p99 %.0f max %.0f\n", counters.Claims, counters.Claimed, counters.ClaimSize.P50, counters.ClaimSize.P99, counters.ClaimSize.Max)
	fmt.Fprintf(out, "  evaluation      %d evaluations, %.2f per notice, judge %.3f ms per notice\n", counters.Evaluations, counters.EvaluationsPerNotice, counters.JudgeMsPerNotice)
	reasons := make([]string, 0, len(counters.Suppressions))
	for reason := range counters.Suppressions {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		fmt.Fprintf(out, "  suppressed      %s x%d\n", reason, counters.Suppressions[reason])
	}
	for _, violation := range detail.Violations {
		fmt.Fprintf(out, "  VIOLATION       %s\n", violation)
	}
}
