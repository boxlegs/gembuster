package main

import (
	"bufio"
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
)

type Config struct {
	BaseURL     string
	Wordlist    string        // Wordlist Path
	Threads     int           // Number of concurrent threads - default is 10
	Timeout     time.Duration // Request timeout
	Port        int           // Port to use for Gemini requests
	Extensions  []string      // Comma-separated list of extensions to append to each word
	Recursive   int           // Level of recursion on directory hit
	Spider      bool          // Spider links on page. Default = true
	Insecure    bool          // Allow self-signed TLS connections
	Verbose     bool          // Verb logging
	Debug       bool          // Debug logging
	FilterCodes []string      // whitelisted gemini status codes
	FilterSize  int           // whitelisted gemini status codes
	Mode        string        // Fuzzing mode: subdir, subdomain, query
}

func parseConfig() (*Config, error) {

	var timeoutSec int
	var FilterCodes string
	var Extensions string

	cfg := &Config{}

	if len(os.Args) < 2 {
		return nil, fmt.Errorf("usage: %s <mode> [flags]", os.Args[0])
	}

	cfg.Mode = strings.ToLower(os.Args[1])
	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	fs.StringVar(&cfg.BaseURL, "u", "", "Base Gemini URL (e.g., gemini://example.com)")
	fs.StringVar(&cfg.Wordlist, "w", "", "Path to wordlist file")
	fs.IntVar(&cfg.Threads, "t", 10, "Number of concurrent threads")
	fs.IntVar(&timeoutSec, "timeout", 10, "Request timeout duration (seconds)")
	fs.StringVar(&Extensions, "x", "", "Comma-separated list of extensions to append to each word")
	fs.IntVar(&cfg.Recursive, "r", 2, "Recursion level for directories")
	fs.IntVar(&cfg.Port, "p", 1965, "Port to use for Gemini requests")
	fs.IntVar(&cfg.FilterSize, "s", -1, "Filter out requests of a given size (in bytes)")
	fs.BoolVar(&cfg.Spider, "spider", true, "Spider links on page")
	fs.BoolVar(&cfg.Insecure, "k", true, "Allow insecure TLS connections")
	fs.StringVar(&FilterCodes, "c", "2,3", "Comma-separated whitelisted status codes (supports wildcards)")
	fs.BoolVar(&cfg.Verbose, "v", false, "Enable verbose logging")
	fs.BoolVar(&cfg.Debug, "d", false, "Enable debug logging")

	// Custom usage showing positional mode
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s <mode> [flags]\n", os.Args[0])
		fmt.Fprintln(os.Stderr, "Modes: dir, vhost, query")
		fs.PrintDefaults()
	}

	// Parse only after the first positional arg
	if err := fs.Parse(os.Args[2:]); err != nil {
		return nil, err
	}

	if len(Extensions) > 0 {
		cfg.Extensions = strings.Split(Extensions, ",")
	}

	switch cfg.Mode {
	case "vhost", "query":
	case "dir": // Add default .gmi extension when dirbusting
		cfg.Extensions = append(cfg.Extensions, "gmi")
	default:
		fs.Usage()
		return nil, fmt.Errorf("unsupported mode: %s", cfg.Mode)
	}

	if cfg.Threads <= 0 {
		return nil, fmt.Errorf("threads must be > 0")
	}
	if timeoutSec <= 0 {
		return nil, fmt.Errorf("timeout must be > 0")
	}
	cfg.Timeout = time.Duration(timeoutSec) * time.Second
	cfg.FilterCodes = strings.Split(FilterCodes, ",")

	// groom the baseURL
	if !strings.HasPrefix(cfg.BaseURL, "gemini://") {
		cfg.BaseURL = "gemini://" + cfg.BaseURL
	}

	var level slog.LevelVar
	level.Set(slog.LevelWarn)

	if cfg.Verbose {
		level.Set(slog.LevelInfo)
	}
	if cfg.Debug {
		level.Set(slog.LevelDebug)
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &level})))
	return cfg, nil
}

func parseWordlist(path string) ([]string, error) {

	var wordlist []string
	if file, err := os.Open(path); err != nil {
		return nil, fmt.Errorf("failed to open wordlist file: %v", err)
	} else {
		defer file.Close()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" {
				wordlist = append(wordlist, line)
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}

	return wordlist, nil
}

func fetchGeminiOnce(rawURL string, baseURL *url.URL, timeout time.Duration, insecure bool) (status string, meta string, size int64, err error) {

	slog.Debug("Fetching URL", "url", rawURL)

	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", 0, err
	}

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", baseURL.Host, &tls.Config{
		ServerName:         u.Hostname(),
		InsecureSkipVerify: insecure,
	})

	if err != nil {
		fmt.Printf("%s\n", err)
		return "", "", 0, err
	}
	defer conn.Close()

	if _, err := conn.Write([]byte(rawURL + "\r\n")); err != nil {
		return "", "", 0, err
	}

	// Read in response header
	r := bufio.NewReader(conn)
	header, err := r.ReadString('\n')
	if err != nil {
		return "", "", 0, err
	}

	header = strings.TrimRight(header, "\r\n")
	parts := strings.SplitN(header, " ", 2)
	status = ""
	meta = ""
	if len(parts) > 0 {
		status = parts[0]
	}
	if len(parts) > 1 {
		meta = parts[1]
	}

	// TODO: Add filtering based on body content

	n, err := io.Copy(io.Discard, r) // Discard body
	if err != nil {
		return status, meta, n, err
	}
	return status, meta, n, nil
}

