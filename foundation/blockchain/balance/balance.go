// Package balance maintains account balances in memory.
package balance

import (
	"fmt"
	"sync"

	"github.com/ardanlabs/blockchain/foundation/blockchain/storage"
)

// Sheet represents the data representation to maintain address balances.
type Sheet struct {
	miningReward uint
	sheet        map[string]uint
	mu           sync.RWMutex
}

// NewSheet constructs a new balance sheet for use, expects a starting
// balance sheet usually from a genesis file.
func NewSheet(miningReward uint, sheet map[string]uint) *Sheet {
	bs := Sheet{
		miningReward: miningReward,
		sheet:        make(map[string]uint),
	}

	if sheet != nil {
		bs.Reset(sheet)
	}

	return &bs
}

// Reset takes the specified sheet and resets the balances.
func (bs *Sheet) Reset(sheet map[string]uint) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	bs.sheet = make(map[string]uint)
	for address, value := range sheet {
		bs.sheet[address] = value
	}
}

// Replace updates the balance sheet for a new version.
func (bs *Sheet) Replace(newBS *Sheet) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	bs.sheet = newBS.sheet
}

// Remove deletes the address from the balance sheet.
func (bs *Sheet) Remove(address string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	delete(bs.sheet, address)
}

// Clone makes a copy of the current balance sheet.
func (bs *Sheet) Clone() *Sheet {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	balanceSheet := NewSheet(bs.miningReward, nil)
	for address, value := range bs.sheet {
		balanceSheet.sheet[address] = value
	}
	return balanceSheet
}

// Values makes a copy of the current balance sheet but returns the raw values.
func (bs *Sheet) Values() map[string]uint {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	sheet := make(map[string]uint)
	for address, value := range bs.sheet {
		sheet[address] = value
	}
	return sheet
}

// ApplyMiningReward gives the specififed address the mining reward.
func (bs *Sheet) ApplyMiningReward(minerAddress string) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	bs.sheet[minerAddress] += bs.miningReward
}

// ApplyTransaction performs the business logic for applying a transaction
// to the balance sheet.
func (bs *Sheet) ApplyTransaction(minerAddress string, tx storage.BlockTx) error {

	// Capture the address of the account that signed this transaction.
	from, err := tx.FromAddress()
	if err != nil {
		return fmt.Errorf("invalid signature, %s", err)
	}

	bs.mu.Lock()
	defer bs.mu.Unlock()
	{
		if from == tx.To {
			return fmt.Errorf("invalid transaction, sending money to yourself, from %s, to %s", from, tx.To)
		}

		if tx.Value > bs.sheet[from] {
			return fmt.Errorf("%s has an insufficient balance", from)
		}

		bs.sheet[from] -= tx.Value
		bs.sheet[tx.To] += tx.Value

		fee := tx.Gas + tx.Tip
		bs.sheet[minerAddress] += fee
		bs.sheet[from] -= fee
	}

	return nil
}
