package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"os"
)

// Keypair holds an Ed25519 key pair for node identity.
type Keypair struct {
	Public  ed25519.PublicKey
	Private ed25519.PrivateKey
}

// Generate creates a new Ed25519 keypair.
func Generate() (*Keypair, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate keypair: %w", err)
	}
	return &Keypair{Public: pub, Private: priv}, nil
}

// SavePrivateKey writes the private key to a PEM file.
func (kp *Keypair) SavePrivateKey(path string) error {
	block := &pem.Block{
		Type:  "ED25519 PRIVATE KEY",
		Bytes: kp.Private.Seed(),
	}
	data := pem.EncodeToMemory(block)
	return os.WriteFile(path, data, 0600)
}

// LoadPrivateKey reads a private key from a PEM file and reconstructs the keypair.
func LoadPrivateKey(path string) (*Keypair, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "ED25519 PRIVATE KEY" {
		return nil, fmt.Errorf("invalid PEM block type")
	}
	if len(block.Bytes) != ed25519.SeedSize {
		return nil, fmt.Errorf("invalid seed size: %d", len(block.Bytes))
	}
	priv := ed25519.NewKeyFromSeed(block.Bytes)
	pub := priv.Public().(ed25519.PublicKey)
	return &Keypair{Public: pub, Private: priv}, nil
}

// LoadOrGenerate loads a keypair from path, or generates a new one and saves it.
func LoadOrGenerate(path string) (*Keypair, error) {
	if _, err := os.Stat(path); err == nil {
		return LoadPrivateKey(path)
	}
	kp, err := Generate()
	if err != nil {
		return nil, err
	}
	if err := kp.SavePrivateKey(path); err != nil {
		return nil, fmt.Errorf("save key: %w", err)
	}
	return kp, nil
}
