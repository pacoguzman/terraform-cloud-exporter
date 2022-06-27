package setup

import (
	"bufio"
	"crypto/tls"
	"net/http"
	"os"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/alecthomas/kong"

	tfe "github.com/hashicorp/go-tfe"
)

type CLI struct {
	Organizations         []string `short:"o" env:"TF_ORGANIZATIONS" placeholder:"ORG1,ORG2" help:"List of the Organization names to scrape from (Ommit to scrape all)."`
	APIToken              string   `short:"t" env:"TF_API_TOKEN" help:"User token for autheticating with the API."`
	APITokenFile          *os.File `placeholder:"/path/to/file" help:"File containing user token for autheticating with the API."`
	APIAddress            string   `placeholder:"https://app.terraform.io/" help:"Terraform API address to scrape metrics from."`
	APIInsecureSkipVerify bool     `help:"Accept any certificate presented by the API."`
	ListenAddress         string   `default:"0.0.0.0:9100" help:"Address to listen on for web interface and telemetry."`
	LogLevel              string   `default:"info" enum:"debug,info,warn,error" help:"Only log messages with the given severity or above. One of: [${enum}]"`
	LogFormat             string   `default:"logfmt" enum:"logfmt,json" help:"Output format of log messages. One of: [${enum}]"`
}

type Config struct {
	CLI
	Client tfe.Client
	Logger log.Logger
}

// NewConfig returns a new Config object that was initialized according to the CLI params.
func NewConfig() Config {
	config := Config{}
	kong.Parse(&config.CLI)
	config.setupLogger()
	config.setupClient()
	return config
}

func (c *Config) setupLogger() {
	// Changes timestamp from 9 variable to 3 fixed
	// decimals (.130 instead of .130987456).
	timestampFormat := log.TimestampFormat(
		func() time.Time { return time.Now().UTC() },
		"2006-01-02T15:04:05.000Z07:00",
	)

	if c.LogFormat == "json" {
		c.Logger = log.NewJSONLogger(log.NewSyncWriter(os.Stderr))
	} else {
		c.Logger = log.NewLogfmtLogger(log.NewSyncWriter(os.Stderr))
	}

	switch c.LogLevel {
	case "debug":
		c.Logger = level.NewFilter(c.Logger, level.AllowDebug())
	case "warn":
		c.Logger = level.NewFilter(c.Logger, level.AllowWarn())
	case "error":
		c.Logger = level.NewFilter(c.Logger, level.AllowError())
	default:
		c.Logger = level.NewFilter(c.Logger, level.AllowInfo())
	}

	c.Logger = log.With(c.Logger, "ts", timestampFormat, "caller", log.DefaultCaller)
}

func (c *Config) setupClient() {
	config := &tfe.Config{}

	if c.APITokenFile != nil {
		defer c.APITokenFile.Close()
		scanner := bufio.NewScanner(c.APITokenFile)
		scanner.Scan()
		config.Token = scanner.Text()
	} else if c.APIToken != "" {
		config.Token = c.APIToken
	} else {
		level.Error(c.Logger).Log("msg", "Error creating tfe client", "err", "Missing API Token.")
		os.Exit(1)
	}

	if c.APIAddress != "" {
		config.Address = c.APIAddress
		level.Info(c.Logger).Log("msg", "Overwritten Terraform API address", "address", c.APIAddress)
	}

	config.HTTPClient = c.setupHTTPClient()

	client, err := tfe.NewClient(config)
	if err != nil {
		level.Error(c.Logger).Log("msg", "Error creating tfe client", "err", err)
		os.Exit(1)
	}
	c.Client = *client
}

func (c *Config) setupHTTPClient() *http.Client {
	reg := prometheus.DefaultRegisterer

	inFlightGauge := prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "client_api_in_flight_requests",
		Help: "A gauge of in-flight requests for the wrapped client.",
	})

	counter := prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "client_api_requests_total",
			Help: "A counter for requests from the wrapped client.",
		},
		[]string{"code", "method"},
	)

	histVec := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "client_api_request_duration_seconds",
			Help:    "A histogram of request latencies.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method"},
	)

	reg.MustRegister(counter, histVec, inFlightGauge)

	tlsConfig := tls.Config{}

	if c.APIInsecureSkipVerify {
		tlsConfig = tls.Config{InsecureSkipVerify: c.APIInsecureSkipVerify}
	}

	roundTripper := promhttp.InstrumentRoundTripperInFlight(inFlightGauge,
		promhttp.InstrumentRoundTripperCounter(counter,
			promhttp.InstrumentRoundTripperDuration(histVec, &http.Transport{
				TLSClientConfig: &tlsConfig,
			}),
		),
	)

	return &http.Client{Transport: roundTripper}
}
