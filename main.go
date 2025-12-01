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
)

type Config struct {
	BaseURL     string
	Wordlist    string        // Wordlist Path
	Threads     int           // Number of concurrent threads - default is 10
	Timeout     time.Duration // Request timeout
	Port        int           // Port to use for Gemini requests
	Extension   bool          // Whether to append .gmi extension
	Recursive   int           // Level of recursion on directory hit
	Spider      bool          // Spider links on page. Default = true
	Insecure    bool          // Allow self-signed TLS connections
	Verbose     bool          // Verb logging
	Debug       bool          // Debug logging
	FilterCodes []string      // whitelisted gemini status codes
	FilterSize  int           // whitelisted gemini status codes
}

func parseConfig() (*Config, error) {

	var timeoutSec int
	var FilterCodes string

	cfg := &Config{}

	flag.StringVar(&cfg.BaseURL, "u", "", "Base Gemini URL with/without protocol wrapper (e.g., gemini://example.com)")
	flag.StringVar(&cfg.Wordlist, "w", "", "Path to wordlist file")
	flag.IntVar(&cfg.Threads, "t", 10, "Number of concurrent threads")
	flag.IntVar(&timeoutSec, "timeout", 10, "Request timeout duration")
	flag.BoolVar(&cfg.Extension, "x", false, "Append .gmi extension to each word")
	flag.IntVar(&cfg.Recursive, "r", 2, "Recursion level for directories")
	flag.IntVar(&cfg.Port, "p", 1965, "Port to use for Gemini requests")
	flag.IntVar(&cfg.FilterSize, "s", -1, "Filter out requests of a given size (in bytes)")
	flag.BoolVar(&cfg.Spider, "spider", true, "Spider links on page")
	flag.BoolVar(&cfg.Insecure, "k", true, "Allow insecure TLS connections (defaults to true)")
	flag.StringVar(&FilterCodes, "c", "2,3", "Comma-separated list of whitelisted status codes. Supports wildcards (e.g., 2 for all 2x codes)")
	flag.BoolVar(&cfg.Verbose, "v", false, "Enable verbose logging")
	flag.BoolVar(&cfg.Debug, "d", false, "Enable debug logging")
	flag.Parse()

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

	// Set logging level
	var level slog.LevelVar
	level.Set(slog.LevelWarn)

	if cfg.Verbose {
		level.Set(slog.LevelInfo)
	}
	if cfg.Debug {
		level.Set(slog.LevelDebug)
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: &level})))

	slog.Warn("hi", "config", &level)

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

func fetchGeminiOnce(rawURL string, timeout time.Duration, insecure bool) (status string, meta string, size int64, err error) {

	slog.Debug("Fetching URL", "url", rawURL)
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", "", 0, err
	}

	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", u.Host, &tls.Config{
		ServerName:         u.Hostname(),
		InsecureSkipVerify: insecure,
	})
	if err != nil {
		fmt.Printf("%s\n", err)
		return "", "", 0, err
	}
	defer conn.Close()

	// Send request line: full URL + CRLF
	if _, err := conn.Write([]byte(rawURL + "\r\n")); err != nil {
		return "", "", 0, err
	}

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

	// Count body bytes regardless of status; many servers send body only on 2x.
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		// Body read errors shouldn't hide header info; return partial
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

	// TODO: Add initial host-up check for quick aborts

	jobs := make(chan Job, len(wordlist))
	done := make(chan struct{})

	go func() {
		for _, w := range wordlist {

			// Clone the parsed base URL and set the path for this job.
			v := *baseURL
			v.Path = path.Join(baseURL.Path, w)

			if cfg.Extension {
				v.Path = v.Path + ".gmi"
				jobs <- Job{URL: v.String(), Depth: 0}
			} else {
				jobs <- Job{URL: v.String(), Depth: 0}
			}
		}
		close(jobs)
	}()

	var workers int = cfg.Threads

	// Use a wait counter via channel
	doneWorkers := make(chan struct{}, workers)
	for i := 0; i < workers; i++ {
		go func() {
			// Each worker consumes jobs; if whitelisted, it can enqueue follow-ups.
			for job := range jobs {

				u := job.URL
				depth := job.Depth
				status, meta, size, err := fetchGeminiOnce(u, cfg.Timeout, cfg.Insecure)
				if err != nil && cfg.Verbose {
					fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", u, err)
				}

				// Print output to stdout
				if isWhitelisted(status, cfg.FilterCodes) && (int(size) != cfg.FilterSize) {

					rawURL, _ := url.Parse(u)

					// Redirect
					if strings.HasPrefix(status, "3") {
						redirURL, _ := url.Parse(meta)
						fmt.Printf("%s\t\t[Status %s]\t%s -> %s\t\tSize: %d\n", rawURL.Path, status, rawURL.Path, redirURL.Path, size)
					} else {
						fmt.Printf("%s\t\t[Status %s]\t%s\t\tSize: %d\n", rawURL.Path, status, meta, size)
					}

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
