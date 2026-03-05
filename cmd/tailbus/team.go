package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"time"

	pb "github.com/alexanderfrey/tailbus/api/coordpb"
	"github.com/alexanderfrey/tailbus/internal/auth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

func runTeam(args []string, logger *slog.Logger) {
	if len(args) == 0 {
		fmt.Println("Usage: tailbus team <subcommand>")
		fmt.Println("\nSubcommands:")
		fmt.Println("  create <name>              Create a team (you become owner)")
		fmt.Println("  list                       List your teams")
		fmt.Println("  members [name]             List team members")
		fmt.Println("  invite <name>              Generate invite code")
		fmt.Println("  join <code>                Accept invite and join team")
		fmt.Println("  switch <name>              Set active team in credentials")
		fmt.Println("  remove <name> <email>      Remove a member (owner only)")
		fmt.Println("  role <name> <email> <role>  Change member role (owner only)")
		fmt.Println("  delete <name>              Delete a team (owner only)")
		os.Exit(1)
	}

	switch args[0] {
	case "create":
		teamCreate(args[1:], logger)
	case "list":
		teamList(args[1:], logger)
	case "members":
		teamMembers(args[1:], logger)
	case "invite":
		teamInvite(args[1:], logger)
	case "join":
		teamJoin(args[1:], logger)
	case "switch":
		teamSwitch(args[1:], logger)
	case "remove":
		teamRemove(args[1:], logger)
	case "role":
		teamRole(args[1:], logger)
	case "delete":
		teamDelete(args[1:], logger)
	default:
		fmt.Printf("Unknown team subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

func teamCoordClient(credsPath string) (pb.CoordinationAPIClient, *auth.Credentials, func()) {
	creds, err := auth.LoadCredentials(credsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Not logged in. Run 'tailbus login' first.\n")
		os.Exit(1)
	}

	host := stripPort(creds.CoordAddr)
	var transportCreds grpc.DialOption
	if host == "localhost" || host == "127.0.0.1" {
		transportCreds = grpc.WithTransportCredentials(insecure.NewCredentials())
	} else {
		cert, err := ephemeralClientCert()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to generate TLS client cert: %v\n", err)
			os.Exit(1)
		}
		transportCreds = grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{
			Certificates:       []tls.Certificate{cert},
			InsecureSkipVerify: true,
		}))
	}
	conn, err := grpc.NewClient(creds.CoordAddr, transportCreds)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to coord: %v\n", err)
		os.Exit(1)
	}
	return pb.NewCoordinationAPIClient(conn), creds, func() { conn.Close() }
}

func defaultCredsPath(fs *flag.FlagSet) (*string, string) {
	p := fs.String("credentials", "", "path to credentials file")
	return p, ""
}

func resolveCredsPath(p *string) string {
	if p != nil && *p != "" {
		return *p
	}
	return auth.DefaultCredentialFile()
}

