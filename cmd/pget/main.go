package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/andrewbytecoder/kget"
	"github.com/spf13/cobra"
)

var version string

func main() {
	var (
		kcpAddr    string
		remotePath string
		out        string
		procs      int
		logLevel   string
	)

	rootCmd := &cobra.Command{
		Use:   "pget",
		Short: "Resumable parallel downloader over KCP",
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

	getCmd := &cobra.Command{
		Use:   "get",
		Short: "Download a file from KCP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			if kcpAddr == "" {
				return fmt.Errorf("--kcp is required")
			}
			if remotePath == "" {
				return fmt.Errorf("--path is required")
			}
			if procs <= 0 {
				procs = 1
			}

			ctx := context.Background()
			target, err := pget.CheckKCP(ctx, kcpAddr, remotePath)
			if err != nil {
				return err
			}

			filename := target.Filename
			dirname := ""
			if out != "" {
				// If out is a directory, keep filename. Otherwise split.
				if fi, err := os.Stat(out); err == nil && fi.IsDir() {
					dirname = out
				} else {
					dirname, filename = filepath.Split(out)
					if dirname != "" {
						if err := os.MkdirAll(dirname, 0755); err != nil {
							return err
						}
					}
				}
			}

			return pget.Download(ctx, &pget.DownloadConfig{
				Filename:      filename,
				Dirname:       dirname,
				ContentLength: target.ContentLength,
				Procs:         procs,
				KCPAddr:       kcpAddr,
				RemotePath:    target.RemotePath,
			}, pget.WithUserAgent("", version))
		},
	}

	getCmd.Flags().StringVar(&kcpAddr, "kcp", "", "KCP server address host:port")
	getCmd.Flags().StringVar(&remotePath, "path", "", "Remote file path (relative to server --root)")
	getCmd.Flags().StringVarP(&out, "output", "o", "", "Output file path or directory")
	getCmd.Flags().IntVarP(&procs, "procs", "p", 4, "Number of parallel connections")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Log level: debug|info|warn|error")

	rootCmd.AddCommand(getCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error:\n  %v\n", err)
		os.Exit(1)
	}
}
