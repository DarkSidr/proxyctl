package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/spf13/cobra"

	"proxyctl/internal/config"
)

const applyServiceUnit = "proxyctl-apply.service"

type dbSnapshot struct {
	Path          string
	Exists        bool
	Initialized   bool
	MissingTables []string
	Users         int
	Nodes         int
	Inbounds      int
}

type runtimeFileStatus struct {
	Label    string
	Path     string
	Exists   bool
	IsDir    bool
	Required bool
}

type unitStatus struct {
	Unit       string
	LoadState  string
	Active     string
	SubState   string
	ErrMessage string
}

type doctorLevel string

const (
	doctorLevelOK      doctorLevel = "OK"
	doctorLevelWarning doctorLevel = "WARN"
	doctorLevelError   doctorLevel = "ERROR"
)

type doctorFinding struct {
	Level   doctorLevel
	Check   string
	Message string
}

type logsOptions struct {
	Lines  int
	Follow bool
	Since  string
	Until  string
}

func newStatusCmd(configPath, dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show operational status",
		Long:  "Displays DB/runtime/systemd health, selected reverse proxy, and entity counters.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			resolvedDBPath := resolveDBPath(cmd, cfg, *dbPath)

			dbState, err := inspectDB(resolvedDBPath)
			if err != nil {
				return fmt.Errorf("inspect db state: %w", err)
			}

			fileStates := runtimeFileStatuses(cfg)
			unitNames := []string{
				cfg.Runtime.SingBoxUnit,
				cfg.Runtime.XrayUnit,
				cfg.Runtime.CaddyUnit,
				cfg.Runtime.NginxUnit,
			}
			units := collectUnitStatuses(cmd.Context(), unitNames)

			fmt.Fprintf(cmd.OutOrStdout(), "reverse_proxy: %s\n", cfg.ReverseProxy)
			fmt.Fprintln(cmd.OutOrStdout(), "")

			printDBSection(cmd.OutOrStdout(), dbState)
			fmt.Fprintln(cmd.OutOrStdout(), "")
			printRuntimeFilesSection(cmd.OutOrStdout(), fileStates, cfg.ReverseProxy)
			fmt.Fprintln(cmd.OutOrStdout(), "")
			printSystemdSection(cmd.OutOrStdout(), units, cfg)
			fmt.Fprintln(cmd.OutOrStdout(), "")
			printCountersSection(cmd.OutOrStdout(), dbState)
			return nil
		},
	}
}

func newLogsCmd(configPath *string) *cobra.Command {
	opts := logsOptions{Lines: 100}

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Inspect service logs",
		Long:  "Reads journalctl logs for runtime/apply services used by proxyctl.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(cmd, *configPath, "runtime", opts)
		},
	}
	cmd.Flags().IntVarP(&opts.Lines, "lines", "n", 100, "Number of log lines")
	cmd.Flags().BoolVarP(&opts.Follow, "follow", "f", false, "Follow logs")
	cmd.Flags().StringVar(&opts.Since, "since", "", "Show logs since this time (journalctl format)")
	cmd.Flags().StringVar(&opts.Until, "until", "", "Show logs until this time (journalctl format)")

	runtimeSub := &cobra.Command{
		Use:   "runtime",
		Short: "Show runtime service logs",
		Long:  "Shows logs for sing-box/xray and selected reverse proxy services.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(cmd, *configPath, "runtime", opts)
		},
	}
	applySub := &cobra.Command{
		Use:   "apply",
		Short: "Show apply pipeline logs",
		Long:  "Shows logs for apply pipeline service unit (if configured).",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogs(cmd, *configPath, "apply", opts)
		},
	}
	cmd.AddCommand(runtimeSub, applySub)
	return cmd
}

