package end2end_carbon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/davecgh/go-spew/spew"
	"github.com/grafana/metrictank/stacktest/docker"
	"github.com/grafana/metrictank/stacktest/fakemetrics"
	"github.com/grafana/metrictank/stacktest/grafana"
	"github.com/grafana/metrictank/stacktest/graphite"
	"github.com/grafana/metrictank/stacktest/track"
)

// TODO: cleanup when ctrl-C go test (teardown all containers)

var tracker *track.Tracker
var fm *fakemetrics.FakeMetrics

const metricsPerSecond = 1000

func TestMain(m *testing.M) {
	fmt.Println("stopping docker-dev stack should it be running...")
	cmd := exec.Command("docker-compose", "down")
	cmd.Dir = docker.Path("docker/docker-dev")
	var err error
	tracker, err = track.NewTracker(cmd, false, false, "compose-down-stdout", "compose-down-stderr")
	if err != nil {
		log.Fatal(err)
	}
	err = cmd.Start()
	if err != nil {
		log.Fatal(err)
	}
	// note: even when we don't care about the output, it's best to consume it before calling cmd.Wait()
	// even though the cmd.Wait docs say it will wait for stdout/stderr copying to complete
	// however the docs for cmd.StdoutPipe say "it is incorrect to call Wait before all reads from the pipe have completed"
	tracker.Wait()
	if err := cmd.Wait(); err != nil {
		log.Printf("ERROR: could not cleanly shutdown running docker-compose down command: %s", err)
		os.Exit(2)
	}

	fmt.Println("launching docker-dev stack...")
	cmd = exec.Command(docker.Path("docker/launch.sh"), "docker-dev")

	tracker, err = track.NewTracker(cmd, false, false, "launch-stdout", "launch-stderr")
	if err != nil {
		log.Fatal(err)
	}

	err = cmd.Start()
	if err != nil {
		log.Fatal(err)
	}

	retcode := m.Run()
	fm.Close()

	fmt.Println("stopping docker-compose stack...")
	cmd.Process.Signal(syscall.SIGINT)
	tracker.Wait()
	if err := cmd.Wait(); err != nil {
		// 130 means ctrl-C (interrupt) which is what we want
		if err.Error() == "exit status 130" {
			os.Exit(retcode)
		}
		log.Printf("ERROR: could not cleanly shutdown running docker-compose command: %s", err)
		retcode = 1
	}

	os.Exit(retcode)
}

func TestStartup(t *testing.T) {
	matchers := []track.Matcher{
		{Str: "metrictank.*metricIndex initialized.*starting data consumption$"},
		{Str: "metrictank.*carbon-in: listening on.*2003"},
		{Str: "grafana.*Initializing HTTP Server.*:3000"},
	}
	select {
	case <-tracker.Match(matchers):
		fmt.Println("stack now running.")
		fmt.Println("Go to http://localhost:3000 (and login as admin:admin) to see what's going on")
	case <-time.After(time.Second * 40):
		grafana.PostAnnotation("TestStartup:FAIL")
		t.Fatal("timed out while waiting for all metrictank instances to come up")
	}
}

func TestBaseIngestWorkload(t *testing.T) {
	grafana.PostAnnotation("TestBaseIngestWorkload:begin")

	fm = fakemetrics.NewCarbon(metricsPerSecond)

	suc6, resp := graphite.RetryGraphite8080("perSecond(metrictank.stats.docker-env.*.input.carbon.metrics_received.counter32)", "-8s", 18, func(resp graphite.Response) bool {
		exp := []string{
			"perSecond(metrictank.stats.docker-env.default.input.carbon.metrics_received.counter32)",
		}
		a := graphite.ValidateTargets(exp)(resp)
		b := graphite.ValidatorLenNulls(1, 8)(resp)
		c := graphite.ValidatorAvgWindowed(8, graphite.Ge(metricsPerSecond))(resp)
		log.Printf("condition target names %t - condition len & nulls %t - condition avg value %t", a, b, c)
		return a && b && c
	})
	if !suc6 {
		grafana.PostAnnotation("TestBaseIngestWorkload:FAIL")
		t.Fatalf("cluster did not reach a state where the MT instance receives 4 points per second. last response was: %s", spew.Sdump(resp))
	}
}
