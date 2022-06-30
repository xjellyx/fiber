// ⚡️ Fiber is an Express inspired web framework written in Go with ☕️
// 🤖 Github Repository: https://github.com/gofiber/fiber
// 📌 API Documentation: https://docs.gofiber.io

package fiber

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
)

// StartConfig is a struct to customize startup of Fiber.
//
// TODO: Add signal and timeout fields to use graceful-shutdown automatically.
type StartConfig struct {
	// Known networks are "tcp", "tcp4" (IPv4-only), "tcp6" (IPv6-only)
	// WARNING: When prefork is set to true, only "tcp4" and "tcp6" can be chose.
	//
	// Default: NetworkTCP4
	ListenerNetwork string `json:"listener_network"`

	// CertFile is a path of certficate file.
	// If you want to use TLS, you have to enter this field.
	//
	// Default : ""
	CertFile string `json:"cert_file"`

	// KeyFile is a path of certficate's private key.
	// If you want to use TLS, you have to enter this field.
	//
	// Default : ""
	CertKeyFile string `json:"cert_key_file"`

	// CertClientFile is a path of client certficate.
	// If you want to use mTLS, you have to enter this field.
	//
	// Default : ""
	CertClientFile string `json:"cert_client_file"`

	// GracefulSignals is a field to shutdown Fiber by given signals gracefully.
	//
	// Default: []os.Signal{os.Interrupt}
	GracefulSignals []os.Signal `json:"graceful_signals"`

	// GracefulTimeout is a max time to close requests before shutdowning the Fiber app.
	// If the time is exceeded process is exited.
	//
	// Default: 10 * time.Second
	GracefulTimeout time.Duration `json:"graceful_timeout"`

	// TLSConfigFunc allows customizing tls.Config as you want.
	//
	// Default: nil
	TLSConfigFunc func(tlsConfig *tls.Config) `json:"tls_config_func"`

	// ListenerFunc allows accessing and customizing net.Listener.
	//
	// Default: nil
	ListenerAddrFunc func(addr net.Addr) `json:"listener_addr_func"`

	// BeforeServeFunc allows customizing and accessing fiber app before serving the app.
	//
	// Default: nil
	BeforeServeFunc func(app *App) error `json:"before_serve_func"`

	// When set to true, it will not print out the «Fiber» ASCII art and listening address.
	//
	// Default: false
	DisableStartupMessage bool `json:"disable_startup_message"`

	// When set to true, this will spawn multiple Go processes listening on the same port.
	//
	// Default: false
	EnablePrefork bool `json:"enable_prefork"`

	// If set to true, will print all routes with their method, path and handler.
	//
	// Default: false
	EnablePrintRoutes bool `json:"enable_print_routes"`

	// OnShutdownError allows to customize error behavior when to graceful shutdown server by given signal.
	//
	// Default: Print error with log.Fatalf()
	OnShutdownError func(err error)
}

// startConfigDefault is a function to set default values of StartConfig.
func startConfigDefault(config ...StartConfig) StartConfig {
	if len(config) < 1 {
		return StartConfig{
			GracefulTimeout: 10 * time.Second,
			GracefulSignals: []os.Signal{os.Interrupt},
			ListenerNetwork: NetworkTCP4,
			OnShutdownError: func(err error) {
				log.Fatalf("shutdown: %v", err)
			},
		}
	}

	cfg := config[0]
	if cfg.GracefulTimeout == 0 {
		cfg.GracefulTimeout = 10 * time.Second
	}

	if len(cfg.GracefulSignals) < 1 {
		cfg.GracefulSignals = []os.Signal{os.Interrupt}
	}

	if cfg.ListenerNetwork == "" {
		cfg.ListenerNetwork = NetworkTCP4
	}

	if cfg.OnShutdownError == nil {
		cfg.OnShutdownError = func(err error) {
			log.Fatalf("shutdown: %v", err)
		}
	}

	return cfg
}