func newDoctorCmd(configPath, dbPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Run operational diagnostics",
		Long:  "Checks common runtime/systemd/database issues and prints actionable findings.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(*configPath)
			if err != nil {
				return err
			}
			resolvedDBPath := resolveDBPath(cmd, cfg, *dbPath)

			findings, hasErrors, err := runDoctorChecks(cmd.Context(), cfg, resolvedDBPath)
			if err != nil {
				return err
			}

			for _, finding := range findings {
				fmt.Fprintf(cmd.OutOrStdout(), "[%s] %s: %s\n", finding.Level, finding.Check, finding.Message)
			}

			if hasErrors {
				return fmt.Errorf("doctor found blocking issues")
			}
			return nil
		},
	}
}

func runLogs(cmd *cobra.Command, configPath, scope string, opts logsOptions) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	units := logsUnits(cfg, scope)
	if len(units) == 0 {
		return fmt.Errorf("no units resolved for logs scope %q", scope)
	}

	if opts.Lines <= 0 {
		opts.Lines = 100
	}

	args := []string{"--no-pager", "-n", strconv.Itoa(opts.Lines)}
	if strings.TrimSpace(opts.Since) != "" {
		args = append(args, "--since", strings.TrimSpace(opts.Since))
	}
	if strings.TrimSpace(opts.Until) != "" {
		args = append(args, "--until", strings.TrimSpace(opts.Until))
	}
	for _, unit := range units {
		args = append(args, "-u", unit)
	}
	if opts.Follow {
		args = append(args, "-f")
	}

	ctl := exec.CommandContext(cmd.Context(), "journalctl", args...)
	ctl.Stdout = cmd.OutOrStdout()
	ctl.Stderr = cmd.ErrOrStderr()
	if err := ctl.Run(); err != nil {
		return fmt.Errorf("journalctl failed: %w", err)
	}
	return nil
}

func logsUnits(cfg config.AppConfig, scope string) []string {
	switch scope {
	case "apply":
		return []string{applyServiceUnit}
	case "runtime":
		units := []string{
			cfg.Runtime.SingBoxUnit,
			cfg.Runtime.XrayUnit,
		}
		if cfg.ReverseProxy == config.ReverseProxyNginx {
			units = append(units, cfg.Runtime.NginxUnit)
		} else {
			units = append(units, cfg.Runtime.CaddyUnit)
		}
		return compactUnique(units)
	default:
		return nil
	}
}

func runDoctorChecks(ctx context.Context, cfg config.AppConfig, dbPath string) ([]doctorFinding, bool, error) {
	findings := make([]doctorFinding, 0)
	hasErrors := false

	add := func(level doctorLevel, check, message string) {
		findings = append(findings, doctorFinding{
			Level:   level,
			Check:   check,
			Message: message,
		})
		if level == doctorLevelError {
			hasErrors = true
		}
	}

	for _, p := range validateConfiguredPaths(cfg, dbPath) {
		add(p.Level, "paths", p.Message)
	}

	dbState, err := inspectDB(dbPath)
	if err != nil {
		return nil, false, fmt.Errorf("inspect db state: %w", err)
	}
	if !dbState.Exists {
		add(doctorLevelError, "database", fmt.Sprintf("database file is missing: %s", dbPath))
	} else if !dbState.Initialized {
		add(doctorLevelError, "database", fmt.Sprintf("database is not initialized; missing tables: %s", strings.Join(dbState.MissingTables, ", ")))
	} else {
		add(doctorLevelOK, "database", fmt.Sprintf("database initialized at %s", dbPath))
	}

	if dbState.Initialized {
		if dbState.Users == 0 {
			add(doctorLevelWarning, "users", "no users found")
		} else {
			add(doctorLevelOK, "users", fmt.Sprintf("users configured: %d", dbState.Users))
		}
	}

	fileStates := runtimeFileStatuses(cfg)
	for _, fs := range fileStates {
		if fs.Required && (!fs.Exists || fs.IsDir) {
			add(doctorLevelError, "runtime-file", fmt.Sprintf("%s is missing or invalid: %s", fs.Label, fs.Path))
		}
	}

	reverseProxyPath := reverseProxyRuntimePath(cfg)
	if reverseProxyPath == "" {
		add(doctorLevelError, "reverse-proxy", "reverse proxy selection is invalid")
	} else if !isRegularFile(reverseProxyPath) {
		add(doctorLevelError, "reverse-proxy", fmt.Sprintf("reverse proxy config is missing: %s", reverseProxyPath))
	} else {
		add(doctorLevelOK, "reverse-proxy", fmt.Sprintf("%s config detected: %s", cfg.ReverseProxy, reverseProxyPath))
	}

	unitStatuses := collectUnitStatuses(ctx, []string{
		cfg.Runtime.SingBoxUnit,
		cfg.Runtime.XrayUnit,
		cfg.Runtime.CaddyUnit,
		cfg.Runtime.NginxUnit,
	})
	for _, us := range unitStatuses {
		if us.ErrMessage != "" {
			add(doctorLevelWarning, "systemd", fmt.Sprintf("%s status unavailable: %s", us.Unit, us.ErrMessage))
			continue
		}
		if us.LoadState == "not-found" {
			add(doctorLevelWarning, "systemd", fmt.Sprintf("%s unit not found", us.Unit))
		}
	}

	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Level != findings[j].Level {
			return severityOrder(findings[i].Level) < severityOrder(findings[j].Level)
		}
		if findings[i].Check != findings[j].Check {
			return findings[i].Check < findings[j].Check
		}
		return findings[i].Message < findings[j].Message
	})
	return findings, hasErrors, nil
}

