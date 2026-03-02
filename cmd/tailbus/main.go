package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func short(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func main() {
	socketPath := flag.String("socket", "/tmp/tailbusd.sock", "daemon Unix socket path")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Usage: tailbus [command] [args...]")
		fmt.Println("Commands: register, introspect, list, open, send, subscribe, resolve, sessions, dashboard, trace, agent")
		os.Exit(1)
	}

	conn, err := grpc.NewClient("unix://"+*socketPath, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		logger.Error("failed to connect to daemon", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	client := agentpb.NewAgentAPIClient(conn)
	ctx := context.Background()

	switch args[0] {
	case "register":
		if len(args) < 2 {
			fmt.Println("Usage: tailbus register <handle>")
			os.Exit(1)
		}
		resp, err := client.Register(ctx, &agentpb.RegisterRequest{Handle: args[1]})
		if err != nil {
			logger.Error("register failed", "error", err)
			os.Exit(1)
		}
		if !resp.Ok {
			fmt.Printf("Registration failed: %s\n", resp.Error)
			os.Exit(1)
		}
		fmt.Printf("Registered as %q\n", args[1])

	case "introspect":
		if len(args) < 2 {
			fmt.Println("Usage: tailbus introspect <handle>")
			os.Exit(1)
		}
		resp, err := client.IntrospectHandle(ctx, &agentpb.IntrospectHandleRequest{Handle: args[1]})
		if err != nil {
			logger.Error("introspect failed", "error", err)
			os.Exit(1)
		}
		if !resp.Found {
			fmt.Printf("Handle %q not found\n", args[1])
			os.Exit(1)
		}
		fmt.Printf("Handle: %s\n", resp.Handle)
		if resp.Manifest != nil {
			m := resp.Manifest
			if m.Description != "" {
				fmt.Printf("Description: %s\n", m.Description)
			}
			if m.Version != "" {
				fmt.Printf("Version: %s\n", m.Version)
			}
			if len(m.Tags) > 0 {
				fmt.Printf("Tags: %s\n", strings.Join(m.Tags, ", "))
			}
			if len(m.Commands) > 0 {
				fmt.Println("Commands:")
				for _, c := range m.Commands {
					if c.Description != "" {
						fmt.Printf("  %s - %s\n", c.Name, c.Description)
					} else {
						fmt.Printf("  %s\n", c.Name)
					}
				}
			}
		} else if resp.Description != "" {
			fmt.Printf("Description: %s\n", resp.Description)
		} else {
			fmt.Println("(no manifest)")
		}

	case "list":
		req := &agentpb.ListHandlesRequest{}
		if len(args) >= 2 {
			req.Tags = strings.Split(args[1], ",")
		}
		resp, err := client.ListHandles(ctx, req)
		if err != nil {
			logger.Error("list handles failed", "error", err)
			os.Exit(1)
		}
		if len(resp.Entries) == 0 {
			fmt.Println("No handles found")
		} else {
			for _, e := range resp.Entries {
				desc := ""
				if e.Manifest != nil && e.Manifest.Description != "" {
					desc = " - " + e.Manifest.Description
				}
				tags := ""
				if e.Manifest != nil && len(e.Manifest.Tags) > 0 {
					tags = " [" + strings.Join(e.Manifest.Tags, ", ") + "]"
				}
				fmt.Printf("  %s%s%s\n", e.Handle, desc, tags)
			}
		}

	case "open":
		if len(args) < 4 {
			fmt.Println("Usage: tailbus open <from> <to> <message>")
			os.Exit(1)
		}
		resp, err := client.OpenSession(ctx, &agentpb.OpenSessionRequest{
			FromHandle:  args[1],
			ToHandle:    args[2],
			Payload:     []byte(args[3]),
			ContentType: "text/plain",
		})
		if err != nil {
			logger.Error("open session failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Session: %s\nMessage: %s\n", resp.SessionId, resp.MessageId)

	case "send":
		if len(args) < 4 {
			fmt.Println("Usage: tailbus send <session-id> <from> <message>")
			os.Exit(1)
		}
		resp, err := client.SendMessage(ctx, &agentpb.SendMessageRequest{
			SessionId:   args[1],
			FromHandle:  args[2],
			Payload:     []byte(args[3]),
			ContentType: "text/plain",
		})
		if err != nil {
			logger.Error("send failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Message: %s\n", resp.MessageId)

	case "subscribe":
		if len(args) < 2 {
			fmt.Println("Usage: tailbus subscribe <handle>")
			os.Exit(1)
		}
		stream, err := client.Subscribe(ctx, &agentpb.SubscribeRequest{Handle: args[1]})
		if err != nil {
			logger.Error("subscribe failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Subscribed as %q, waiting for messages...\n", args[1])
		for {
			msg, err := stream.Recv()
			if err != nil {
				logger.Error("stream error", "error", err)
				os.Exit(1)
			}
			env := msg.Envelope
			fmt.Printf("[%s] %s -> %s: %s\n", env.SessionId[:8], env.FromHandle, env.ToHandle, string(env.Payload))
		}

	case "resolve":
		if len(args) < 3 {
			fmt.Println("Usage: tailbus resolve <session-id> <from> [message]")
			os.Exit(1)
		}
		var payload []byte
		if len(args) >= 4 {
			payload = []byte(args[3])
		}
		resp, err := client.ResolveSession(ctx, &agentpb.ResolveSessionRequest{
			SessionId:   args[1],
			FromHandle:  args[2],
			Payload:     payload,
			ContentType: "text/plain",
		})
		if err != nil {
			logger.Error("resolve failed", "error", err)
			os.Exit(1)
		}
		fmt.Printf("Resolved. Message: %s\n", resp.MessageId)

	case "sessions":
		if len(args) < 2 {
			fmt.Println("Usage: tailbus sessions <handle>")
			os.Exit(1)
		}
		resp, err := client.ListSessions(ctx, &agentpb.ListSessionsRequest{Handle: args[1]})
		if err != nil {
			logger.Error("list sessions failed", "error", err)
			os.Exit(1)
		}
		for _, s := range resp.Sessions {
			fmt.Printf("  %s  %s -> %s  [%s]\n", s.SessionId[:8], s.FromHandle, s.ToHandle, s.State)
		}

	case "agent":
		if err := runAgent(client, logger); err != nil {
			logger.Error("agent bridge error", "error", err)
			os.Exit(1)
		}

	case "dashboard":
		if err := runDashboard(client); err != nil {
			logger.Error("dashboard error", "error", err)
			os.Exit(1)
		}

	case "trace":
		if len(args) < 2 {
			fmt.Println("Usage: tailbus trace <trace-id>")
			os.Exit(1)
		}
		resp, err := client.GetTrace(ctx, &agentpb.GetTraceRequest{TraceId: args[1]})
		if err != nil {
			logger.Error("get trace failed", "error", err)
			os.Exit(1)
		}
		if len(resp.Spans) == 0 {
			fmt.Println("No spans found for trace", args[1])
			os.Exit(0)
		}
		fmt.Printf("Trace %s (%d spans):\n\n", args[1], len(resp.Spans))
		for _, span := range resp.Spans {
			ts := span.Timestamp.AsTime().Format("15:04:05.000")
			action := span.Action.String()
			meta := ""
			if len(span.Metadata) > 0 {
				var parts []string
				for k, v := range span.Metadata {
					parts = append(parts, k+"="+v)
				}
				meta = " " + fmt.Sprintf("%v", parts)
			}
			fmt.Printf("  %s  %-40s  msg:%s  node:%s%s\n", ts, action, short(span.MessageId), span.NodeId, meta)
		}

	default:
		fmt.Printf("Unknown command: %s\n", args[0])
		os.Exit(1)
	}
}