// Start serves HTTP requests from the given addr.
// You should enter custom StartConfig to customize startup. (TLS, mTLS, prefork...)
//
//  app.Start(":8080")
//  app.Start("127.0.0.1:8080")
//  app.Start(":8080", StartConfig{EnablePrefork: true})
func (app *App) Start(addr any, config ...StartConfig) error {
	cfg := startConfigDefault(config...)

	// Configure TLS
	var tlsConfig *tls.Config = nil
	if cfg.CertFile != "" && cfg.CertKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.CertKeyFile)
		if err != nil {
			return fmt.Errorf("tls: cannot load TLS key pair from certFile=%q and keyFile=%q: %s", cfg.CertFile, cfg.CertKeyFile, err)
		}

		tlsConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			Certificates: []tls.Certificate{
				cert,
			},
		}

		if cfg.CertClientFile != "" {
			clientCACert, err := os.ReadFile(filepath.Clean(cfg.CertClientFile))
			if err != nil {
				return err
			}

			clientCertPool := x509.NewCertPool()
			clientCertPool.AppendCertsFromPEM(clientCACert)

			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
			tlsConfig.ClientCAs = clientCertPool
		}
	}

	if cfg.TLSConfigFunc != nil {
		cfg.TLSConfigFunc(tlsConfig)
	}

	// Graceful shutdown
	/*ctx, cancel := signal.NotifyContext(context.Background(), cfg.GracefulSignals...)
	defer cancel()

	go app.gracefulShutdown(ctx, cfg)*/

	var ln net.Listener
	var err error

	switch addr := addr.(type) {
	case string:
		// Start prefork
		if cfg.EnablePrefork {
			return app.prefork(addr, tlsConfig, cfg)
		}

		// Configure Listener
		ln, err = app.createListener(addr, tlsConfig, cfg)
		if err != nil {
			return err
		}
	case net.Listener:
		// Prefork is supported for custom listeners
		if cfg.EnablePrefork {
			newAddr, tlsConfig := lnMetadata(cfg.ListenerNetwork, addr)

			return app.prefork(newAddr, tlsConfig, cfg)
		}

		ln = addr
	default:
		panic("start: invalid handler, you must use string or net.Listener as addr type")
	}

	// prepare the server for the start
	app.startupProcess()

	// Print startup message & routes
	app.printMessages(cfg, ln)

	// Serve
	if cfg.BeforeServeFunc != nil {
		if err := cfg.BeforeServeFunc(app); err != nil {
			return err
		}
	}

	return app.server.Serve(ln)
}

// Create listener function.
func (app *App) createListener(addr string, tlsConfig *tls.Config, cfg StartConfig) (net.Listener, error) {
	var listener net.Listener
	var err error

	if tlsConfig != nil {
		listener, err = tls.Listen(cfg.ListenerNetwork, addr, tlsConfig)
	} else {
		listener, err = net.Listen(cfg.ListenerNetwork, addr)
	}

	if cfg.ListenerAddrFunc != nil {
		cfg.ListenerAddrFunc(listener.Addr())
	}

	return listener, err
}

func (app *App) printMessages(cfg StartConfig, ln net.Listener) {
	// Print startup message
	if !cfg.DisableStartupMessage {
		app.startupMessage(ln.Addr().String(), getTlsConfig(ln) != nil, "", cfg)
	}

	// Print routes
	if cfg.EnablePrintRoutes {
		app.printRoutesMessage()
	}
}

// startupProcess Is the method which executes all the necessary processes just before the start of the server.
func (app *App) startupProcess() *App {
	if err := app.hooks.executeOnListenHooks(); err != nil {
		panic(err)
	}

	app.mutex.Lock()
	app.buildTree()
	app.mutex.Unlock()

	return app
}