func severityOrder(level doctorLevel) int {
	switch level {
	case doctorLevelError:
		return 0
	case doctorLevelWarning:
		return 1
	default:
		return 2
	}
}

func validateConfiguredPaths(cfg config.AppConfig, dbPath string) []doctorFinding {
	type pathSpec struct {
		Label  string
		Path   string
		IsDir  bool
		IsFile bool
	}

	specs := []pathSpec{
		{Label: "config_dir", Path: cfg.Paths.ConfigDir, IsDir: true},
		{Label: "runtime_dir", Path: cfg.Paths.RuntimeDir, IsDir: true},
		{Label: "caddy_dir", Path: cfg.Paths.CaddyDir, IsDir: true},
		{Label: "nginx_dir", Path: cfg.Paths.NginxDir, IsDir: true},
		{Label: "decoy_site_dir", Path: cfg.Paths.DecoySiteDir, IsDir: true},
		{Label: "state_dir", Path: cfg.Paths.StateDir, IsDir: true},
		{Label: "backups_dir", Path: cfg.Paths.BackupsDir, IsDir: true},
		{Label: "subscriptions_dir", Path: cfg.Paths.Subscription, IsDir: true},
		{Label: "config_file", Path: cfg.Paths.ConfigFile, IsFile: true},
		{Label: "sqlite_path", Path: dbPath, IsFile: true},
	}

	result := make([]doctorFinding, 0)
	for _, spec := range specs {
		p := strings.TrimSpace(spec.Path)
		if p == "" {
			result = append(result, doctorFinding{
				Level:   doctorLevelError,
				Message: fmt.Sprintf("%s is empty", spec.Label),
			})
			continue
		}
		if !filepath.IsAbs(p) {
			result = append(result, doctorFinding{
				Level:   doctorLevelError,
				Message: fmt.Sprintf("%s must be absolute: %s", spec.Label, p),
			})
			continue
		}

		info, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if spec.IsDir {
					result = append(result, doctorFinding{
						Level:   doctorLevelWarning,
						Message: fmt.Sprintf("%s does not exist: %s", spec.Label, p),
					})
				}

				parent := filepath.Dir(p)
				if _, statErr := os.Stat(parent); statErr != nil {
					result = append(result, doctorFinding{
						Level:   doctorLevelError,
						Message: fmt.Sprintf("%s parent directory is missing: %s", spec.Label, parent),
					})
				}
				continue
			}
			result = append(result, doctorFinding{
				Level:   doctorLevelError,
				Message: fmt.Sprintf("cannot access %s (%s): %v", spec.Label, p, err),
			})
			continue
		}

		if spec.IsDir && !info.IsDir() {
			result = append(result, doctorFinding{
				Level:   doctorLevelError,
				Message: fmt.Sprintf("%s must be a directory: %s", spec.Label, p),
			})
			continue
		}
		if spec.IsFile && info.IsDir() {
			result = append(result, doctorFinding{
				Level:   doctorLevelError,
				Message: fmt.Sprintf("%s must be a file: %s", spec.Label, p),
			})
			continue
		}

		result = append(result, doctorFinding{
			Level:   doctorLevelOK,
			Message: fmt.Sprintf("%s: %s", spec.Label, p),
		})
	}

	return result
}

