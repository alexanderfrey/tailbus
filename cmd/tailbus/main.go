package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"

	agentpb "github.com/alexanderfrey/tailbus/api/agentpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() {
	socketPath := flag.String("socket", "/tmp/tailbusd.sock", "daemon Unix socket path")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	args := flag.Args()
	if len(args) == 0 {
		fmt.Println("Usage: tailbus [command] [args...]")
		fmt.Println("Commands: register, open, send, subscribe, resolve, sessions, dashboard")
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

	case "dashboard":
		if err := runDashboard(client); err != nil {
			logger.Error("dashboard error", "error", err)
			os.Exit(1)
		}

	default:
		fmt.Printf("Unknown command: %s\n", args[0])
		os.Exit(1)
	}
}