// startupMessage prepares the startup message with the handler number, port, address and other information
func (app *App) startupMessage(addr string, tls bool, pids string, cfg StartConfig) {
	// ignore child processes
	if IsChild() {
		return
	}

	const (
		cBlack = "\u001b[90m"
		cCyan  = "\u001b[96m"
		cReset = "\u001b[0m"
	)

	value := func(s string, width int) string {
		pad := width - len(s)
		str := ""
		for i := 0; i < pad; i++ {
			str += "."
		}
		if s == "Disabled" {
			str += " " + s
		} else {
			str += fmt.Sprintf(" %s%s%s", cCyan, s, cBlack)
		}
		return str
	}

	center := func(s string, width int) string {
		pad := strconv.Itoa((width - len(s)) / 2)
		str := fmt.Sprintf("%"+pad+"s", " ")
		str += s
		str += fmt.Sprintf("%"+pad+"s", " ")
		if len(str) < width {
			str += " "
		}
		return str
	}

	centerValue := func(s string, width int) string {
		pad := strconv.Itoa((width - len(s)) / 2)
		str := fmt.Sprintf("%"+pad+"s", " ")
		str += fmt.Sprintf("%s%s%s", cCyan, s, cBlack)
		str += fmt.Sprintf("%"+pad+"s", " ")
		if len(str)-10 < width {
			str += " "
		}
		return str
	}

	pad := func(s string, width int) (str string) {
		toAdd := width - len(s)
		str += s
		for i := 0; i < toAdd; i++ {
			str += " "
		}
		return
	}

	host, port := parseAddr(addr)
	if host == "" {
		if cfg.ListenerNetwork == NetworkTCP6 {
			host = "[::1]"
		} else {
			host = "0.0.0.0"
		}
	}

	scheme := "http"
	if tls {
		scheme = "https"
	}

	isPrefork := "Disabled"
	if cfg.EnablePrefork {
		isPrefork = "Enabled"
	}

	procs := strconv.Itoa(runtime.GOMAXPROCS(0))
	if !cfg.EnablePrefork {
		procs = "1"
	}

	mainLogo := cBlack + " ┌───────────────────────────────────────────────────┐\n"
	if app.config.AppName != "" {
		mainLogo += " │ " + centerValue(app.config.AppName, 49) + " │\n"
	}
	mainLogo += " │ " + centerValue(" Fiber v"+Version, 49) + " │\n"

	if host == "0.0.0.0" {
		mainLogo +=
			" │ " + center(fmt.Sprintf("%s://127.0.0.1:%s", scheme, port), 49) + " │\n" +
				" │ " + center(fmt.Sprintf("(bound on host 0.0.0.0 and port %s)", port), 49) + " │\n"
	} else {
		mainLogo +=
			" │ " + center(fmt.Sprintf("%s://%s:%s", scheme, host, port), 49) + " │\n"
	}

	mainLogo += fmt.Sprintf(
		" │                                                   │\n"+
			" │ Handlers %s  Processes %s │\n"+
			" │ Prefork .%s  PID ....%s │\n"+
			" └───────────────────────────────────────────────────┘"+
			cReset,
		value(strconv.Itoa(int(app.handlersCount)), 14), value(procs, 12),
		value(isPrefork, 14), value(strconv.Itoa(os.Getpid()), 14),
	)

	var childPidsLogo string
	if cfg.EnablePrefork {
		var childPidsTemplate string
		childPidsTemplate += "%s"
		childPidsTemplate += " ┌───────────────────────────────────────────────────┐\n%s"
		childPidsTemplate += " └───────────────────────────────────────────────────┘"
		childPidsTemplate += "%s"

		newLine := " │ %s%s%s │"

		// Turn the `pids` variable (in the form ",a,b,c,d,e,f,etc") into a slice of PIDs
		var pidSlice []string
		for _, v := range strings.Split(pids, ",") {
			if v != "" {
				pidSlice = append(pidSlice, v)
			}
		}

		var lines []string
		thisLine := "Child PIDs ... "
		var itemsOnThisLine []string

		addLine := func() {
			lines = append(lines,
				fmt.Sprintf(
					newLine,
					cBlack,
					thisLine+cCyan+pad(strings.Join(itemsOnThisLine, ", "), 49-len(thisLine)),
					cBlack,
				),
			)
		}

		for _, pid := range pidSlice {
			if len(thisLine+strings.Join(append(itemsOnThisLine, pid), ", ")) > 49 {
				addLine()
				thisLine = ""
				itemsOnThisLine = []string{pid}
			} else {
				itemsOnThisLine = append(itemsOnThisLine, pid)
			}
		}

		// Add left over items to their own line
		if len(itemsOnThisLine) != 0 {
			addLine()
		}

		// Form logo
		childPidsLogo = fmt.Sprintf(childPidsTemplate,
			cBlack,
			strings.Join(lines, "\n")+"\n",
			cReset,
		)
	}

	// Combine both the child PID logo and the main Fiber logo

	// Pad the shorter logo to the length of the longer one
	splitMainLogo := strings.Split(mainLogo, "\n")
	splitChildPidsLogo := strings.Split(childPidsLogo, "\n")

	mainLen := len(splitMainLogo)
	childLen := len(splitChildPidsLogo)

	if mainLen > childLen {
		diff := mainLen - childLen
		for i := 0; i < diff; i++ {
			splitChildPidsLogo = append(splitChildPidsLogo, "")
		}
	} else {
		diff := childLen - mainLen
		for i := 0; i < diff; i++ {
			splitMainLogo = append(splitMainLogo, "")
		}
	}

	// Combine the two logos, line by line
	output := "\n"
	for i := range splitMainLogo {
		output += cBlack + splitMainLogo[i] + " " + splitChildPidsLogo[i] + "\n"
	}

	out := colorable.NewColorableStdout()
	if os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") == "1" || (!isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd())) {
		out = colorable.NewNonColorable(os.Stdout)
	}

	_, _ = fmt.Fprintln(out, output)
}