func inspectDB(path string) (dbSnapshot, error) {
	snapshot := dbSnapshot{Path: path}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return snapshot, nil
		}
		return snapshot, fmt.Errorf("stat db path %q: %w", path, err)
	}
	if info.IsDir() {
		return snapshot, fmt.Errorf("db path points to directory: %s", path)
	}

	snapshot.Exists = true
	dsn := fmt.Sprintf("file:%s?mode=ro", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return snapshot, fmt.Errorf("open sqlite db %q: %w", path, err)
	}
	defer db.Close()

	required := []string{"users", "nodes", "inbounds", "credentials", "subscriptions"}
	missing := make([]string, 0)
	for _, table := range required {
		exists, tableErr := sqliteTableExists(db, table)
		if tableErr != nil {
			return snapshot, fmt.Errorf("check table %q: %w", table, tableErr)
		}
		if !exists {
			missing = append(missing, table)
		}
	}
	if len(missing) > 0 {
		snapshot.MissingTables = missing
		return snapshot, nil
	}
	snapshot.Initialized = true

	userCount, err := sqliteCount(db, "users")
	if err != nil {
		return snapshot, err
	}
	nodeCount, err := sqliteCount(db, "nodes")
	if err != nil {
		return snapshot, err
	}
	inboundCount, err := sqliteCount(db, "inbounds")
	if err != nil {
		return snapshot, err
	}
	snapshot.Users = userCount
	snapshot.Nodes = nodeCount
	snapshot.Inbounds = inboundCount
	return snapshot, nil
}

func sqliteTableExists(db *sql.DB, table string) (bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func sqliteCount(db *sql.DB, table string) (int, error) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM %s", table)
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return n, nil
}

func runtimeFileStatuses(cfg config.AppConfig) []runtimeFileStatus {
	files := []runtimeFileStatus{
		{Label: "sing-box runtime config", Path: filepath.Join(cfg.Paths.RuntimeDir, "sing-box.json"), Required: true},
		{Label: "xray runtime config", Path: filepath.Join(cfg.Paths.RuntimeDir, "xray.json"), Required: true},
	}

	switch cfg.ReverseProxy {
	case config.ReverseProxyNginx:
		files = append(files, runtimeFileStatus{
			Label:    "nginx runtime config",
			Path:     filepath.Join(cfg.Paths.NginxDir, "nginx.conf"),
			Required: true,
		})
	case config.ReverseProxyCaddy:
		files = append(files, runtimeFileStatus{
			Label:    "caddy runtime config",
			Path:     filepath.Join(cfg.Paths.CaddyDir, "Caddyfile"),
			Required: true,
		})
	}

	for i := range files {
		info, err := os.Stat(files[i].Path)
		if err != nil {
			continue
		}
		files[i].Exists = true
		files[i].IsDir = info.IsDir()
	}

	return files
}

