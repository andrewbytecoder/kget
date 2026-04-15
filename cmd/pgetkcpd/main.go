package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/andrewbytecoder/kget"
	"github.com/spf13/cobra"
)

func main() {
	var listen, root string
	var logLevel string

	rootCmd := &cobra.Command{
		Use:   "pgetkcpd",
		Short: "KCP file server for pget",
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			level := slog.LevelInfo
			switch strings.ToLower(logLevel) {
			case "debug":
				level = slog.LevelDebug
			case "info":
				level = slog.LevelInfo
			case "warn", "warning":
				level = slog.LevelWarn
			case "error":
				level = slog.LevelError
			default:
				return fmt.Errorf("invalid --log-level: %s", logLevel)
			}
			h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
			pget.SetLogger(slog.New(h))
			return nil
		},
	}

	serveCmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve files over KCP",
		RunE: func(cmd *cobra.Command, args []string) error {
			srv := &pget.KCPServer{RootDir: root}
			fmt.Fprintf(os.Stdout, "pgetkcpd listening on %s (root=%s)\n", listen, root)
			return srv.ListenAndServe(listen)
		},
	}
	serveCmd.Flags().StringVar(&listen, "listen", ":29900", "KCP listen address (host:port)")
	serveCmd.Flags().StringVar(&root, "root", ".", "root directory to serve")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Log level: debug|info|warn|error")

	rootCmd.AddCommand(serveCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "pgetkcpd error: %v\n", err)
		os.Exit(1)
	}
}