// printRoutesMessage print all routes with method, path, name and handlers
// in a format of table, like this:
// method | path | name      | handlers
// GET    | /    | routeName | github.com/gofiber/fiber/v3.emptyHandler
// HEAD   | /    |           | github.com/gofiber/fiber/v3.emptyHandler
func (app *App) printRoutesMessage() {
	// ignore child processes
	if IsChild() {
		return
	}

	const (
		cCyan   = "\u001b[96m"
		cGreen  = "\u001b[92m"
		cYellow = "\u001b[93m"
		cBlue   = "\u001b[94m"
		cWhite  = "\u001b[97m"
	)
	var routes []RouteMessage
	for _, routeStack := range app.stack {
		for _, route := range routeStack {
			var newRoute = RouteMessage{}
			newRoute.name = route.Name
			newRoute.method = route.Method
			newRoute.path = route.Path
			for _, handler := range route.Handlers {
				newRoute.handlers += runtime.FuncForPC(reflect.ValueOf(handler).Pointer()).Name() + " "
			}
			routes = append(routes, newRoute)
		}
	}

	out := colorable.NewColorableStdout()
	if os.Getenv("TERM") == "dumb" || os.Getenv("NO_COLOR") == "1" || (!isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd())) {
		out = colorable.NewNonColorable(os.Stdout)
	}

	w := tabwriter.NewWriter(out, 1, 1, 1, ' ', 0)
	// Sort routes by path
	sort.Slice(routes, func(i, j int) bool {
		return routes[i].path < routes[j].path
	})
	_, _ = fmt.Fprintf(w, "%smethod\t%s| %spath\t%s| %sname\t%s| %shandlers\n", cBlue, cWhite, cGreen, cWhite, cCyan, cWhite, cYellow)
	_, _ = fmt.Fprintf(w, "%s------\t%s| %s----\t%s| %s----\t%s| %s--------\n", cBlue, cWhite, cGreen, cWhite, cCyan, cWhite, cYellow)
	for _, route := range routes {
		_, _ = fmt.Fprintf(w, "%s%s\t%s| %s%s\t%s| %s%s\t%s| %s%s\n", cBlue, route.method, cWhite, cGreen, route.path, cWhite, cCyan, route.name, cWhite, cYellow, route.handlers)
	}

	_ = w.Flush()
}

func (app *App) gracefulShutdown(ctx context.Context, cfg StartConfig) {
	<-ctx.Done()

	timeoutCtx, cancel := context.WithTimeout(context.Background(), cfg.GracefulTimeout)
	defer cancel()

	select {
	case <-timeoutCtx.Done():
		if cfg.OnShutdownError != nil {
			cfg.OnShutdownError(ErrGracefulTimeout)
		}
		os.Exit(1)
	default:
		if err := app.Shutdown(); err != nil && cfg.OnShutdownError != nil {
			cfg.OnShutdownError(err)
		}
		os.Exit(0)
	}
}