func reverseProxyRuntimePath(cfg config.AppConfig) string {
	if cfg.ReverseProxy == config.ReverseProxyNginx {
		return filepath.Join(cfg.Paths.NginxDir, "nginx.conf")
	}
	if cfg.ReverseProxy == config.ReverseProxyCaddy {
		return filepath.Join(cfg.Paths.CaddyDir, "Caddyfile")
	}
	return ""
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func collectUnitStatuses(ctx context.Context, units []string) []unitStatus {
	trimmed := compactUnique(units)
	statuses := make([]unitStatus, 0, len(trimmed))
	for _, unit := range trimmed {
		statuses = append(statuses, getUnitStatus(ctx, unit))
	}
	sort.Slice(statuses, func(i, j int) bool { return statuses[i].Unit < statuses[j].Unit })
	return statuses
}

func getUnitStatus(ctx context.Context, unit string) unitStatus {
	state := unitStatus{Unit: unit}
	if strings.TrimSpace(unit) == "" {
		state.ErrMessage = "empty unit name"
		return state
	}

	callCtx := ctx
	if callCtx == nil {
		callCtx = context.Background()
	}
	timeoutCtx, cancel := context.WithTimeout(callCtx, 4*time.Second)
	defer cancel()

	ctl := exec.CommandContext(timeoutCtx, "systemctl", "show", unit, "--property=LoadState,ActiveState,SubState")
	out, err := ctl.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		state.ErrMessage = msg
		return state
	}

	lines := strings.Split(string(out), "\n")
	for _, rawLine := range lines {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := parts[0]
		value := parts[1]
		switch key {
		case "LoadState":
			state.LoadState = value
		case "ActiveState":
			state.Active = value
		case "SubState":
			state.SubState = value
		}
	}
	return state
}

func compactUnique(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func printDBSection(out io.Writer, dbState dbSnapshot) {
	fmt.Fprintln(out, "database:")
	fmt.Fprintf(out, "  path: %s\n", dbState.Path)
	if !dbState.Exists {
		fmt.Fprintln(out, "  file: missing")
		fmt.Fprintln(out, "  initialized: no")
		return
	}
	fmt.Fprintln(out, "  file: present")
	if dbState.Initialized {
		fmt.Fprintln(out, "  initialized: yes")
		return
	}
	fmt.Fprintf(out, "  initialized: no (missing tables: %s)\n", strings.Join(dbState.MissingTables, ", "))
}

func printRuntimeFilesSection(out io.Writer, files []runtimeFileStatus, reverseProxy config.ReverseProxyEngine) {
	fmt.Fprintf(out, "runtime files (reverse proxy: %s):\n", reverseProxy)
	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "COMPONENT\tSTATUS\tPATH")
	for _, f := range files {
		status := "missing"
		if f.Exists && !f.IsDir {
			status = "present"
		}
		if f.Exists && f.IsDir {
			status = "invalid (directory)"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", f.Label, status, f.Path)
	}
	_ = w.Flush()
}

func printSystemdSection(out io.Writer, units []unitStatus, cfg config.AppConfig) {
	fmt.Fprintln(out, "systemd services:")
	selectedProxyUnit := cfg.Runtime.CaddyUnit
	if cfg.ReverseProxy == config.ReverseProxyNginx {
		selectedProxyUnit = cfg.Runtime.NginxUnit
	}

	w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "UNIT\tLOAD\tACTIVE\tSUB\tNOTE")
	for _, unit := range units {
		note := ""
		if unit.Unit == selectedProxyUnit {
			note = "selected reverse proxy"
		}
		if unit.ErrMessage != "" {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", unit.Unit, "unknown", "unknown", "unknown", strings.TrimSpace(unit.ErrMessage))
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", unit.Unit, unit.LoadState, unit.Active, unit.SubState, note)
	}
	_ = w.Flush()
}

func printCountersSection(out io.Writer, dbState dbSnapshot) {
	fmt.Fprintln(out, "counts:")
	if !dbState.Exists || !dbState.Initialized {
		fmt.Fprintln(out, "  users: n/a")
		fmt.Fprintln(out, "  nodes: n/a")
		fmt.Fprintln(out, "  inbounds: n/a")
		return
	}
	fmt.Fprintf(out, "  users: %d\n", dbState.Users)
	fmt.Fprintf(out, "  nodes: %d\n", dbState.Nodes)
	fmt.Fprintf(out, "  inbounds: %d\n", dbState.Inbounds)
}
