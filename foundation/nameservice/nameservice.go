// Package nameservice reads the zblock/accounts folder and creates a name
// service lookup for the ardan accounts.
package nameservice

import (
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
)

// NameService maintains a map for account name to account address.
type NameService struct {
	accounts map[string]string
}

// New constructs an Ardan Name Service with accounts from the zblock/accounts folder.
func New(root string) (*NameService, error) {
	ns := NameService{
		accounts: make(map[string]string),
	}

	fn := func(fileName string, info fs.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walkdir failure: %w", err)
		}

		if path.Ext(fileName) != ".ecdsa" {
			return nil
		}

		privateKey, err := crypto.LoadECDSA(fileName)
		if err != nil {
			return err
		}
		address := crypto.PubkeyToAddress(privateKey.PublicKey)

		ns.accounts[address.String()] = strings.TrimSuffix(path.Base(fileName), ".ecdsa")
		return nil
	}

	if err := filepath.Walk(root, fn); err != nil {
		return nil, fmt.Errorf("walking directory: %w", err)
	}

	return &ns, nil
}

// Lookup returns the name for the specified address.
func (ns *NameService) Lookup(address string) string {
	name, exists := ns.accounts[address]
	if !exists {
		return address
	}
	return name
}

// Copy returns a copy of the map of names and accounts.
func (ns *NameService) Copy() map[string]string {
	cpy := make(map[string]string, len(ns.accounts))
	for address, name := range ns.accounts {
		cpy[address] = name
	}
	return cpy
}