func teamCreate(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("team create", flag.ExitOnError)
	credsFlag, _ := defaultCredsPath(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Println("Usage: tailbus team create <name>")
		os.Exit(1)
	}
	name := fs.Arg(0)
	credsPath := resolveCredsPath(credsFlag)

	client, creds, cleanup := teamCoordClient(credsPath)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.CreateTeam(ctx, &pb.CreateTeamRequest{
		AuthToken: creds.AccessToken,
		Name:      name,
	})
	if err != nil {
		logger.Error("create team failed", "error", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Printf("Team %q created (ID: %s)\n", resp.Name, resp.TeamId)
	fmt.Printf("You are the owner. Use 'tailbus team switch %s' to activate.\n", name)
}

func teamList(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("team list", flag.ExitOnError)
	credsFlag, _ := defaultCredsPath(fs)
	fs.Parse(args)
	credsPath := resolveCredsPath(credsFlag)

	client, creds, cleanup := teamCoordClient(credsPath)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.ListTeams(ctx, &pb.ListTeamsRequest{
		AuthToken: creds.AccessToken,
	})
	if err != nil {
		logger.Error("list teams failed", "error", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	if len(resp.Teams) == 0 {
		fmt.Println("No teams. Create one with 'tailbus team create <name>'")
		return
	}

	activeTeam := creds.TeamID
	for _, t := range resp.Teams {
		active := ""
		if t.TeamId == activeTeam {
			active = " (active)"
		}
		fmt.Printf("  %s  [%s]%s\n", t.Name, t.Role, active)
	}
}

func teamMembers(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("team members", flag.ExitOnError)
	credsFlag, _ := defaultCredsPath(fs)
	fs.Parse(args)
	credsPath := resolveCredsPath(credsFlag)

	client, creds, cleanup := teamCoordClient(credsPath)
	defer cleanup()

	// Resolve team: argument or active team
	var teamID string
	if fs.NArg() >= 1 {
		teamID = resolveTeamIDByName(client, creds, fs.Arg(0), logger)
	} else if creds.TeamID != "" {
		teamID = creds.TeamID
	} else {
		fmt.Println("Usage: tailbus team members <name>  (or set active team with 'tailbus team switch')")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.GetTeamMembers(ctx, &pb.GetTeamMembersRequest{
		AuthToken: creds.AccessToken,
		TeamId:    teamID,
	})
	if err != nil {
		logger.Error("get members failed", "error", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	for _, m := range resp.Members {
		fmt.Printf("  %s  [%s]\n", m.Email, m.Role)
	}
}

func teamInvite(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("team invite", flag.ExitOnError)
	credsFlag, _ := defaultCredsPath(fs)
	uses := fs.Int("uses", 1, "max number of uses")
	ttl := fs.String("ttl", "7d", "invite TTL (e.g. 7d, 24h)")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Println("Usage: tailbus team invite <name> [--uses N] [--ttl 7d]")
		os.Exit(1)
	}
	name := fs.Arg(0)
	credsPath := resolveCredsPath(credsFlag)

	client, creds, cleanup := teamCoordClient(credsPath)
	defer cleanup()

	teamID := resolveTeamIDByName(client, creds, name, logger)

	ttlDur, err := time.ParseDuration(*ttl)
	if err != nil {
		// Try parsing "7d" format
		if len(*ttl) > 1 && (*ttl)[len(*ttl)-1] == 'd' {
			days := 0
			fmt.Sscanf(*ttl, "%dd", &days)
			if days > 0 {
				ttlDur = time.Duration(days) * 24 * time.Hour
			} else {
				fmt.Fprintf(os.Stderr, "Invalid TTL: %s\n", *ttl)
				os.Exit(1)
			}
		} else {
			fmt.Fprintf(os.Stderr, "Invalid TTL: %s\n", *ttl)
			os.Exit(1)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.CreateTeamInvite(ctx, &pb.CreateTeamInviteRequest{
		AuthToken:  creds.AccessToken,
		TeamId:     teamID,
		MaxUses:    int32(*uses),
		TtlSeconds: int64(ttlDur.Seconds()),
	})
	if err != nil {
		logger.Error("create invite failed", "error", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	expires := time.Unix(resp.ExpiresAt, 0).Format(time.RFC3339)
	fmt.Printf("Invite code: %s\n", resp.Code)
	fmt.Printf("Expires: %s  Max uses: %d\n", expires, *uses)
	fmt.Printf("\nShare this code. Others can join with:\n  tailbus team join %s\n", resp.Code)
}

func teamJoin(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("team join", flag.ExitOnError)
	credsFlag, _ := defaultCredsPath(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Println("Usage: tailbus team join <code>")
		os.Exit(1)
	}
	code := fs.Arg(0)
	credsPath := resolveCredsPath(credsFlag)

	client, creds, cleanup := teamCoordClient(credsPath)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.AcceptTeamInvite(ctx, &pb.AcceptTeamInviteRequest{
		AuthToken: creds.AccessToken,
		Code:      code,
	})
	if err != nil {
		logger.Error("join team failed", "error", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	fmt.Printf("Joined team %q (ID: %s)\n", resp.TeamName, resp.TeamId)
	fmt.Printf("Use 'tailbus team switch %s' to activate.\n", resp.TeamName)
}

func teamSwitch(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("team switch", flag.ExitOnError)
	credsFlag, _ := defaultCredsPath(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Println("Usage: tailbus team switch <name>")
		fmt.Println("Use empty string to switch to personal mode:")
		fmt.Println("  tailbus team switch \"\"")
		os.Exit(1)
	}
	name := fs.Arg(0)
	credsPath := resolveCredsPath(credsFlag)

	creds, err := auth.LoadCredentials(credsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Not logged in. Run 'tailbus login' first.\n")
		os.Exit(1)
	}

	if name == "" || name == "personal" {
		creds.TeamID = ""
		creds.TeamName = ""
		if err := auth.SaveCredentials(credsPath, creds); err != nil {
			logger.Error("failed to save credentials", "error", err)
			os.Exit(1)
		}
		fmt.Println("Switched to personal mode (no team).")
		fmt.Println("Restart tailbusd for the change to take effect.")
		return
	}

	// Look up team by name to get ID
	client, _, cleanup := teamCoordClient(credsPath)
	defer cleanup()
	teamID := resolveTeamIDByName(client, creds, name, logger)

	creds.TeamID = teamID
	creds.TeamName = name
	if err := auth.SaveCredentials(credsPath, creds); err != nil {
		logger.Error("failed to save credentials", "error", err)
		os.Exit(1)
	}
	fmt.Printf("Active team set to %q (ID: %s)\n", name, teamID)
	fmt.Println("Restart tailbusd for the change to take effect.")
}

func teamRemove(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("team remove", flag.ExitOnError)
	credsFlag, _ := defaultCredsPath(fs)
	fs.Parse(args)
	if fs.NArg() < 2 {
		fmt.Println("Usage: tailbus team remove <team-name> <email>")
		os.Exit(1)
	}
	name := fs.Arg(0)
	email := fs.Arg(1)
	credsPath := resolveCredsPath(credsFlag)

	client, creds, cleanup := teamCoordClient(credsPath)
	defer cleanup()

	teamID := resolveTeamIDByName(client, creds, name, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.RemoveTeamMember(ctx, &pb.RemoveTeamMemberRequest{
		AuthToken: creds.AccessToken,
		TeamId:    teamID,
		Email:     email,
	})
	if err != nil {
		logger.Error("remove member failed", "error", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Printf("Removed %s from %s\n", email, name)
}

func teamRole(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("team role", flag.ExitOnError)
	credsFlag, _ := defaultCredsPath(fs)
	fs.Parse(args)
	if fs.NArg() < 3 {
		fmt.Println("Usage: tailbus team role <team-name> <email> <owner|member>")
		os.Exit(1)
	}
	name := fs.Arg(0)
	email := fs.Arg(1)
	role := fs.Arg(2)
	credsPath := resolveCredsPath(credsFlag)

	client, creds, cleanup := teamCoordClient(credsPath)
	defer cleanup()

	teamID := resolveTeamIDByName(client, creds, name, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.UpdateTeamMemberRole(ctx, &pb.UpdateTeamMemberRoleRequest{
		AuthToken: creds.AccessToken,
		TeamId:    teamID,
		Email:     email,
		Role:      role,
	})
	if err != nil {
		logger.Error("update role failed", "error", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Printf("Updated %s to %s in %s\n", email, role, name)
}

func teamDelete(args []string, logger *slog.Logger) {
	fs := flag.NewFlagSet("team delete", flag.ExitOnError)
	credsFlag, _ := defaultCredsPath(fs)
	fs.Parse(args)
	if fs.NArg() < 1 {
		fmt.Println("Usage: tailbus team delete <team-name>")
		os.Exit(1)
	}
	name := fs.Arg(0)
	credsPath := resolveCredsPath(credsFlag)

	client, creds, cleanup := teamCoordClient(credsPath)
	defer cleanup()

	teamID := resolveTeamIDByName(client, creds, name, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.DeleteTeam(ctx, &pb.DeleteTeamRequest{
		AuthToken: creds.AccessToken,
		TeamId:    teamID,
	})
	if err != nil {
		logger.Error("delete team failed", "error", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}
	fmt.Printf("Team %q deleted\n", name)

	// Clear from credentials if it was the active team
	if creds.TeamID == teamID {
		creds.TeamID = ""
		creds.TeamName = ""
		auth.SaveCredentials(credsPath, creds)
		fmt.Println("Active team cleared (was the deleted team).")
	}
}

// ephemeralClientCert generates a throwaway self-signed TLS client certificate.
// The coord server requires any client cert (RequireAnyClientCert) but the CLI
// authenticates via JWT tokens in the request body, not via mTLS identity.
func ephemeralClientCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "tailbus-cli",
			Organization: []string{fmt.Sprintf("%064x", 0)}, // dummy pubkey for coord mTLS verification
		},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

// resolveTeamIDByName lists the user's teams and finds one by name.
func resolveTeamIDByName(client pb.CoordinationAPIClient, creds *auth.Credentials, name string, logger *slog.Logger) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.ListTeams(ctx, &pb.ListTeamsRequest{
		AuthToken: creds.AccessToken,
	})
	if err != nil {
		logger.Error("list teams failed", "error", err)
		os.Exit(1)
	}
	if resp.Error != "" {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Error)
		os.Exit(1)
	}

	for _, t := range resp.Teams {
		if t.Name == name {
			return t.TeamId
		}
	}

	fmt.Fprintf(os.Stderr, "Team %q not found. Use 'tailbus team list' to see your teams.\n", name)
	os.Exit(1)
	return ""
}
