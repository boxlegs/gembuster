package main

import (
    "bufio"
    "crypto/tls"
    "flag"
    "fmt"
    "net"
    "net/url"
    "io"
    "os"
    "strings"
    "time"
)

type Config struct {
    BaseURL string
    Wordlist string // Wordlist Path
    Threads int // Number of concurrent threads - default is 10
    Timeout time.Duration // Request timeout
    Port int // Port to use for Gemini requests
    Extension bool // Whether to append .gmi extension
    Recursive int // Level of recursion on directory hit
    Spider bool // Spider links on page. Default = true
    Insecure bool // Allow self-signed TLS connections
    Verbose bool // Verb logging 
    Codes []string // whitelisted gemini status codes
}

func parseConfig() (*Config, error) {

    var timeoutSec int
    var codes string

    cfg := &Config{}
    flag.StringVar(&cfg.BaseURL, "u", "", "Base Gemini URL with/without protocol wrapper (e.g., gemini://example.com)")
    flag.StringVar(&cfg.Wordlist, "w", "", "Path to wordlist file")
    flag.IntVar(&cfg.Threads, "t", 10, "Number of concurrent threads")
    flag.IntVar(&timeoutSec, "timeout", 10, "Request timeout duration")
    flag.BoolVar(&cfg.Extension, "ext", false, "Append .gmi extension to each word")
    flag.IntVar(&cfg.Recursive, "r", 2, "Recursion level for directories")
    flag.IntVar(&cfg.Port, "p", 1965, "Port to use for Gemini requests")
    flag.BoolVar(&cfg.Spider, "spider", true, "Spider links on page")
    flag.BoolVar(&cfg.Insecure, "k", true, "Allow self-signed TLS connections")
    flag.StringVar(&codes, "codes", "2*,3*", "Comma-separated list of whitelisted status codes. Supports wildcards (e.g., 2* for all 2x codes)")
    flag.BoolVar(&cfg.Verbose, "v", false, "Enable verbose logging")
    flag.Parse()

    if cfg.Threads <= 0 {
        return nil, fmt.Errorf("threads must be > 0")
    }
    if timeoutSec <= 0 {
        return nil, fmt.Errorf("timeout must be > 0")
    }
    cfg.Timeout = time.Duration(timeoutSec) * time.Second
    cfg.Codes = strings.Split(codes, ",")

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
    
    // TODO: Offload to logging fmt.Printf("Fetching: %s\n", rawURL)

    if !strings.Contains(rawURL, "://") {
        rawURL = "gemini://" + rawURL
    }
    u, err := url.Parse(rawURL)
    if err != nil {
        return "", "", 0, err
    }


    host := u.Host

    dialer := &net.Dialer{Timeout: timeout}
    conn, err := tls.DialWithDialer(dialer, "tcp", host, &tls.Config{
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
    // Gemini status codes are 2 digits; whitelist "2x"
    
    for _, pattern := range codes {
        p := strings.TrimSpace(pattern)
        if p == "*" {
            return true
        }
        if strings.HasSuffix(p, "*") { // Wildcard
            prefix := strings.TrimSuffix(p, "*")
            if strings.HasPrefix(status, prefix) {
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

// TODO: Implement Job struct for tracking recursion depth + any other deets we might need
// type Job struct {
//     URI   string
//     Depth int
// }

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

    fmt.Printf("Starting Gemini directory busting on %s with %d threads\n", cfg.BaseURL, cfg.Threads)
    fmt.Printf("Loaded %d words from wordlist\n", len(wordlist))

    u, err := url.Parse(cfg.BaseURL)

    base := fmt.Sprintf("%s:%d", u.Hostname(), cfg.Port)

    fmt.Printf("Using base URL: %s\n", base)

    jobs := make(chan string, len(wordlist))
    done := make(chan struct{})

    go func() {
        for _, w := range wordlist {
            u := base + "/" + w

            // If extension is enabled, append .gmi as well
            if cfg.Extension {
                jobs <- u + ".gmi"
            }
            jobs <- u
        }
        close(jobs)
    }()


    var workers int = cfg.Threads

    // Use a wait counter via channel
    doneWorkers := make(chan struct{}, workers)
    for i := 0; i < workers; i++ {
        go func() {
            // Each worker consumes jobs; if whitelisted, it can enqueue follow-ups.
            for u := range jobs {
                status, meta, size, err := fetchGeminiOnce(u, cfg.Timeout, cfg.Insecure)
                if err != nil && cfg.Verbose {
                    fmt.Fprintf(os.Stderr, "Error fetching %s: %v\n", u, err)
                }
                

                if strings.HasPrefix(status, "3") {
                    // TODO: Handle redirect -> needs the whole header
                    // Similarly if we hit a directory listing we need another worker queue
                    continue
                } 

                // Go through Whitelist/Blacklist checks. Should 31 redirects happen before or after? 
                if isWhitelisted(status, cfg.Codes) {
                
                    fmt.Printf("%s\t\t[Status %s]\tSize: %d\n", u, status, size)
                }

                // TODO: Fix this by using WaitGroup and prio queue
                // if cfg.Recursive > 0 && !strings.HasSuffix(u, "/") {
                //     jobs <- u + "/"
                // }

                // TODO: Implement per-status/family stats

                // If whitelisted (2x), add to pool: you can define follow-up behavior here.
                // Example: if Recursive > 0, enqueue the same URL with a trailing slash for deeper probing.
                

                _ = meta // meta available if you want to use it (e.g., mime type or redirect target)
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