func isWhitelisted(status string, codes []string) bool {

	for _, pattern := range codes {
		p := strings.TrimSpace(pattern)
		if p == "all" {
			return true
		}
		if len(p) == 1 { // Wildcard e.g. 2x, 3x
			if strings.HasPrefix(status, p) {
				return true
			}
			continue
		}

		if status == p {
			return true
		}

	}
	return false
}

// Template for URL generators (e.g. subdir,subdomain,query)
type URLGen func(base *url.URL, word string) *url.URL

func dirURLGen(base *url.URL, word string) *url.URL {
	u := *base
	u.Path = path.Join(base.Path, word)
	return &u
}

func vhostURLGen(base *url.URL, word string) *url.URL {
	u := *base
	host := word + "." + base.Hostname()
	u.Host = net.JoinHostPort(host, strings.Split(u.Host, ":")[1])
	return &u
}

func buildURLs(base *url.URL, wordlist []string, gen URLGen, extensions []string) []Job {
	var jobs []Job
	for _, w := range wordlist {

		if u := gen(base, w); u != nil {
			jobs = append(jobs, Job{URL: u.String(), Depth: 0})

		}

		// For each extension provided.
		if len(extensions) > 0 {
			for _, ext := range extensions {
				if u := gen(base, w+"."+strings.Trim(ext, ".")); u != nil {
					jobs = append(jobs, Job{URL: u.String(), Depth: 0})
				}
			}
		}
	}

	slog.Debug("Built URLs", "count", len(jobs))

	return jobs
}

func formatStatusCode(code string) func(string, ...interface{}) string {

	var formatted func(string, ...interface{}) string

	switch code[0] {
	case '1':
		formatted = color.BlueString
	case '2':
		formatted = color.GreenString
	case '3':
		formatted = color.YellowString
	case '4':
		formatted = color.RedString
	case '5':
		formatted = color.RedString
	default:
		formatted = color.WhiteString
	}

	return formatted
}

func formatOutput(u *url.URL, mode string) string {
	switch strings.ToLower(mode) {
	case "vhost":
		return u.Hostname()
	case "dir":
		fallthrough
	default:
		if u.Path == "" {
			return "/"
		}
		return u.Path
	}
}

type Job struct {
	URL   string
	Depth int
}

func main() {

	cfg, err := parseConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error parsing config: %v\n", err)
		os.Exit(1)
	}

	wordlist, err := parseWordlist(cfg.Wordlist)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading wordlist: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Started Gemini directory busting on %s with %d threads\n", cfg.BaseURL, cfg.Threads)
	fmt.Printf("Loaded %d words from wordlist\n\n", len(wordlist))

	slog.Debug("Attempting heartbeat", "URL", cfg.BaseURL)
	u, _ := url.Parse(cfg.BaseURL)
	u.Host = net.JoinHostPort(u.Hostname(), strconv.Itoa(cfg.Port))
	baseURL := u

	fmt.Printf("Using base URL: %s\n\n", baseURL.String())

	jobs := make(chan Job, len(wordlist))
	done := make(chan struct{})

	// Setup Output Formatting
	bold := color.New(color.Bold).SprintFunc()

	var urlGen URLGen
	switch cfg.Mode {
	case "dir":
		urlGen = dirURLGen
	case "vhost":
		urlGen = vhostURLGen
	}

	seedJobs := buildURLs(baseURL, wordlist, urlGen, cfg.Extensions)
	go func() {
		for _, j := range seedJobs {
			jobs <- j
		}
		close(jobs)
	}()
	var workers int = cfg.Threads

	doneWorkers := make(chan struct{}, workers)
	for i := 0; i < workers; i++ {
		go func() {
			for job := range jobs {

				u := job.URL
				depth := job.Depth
				status, meta, size, err := fetchGeminiOnce(u, baseURL, cfg.Timeout, cfg.Insecure)
				if err != nil && cfg.Verbose {
					fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", u, err)
				}

				if isWhitelisted(status, cfg.FilterCodes) && (int(size) != cfg.FilterSize) {
					rawURL, _ := url.Parse(u)
					var outputURL = formatOutput(rawURL, cfg.Mode)

					// Redirect logic
					if strings.HasPrefix(status, "3") {
						redirURL, _ := url.Parse(meta)
						outputURL = fmt.Sprintf("%s -> %s", formatOutput(rawURL, cfg.Mode), formatOutput(redirURL, cfg.Mode))
					}

					// Print row to stdout with padding
					fmt.Printf("%-6s %-*s Size: %-6d %s\n",
						bold(formatStatusCode(status)(fmt.Sprintf("[%s]", status))),
						30, outputURL,
						size,
						meta)

					// If directory, recurse if not at max depth
					if strings.HasPrefix(status, "2") && strings.HasSuffix(u, "/") && depth < cfg.Recursive {
						// Enqueue new job
						slog.Info("Hit new directory", "url", u, "depth", depth)

						// TODO: Decide where or not to add recursion. Will require job/queue rework
					}
				}
			}
			doneWorkers <- struct{}{}
		}()
	}

	// Wait for all workers to finish
	for i := 0; i < workers; i++ {
		<-doneWorkers
	}
	close(done)
}
