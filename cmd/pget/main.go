package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	kget "github.com/andrewbytecoder/kget"
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
			kget.SetLogger(slog.New(h))
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
			target, err := kget.CheckKCP(ctx, kcpAddr, remotePath)
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

			return kget.Download(ctx, &kget.DownloadConfig{
				Filename:      filename,
				Dirname:       dirname,
				ContentLength: target.ContentLength,
				Procs:         procs,
				KCPAddr:       kcpAddr,
				RemotePath:    target.RemotePath,
			}, kget.WithUserAgent("", version))
		},
	}

	getCmd.Flags().StringVar(&kcpAddr, "kcp", "", "KCP server address host:port")
	getCmd.Flags().StringVar(&remotePath, "path", "", "Remote file path (relative to server --root)")
	getCmd.Flags().StringVarP(&out, "output", "o", "", "Output file path or directory")
	getCmd.Flags().IntVarP(&procs, "procs", "p", 4, "Number of parallel connections")
	rootCmd.PersistentFlags().StringVar(&logLevel, "log-level", "info", "Log level: debug|info|warn|error")

	callCmd := newCallCmd()
	alertCmd := newAlertCmd()

	rootCmd.AddCommand(getCmd, callCmd, alertCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error:\n  %v\n", err)
		os.Exit(1)
	}
}

func newCallCmd() *cobra.Command {
	var (
		kcpAddr  string
		method   string
		path     string
		data     string
		headers  []string
		showBody bool
	)

	cmd := &cobra.Command{
		Use:   "call",
		Short: "Send a RESTful control request over KCP",
		RunE: func(cmd *cobra.Command, args []string) error {
			if kcpAddr == "" {
				return fmt.Errorf("--kcp is required")
			}
			if path == "" {
				return fmt.Errorf("--path is required")
			}
			hm := map[string]string{}
			for _, h := range headers {
				parts := strings.SplitN(h, ":", 2)
				if len(parts) != 2 {
					return fmt.Errorf("invalid header: %q (want Key:Value)", h)
				}
				hm[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
			}

			var body []byte
			if data != "" {
				if strings.HasPrefix(data, "@") {
					b, err := os.ReadFile(strings.TrimPrefix(data, "@"))
					if err != nil {
						return err
					}
					body = b
				} else {
					body = []byte(data)
				}
			}

			cc := &kget.ControlClient{Addr: kcpAddr}
			resp, err := cc.Do(context.Background(), method, path, hm, body)
			if err != nil {
				return err
			}

			fmt.Fprintf(os.Stdout, "%d\n", resp.Status)
			if showBody && resp.BodyB64 != "" {
				b, _ := base64.StdEncoding.DecodeString(resp.BodyB64)
				_, _ = os.Stdout.Write(b)
				if len(b) > 0 && b[len(b)-1] != '\n' {
					_, _ = io.WriteString(os.Stdout, "\n")
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&kcpAddr, "kcp", "", "KCP server address host:port")
	cmd.Flags().StringVar(&method, "method", "GET", "HTTP method")
	cmd.Flags().StringVar(&path, "path", "", "Request path, e.g. /healthz")
	cmd.Flags().StringVar(&data, "data", "", "Body string or @file")
	cmd.Flags().StringArrayVar(&headers, "header", nil, "Header Key:Value (repeatable)")
	cmd.Flags().BoolVar(&showBody, "print-body", true, "Print response body to stdout")
	return cmd
}

func newAlertCmd() *cobra.Command {
	var (
		kcpAddr string
		file    string
		jsonStr string
	)
	cmd := &cobra.Command{
		Use:   "alert",
		Short: "Send Alertmanager webhook JSON over KCP",
		RunE: func(cmd *cobra.Command, args []string) error {
			if kcpAddr == "" {
				return fmt.Errorf("--kcp is required")
			}
			var body []byte
			switch {
			case file != "":
				b, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				body = b
			case jsonStr != "":
				body = []byte(jsonStr)
			default:
				return fmt.Errorf("either --file or --json is required")
			}
			// validate json
			var tmp any
			if err := json.Unmarshal(body, &tmp); err != nil {
				return fmt.Errorf("invalid json: %w", err)
			}
			cc := &kget.ControlClient{Addr: kcpAddr}
			return cc.SendAlertmanager(context.Background(), body)
		},
	}
	cmd.Flags().StringVar(&kcpAddr, "kcp", "", "KCP server address host:port")
	cmd.Flags().StringVar(&file, "file", "", "Path to Alertmanager webhook JSON file")
	cmd.Flags().StringVar(&jsonStr, "json", "", "Alertmanager webhook JSON string")
	return cmd
}
